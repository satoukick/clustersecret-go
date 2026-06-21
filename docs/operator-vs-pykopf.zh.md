# ClusterSecret: Python/Kopf 与 Go/controller-runtime 对比

本文对比了 ClusterSecret 的原始 Python/Kopf 实现与 Go/controller-runtime
重写版本。面向熟悉 Kubernetes Operator 模式的读者，帮助理解两种方案之间的
设计取舍。

---

## 总览

| 维度 | Python / Kopf | Go / controller-runtime |
|------|---------------|------------------------|
| 语言 | Python 3 | Go 1.26+ |
| 框架 | [Kopf](https://kopf.readthedocs.io/)（Kubernetes Operator 框架） | [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) |
| API 风格 | 装饰器驱动（声明式） | 接口驱动（`Reconcile(ctx, req)`） |
| 状态管理 | 内存字典（UID → body） | Informer 缓存（API Server 背书） |
| CRD Schema | Preserve-unknown-fields | 显式 Schema + 校验 |
| 构建工具 | setup.cfg / pip | go build / Makefile |
| 部署方式 | Helm Chart + yaml | kubectl apply（静态清单） |
| Leader Election | 不支持（仅单副本） | 内置，基于 Leases |

---

## 1. 框架哲学

### Kopf（Python）

Kopf 使用 Python 装饰器注册事件处理器。每个处理器是一个普通函数，
接收事件负载（字典形式）：

```python
@kopf.on.create('clustersecret.io', 'v1', 'clustersecrets')
async def create_fn(logger, uid, name, body, **_):
    matchedns = get_ns_list(logger, body, v1)
    for ns in matchedns:
        sync_secret(logger, ns, body, v1)
    return {'syncedns': matchedns}
```

**优点：**
- 样板代码少 —— 50 行就能跑起来一个 Operator
- 熟悉的 Python 生态（pydantic、pytest）
- 默认异步（asyncio 事件循环）

**缺点：**
- 所有东西都是 `Dict[str, Any]` —— CRD 字段没有类型安全
- 事件驱动（边沿触发）：如果 Operator 错过一个事件，状态就丢了
- 装饰器隐藏了执行顺序和生命周期 —— 难以推理错误路径

### controller-runtime（Go）

controller-runtime 使用单一的 `Reconcile` 接口。监听到资源的每个事件
都会产生一个 `reconcile.Request`（只有 name/namespace 元组）。
Reconciler 读取当前状态并收敛：

```go
func (r *ClusterSecretReconciler) Reconcile(
    ctx context.Context, req ctrl.Request,
) (ctrl.Result, error) {
    var csec clustersecretv1.ClusterSecret
    if err := r.Get(ctx, req.NamespacedName, &csec); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }
    // ... converge
}
```

**优点：**
- 类型安全的 CRD 结构体（从 Go 类型生成）
- 水平触发：重启安全，事件顺序无关紧要
- 对 Manager 生命周期、缓存和工作队列有显式控制

**缺点：**
- 更多样板代码（Scheme 注册、Controller 设置、DeepCopy）
- Go 的类型系统严格 —— 每次字段变更都需要重新生成 DeepCopy
- 原型开发速度较慢

---

## 2. 状态管理

这是最重要的架构差异。

### Python：内存缓存

```python
# handlers.py
csecs_cache: Cache = MemoryCache()  # Dict[str, BaseClusterSecret]

@kopf.on.startup()
async def startup_fn(logger, **_):
    cluster_secrets = get_custom_objects_by_kind(...)
    for item in cluster_secrets:
        csecs_cache.set_cluster_secret(BaseClusterSecret(
            uid=metadata.get('uid'),
            name=metadata.get('name'),
            body=item,
            synced_namespace=item.get('status', {}).get('create_fn', {}).get('syncedns', []),
        ))
```

Python 版本维护一个**直写式内存缓存**，以 UID 为键。这个缓存：

1. **易失性**：重启后全部丢失 → 启动时必须重新列出所有资源
2. **手动维护**：每个处理器必须显式读写缓存
3. **边沿触发**：缓存只反映 Operator 看到的事件

如果在 Operator 宕机期间创建了 `ClusterSecret`，启动时的重新列表可以
捕获它。但如果缓存与实际状态产生偏差（例如错过了一个更新事件），
Operator 会静默地在过期数据上操作。

### Go：Informer 缓存

```go
// controller-runtime 的共享 Informer 缓存 —— 不需要显式缓存代码
mgr, _ := manager.New(config, manager.Options{
    Scheme: scheme,
    // ...
})
// r.Get() 从缓存读取，而不是 API Server
```

controller-runtime 维护一个**共享 Informer 缓存**：

1. **跨 Reconcile 持久化**：存在于 Manager 中，被所有 Controller 共享
2. **自愈**：Watch 连接出错后自动重建；错过的事件触发完整重新列表
3. **水平触发**：`Get()` 始终返回最新的观测状态，而不是触发 Reconcile 的事件

Go 版本有**零行**缓存管理代码 —— 框架全权处理。Reconciler 直接从缓存
读取状态并收敛。

---

## 3. CRD Schema 设计

### Python：Preserve-Unknown-Fields

```yaml
# config/crd/clustersecret-crd.yaml（Python 版本）
spec:
  x-kubernetes-preserve-unknown-fields: true
```

`valueFrom` 嵌套在 `data` 内部：

```yaml
spec:
  data:
    valueFrom:
      secretKeyRef:
        name: root-ca
        namespace: cert-manager
```

这意味着：
- API Server 接受 `spec` 中的**任意字段** —— 拼写错误被静默忽略
- Operator 必须手动校验结构
- `data` 和 `valueFrom` 共存于同一个 map，校验变得别扭
- 没有 Printer Columns，没有 Status 子资源

### Go：显式 Schema

```go
type ClusterSecretSpec struct {
    MatchNamespace  []string          `json:"matchNamespace,omitempty"`
    AvoidNamespaces []string          `json:"avoidNamespaces,omitempty"`
    Data            map[string]string `json:"data,omitempty"`
    ValueFrom       *ValueFromSource  `json:"valueFrom,omitempty"`
    Type            string            `json:"type,omitempty"`
}

type ValueFromSource struct {
    Name      string   `json:"name"`
    Namespace string   `json:"namespace"`
    Keys      []string `json:"keys,omitempty"`
}
```

`valueFrom` 被提升为顶层字段，与 `data` 分离：

```yaml
spec:
  valueFrom:
    name: root-ca
    namespace: cert-manager
    keys: ["ca.crt"]
```

这意味着：
- API Server 在准入阶段就拒绝不合法的 Spec
- `data` 和 `valueFrom` **在类型层面互斥** —— Operator 会显式检查并返回错误
- Printer Columns 和 Status 子资源通过声明式配置
- `kubectl get csec` 无需额外参数就能显示有用信息

---

## 4. 清理机制

### Python：无 Finalizer

```python
@kopf.on.delete('clustersecret.io', 'v1', 'clustersecrets')
def on_delete(body, uid, name, logger, **_):
    syncedns = body.get('status', {}).get('create_fn', {}).get('syncedns', [])
    for ns in syncedns:
        delete_secret(logger, ns, name, v1)
    csecs_cache.remove_cluster_secret(uid)
```

Kopf 透明地处理 Finalizer，但 Python 版本**没有**显式使用 Finalizer。
如果在删除 `ClusterSecret` 时 Operator 宕机，API Server 会立即删除对象，
子 Secret 成为孤儿。它们会一直存在直到被手动清理。

### Go：显式 Finalizer

```go
const clusterSecretFinalizer = "clustersecret.io/finalizer"

// 在创建子 Secret 之前确保 Finalizer 存在
if !controllerutil.ContainsFinalizer(&csec, clusterSecretFinalizer) {
    controllerutil.AddFinalizer(&csec, clusterSecretFinalizer)
    r.Update(ctx, &csec)
    return ctrl.Result{}, nil
}
```

Go 版本在**创建任何子 Secret 之前**就添加 Finalizer。这保证了：

- API Server 在 Finalizer 被移除之前无法删除 `ClusterSecret`
- 如果 Operator 宕机，`ClusterSecret` 保持在 `Terminating` 状态
- 重启后 Operator 会 Reconcile 这个 `Terminating` 对象，清理子 Secret，
  然后移除 Finalizer → 删除完成
- **任何故障场景下零孤儿 Secret**

---

## 5. 标签保护

### Python：基于注解

```python
CREATE_BY_ANNOTATION = 'clustersecret.io/created-by'
CLUSTER_SECRET_LABEL = "clustersecret.io"
BLOCKED_ANNOTATIONS = ["kopf.zalando.org", "kubectl.kubernetes.io"]
BLOCKED_LABELS = ["app.kubernetes.io"]
```

Python 版本使用注解 `clustersecret.io/created-by` 标记被管理的 Secret，
并在覆盖前检查。但是：

- 检查只在**已有** Secret 上执行 —— 不会阻止在用户创建的同名 Secret 上
  执行初始创建
- 用户可以移除注解，破坏保护
- `REPLACE_EXISTING` 环境变量可以完全绕过保护

### Go：基于标签

```go
const managedByLabel  = "clustersecret.io/managed-by"
const managedByValue  = "clustersecret-operator"
const parentNameLabel = "clustersecret.io/parent"
```

Go 版本使用标签（`clustersecret.io/managed-by`）加特定值
（`clustersecret-operator`）。在 `syncSecretToNamespace` 和
`deleteSecretFromNamespace` 中都会检查：

```go
if existing, ok := secret.Labels[managedByLabel]; ok && existing != managedByValue {
    return fmt.Errorf("refusing to overwrite Secret %s/%s not managed by clustersecret-operator", ns, secret.Name)
}
```

关键区别：
- **创建和删除**路径都受保护
- 标签是 `CreateOrUpdate` 会合并的对象元数据的一部分 —— 用户无法
  在不触发重新同步的情况下意外移除
- `parentNameLabel` 便于发现：`kubectl get secret --all-namespaces -l clustersecret.io/parent=<name>`

---

## 6. 事件处理

### Python：按字段边沿触发

```python
@kopf.on.field('clustersecret.io', 'v1', 'clustersecrets', field='data')
def on_field_data(old, new, body, name, uid, logger, reason, **_):
    if reason == "create":
        return  # 创建时跳过 —— create_fn 处理
    # 重新同步到所有已有命名空间
    for ns in syncedns:
        sync_secret(logger, ns, body, v1)
```

Kopf 为每个字段变更触发独立的处理器。这样高效（只有相关处理器运行）
但脆弱：

- `data` 处理器和 `matchNamespace` 处理器逻辑相似但不相同 —— bug 可能发散
- 两个处理器都必须独立更新内存缓存
- `namespace_watcher` 只处理命名空间**创建**，不处理删除
  （已删除的命名空间会在 `syncedns` 中留下过期条目）

### Go：统一的 Reconcile 循环

```go
func (r *ClusterSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // 1. 获取 → 2. 删除？ → 3. Finalizer → 4. 解析 → 5. 匹配 → 6. 同步 → 7. 清理 → 8. 状态
}
```

每个事件 —— 无论是数据变更、模式变更还是命名空间事件 —— 都经过**同一个**
Reconcile 循环。循环读取当前状态并收敛。这意味着：

- 一条代码路径，一套 bug
- 没有缓存漂移：循环始终从 Informer 缓存读取
- 没有特殊情况：创建 `ClusterSecret`、修改 `data` 和修改 `matchNamespace`
  都走同样的 `sync` → `cleanup` → `status` 流程
- 命名空间**删除**被隐式处理：`listMatchingNamespaces` 会跳过
  `Terminating` 状态的命名空间，清理分支会自动移除它们的 Secret

---

## 7. Namespace 监听

### Python

```python
@kopf.on.create('', 'v1', 'namespaces')
async def namespace_watcher(logger, meta, **_):
    # 仅监听 CREATE —— 不监听 update 或 delete
    for cluster_secret in csecs_cache.all_cluster_secret():
        # 如果新命名空间匹配，重新计算并同步
```

仅监听命名空间**创建**。命名空间删除不会触发清理 —— Operator 依赖
下一次数据变更来发现命名空间已消失。

### Go

```go
Watches(
    &corev1.Namespace{},
    handler.EnqueueRequestsFromMapFunc(r.findClusterSecretsForNamespace),
)
```

监听所有命名空间事件（创建、更新、删除）。每个事件都将**所有**
ClusterSecret 入队以重新 Reconcile。Reconcile 循环随后：

- 重新计算 `listMatchingNamespaces`（跳过 `Terminating` 命名空间）
- 清理不再匹配或已被删除的命名空间中的 Secret

---

## 8. 生产就绪度

| 特性 | Python | Go |
|------|--------|-----|
| 健康探针 | ❌ 未暴露 | ✅ /healthz + /readyz |
| Prometheus 指标 | ❌ 未接入 | ✅ 4 个自定义指标 + 运行时默认指标 |
| Leader Election | ❌ 不支持 | ✅ Leases，15 秒时长 |
| 资源限制 | ❌ 清单中未定义 | ✅ deployment.yaml 中配置了 Requests + Limits |
| Pod 安全 | ❌ 未配置 | ✅ runAsNonRoot、readOnlyRootFilesystem、seccomp |
| 多架构镜像 | ✅（amd64、arm64、s390x） | ✅（amd64 + arm64，通过 buildx） |
| 调试日志 | ✅ 详细 | ✅ 结构化 zap（开发模式） |
| 优雅关闭 | ❌ 难以控制 | ✅ 通过 Manager 的信号处理 |

---

## 总结

Python/Kopf 版本非常适合**快速原型开发** —— 一个下午就能从想法到可运行的
Operator。这是原始 ClusterSecret 作者的选择，并且多年来运行良好。

Go/controller-runtime 版本用开发速度换取了**运维安全性**：

| 关注点 | Python/Kopf | Go/controller-runtime |
|--------|-------------|----------------------|
| 状态安全 | 内存缓存，重启丢失 | Informer 缓存，重启不丢失 |
| 清理保障 | 尽力而为（可能产生孤儿） | Finalizer 保证（无孤儿） |
| Schema 校验 | 运行时（Operator 必须检查） | 准入阶段（API Server 拒绝） |
| 代码复杂度 | 低（~250 行） | 较高（~700 行） |
| 学习曲线 | 平缓（Python + 装饰器） | 陡峭（Go + 接口 + 通道） |
| 生产就绪度 | 低（无 HA、无指标） | 高（HA、指标、探针、安全） |

**何时使用哪个：**

- **Python/Kopf**：原型开发、概念验证、内部工具、已经投入 Python 的团队
- **Go/controller-runtime**：生产级 Operator、多副本部署、安全敏感环境、
  重视类型安全的团队
