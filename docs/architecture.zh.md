# ClusterSecret Operator — 架构说明

## 概述

ClusterSecret 是一个 Kubernetes Operator，负责根据正则表达式模式在命名空间之间同步 Secret 数据。它基于
[controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)
实现——没有使用 kubebuilder 或 operator-sdk——作为从零构建生产级 Operator 的学习实践项目。

## 架构图

```
┌──────────────────────────────────────────────────────────────────┐
│                      Kubernetes API Server                        │
│                                                                  │
│  ┌──────────────────┐     ┌──────────────────┐                  │
│  │ ClusterSecret CR │     │  Namespace  CR   │                  │
│  │ (集群级别)       │     │ (集群级别)       │                  │
│  └────────┬─────────┘     └────────┬─────────┘                  │
│           │ watch                  │ watch                      │
└───────────┼─────────────────────────┼───────────────────────────┘
            │                         │
            ▼                         ▼
┌───────────────────────────────────────────────────────────┐
│                  controller-runtime Manager                 │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐  │
│  │               Informer / Cache（共享缓存）            │  │
│  │  存储类型化对象，减少 API Server 负载，                │  │
│  │  通过 Reader 接口提供线程安全访问。                    │  │
│  └──────────────────────┬──────────────────────────────┘  │
│                         │                                  │
│                         ▼                                  │
│  ┌─────────────────────────────────────────────────────┐  │
│  │               Workqueue（限速队列）                    │  │
│  │  去重并重试 Reconcile 请求。                          │  │
│  │  失败的任务会按指数退避重新入队                        │  │
│  │  （基数 5ms，最长约 16 分钟）。                       │  │
│  └──────────────────────┬──────────────────────────────┘  │
│                         │                                  │
│                         ▼                                  │
│  ┌─────────────────────────────────────────────────────┐  │
│  │              ClusterSecretReconciler                  │  │
│  │                                                      │  │
│  │  1. 获取 ClusterSecret（从缓存）                      │  │
│  │  2. 检查 DeletionTimestamp → 清理分支                 │  │
│  │  3. 确保 Finalizer 存在                               │  │
│  │  4. 解析数据（字面量或 ValueFrom）                    │  │
│  │  5. 列出并匹配命名空间（通过 Informer 缓存）          │  │
│  │  6. 创建或更新子 Secret                               │  │
│  │  7. 删除过期的 Secret（不再匹配的命名空间）           │  │
│  │  8. 更新 status.syncedNamespaces                      │  │
│  └──────────────────────┬──────────────────────────────┘  │
│                         │                                  │
│  ┌─────────────────────────────────────────────────────┐  │
│  │            Metrics 指标 (:8080)                      │  │
│  │  reconcile_total、reconcile_duration_seconds、       │  │
│  │  synced_namespaces（Gauge）、sync_errors_total       │  │
│  └─────────────────────────────────────────────────────┘  │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐  │
│  │            Health 健康检查 (:8081)                    │  │
│  │  /healthz → 存活探针                                │  │
│  │  /readyz → 就绪探针                                 │  │
│  └─────────────────────────────────────────────────────┘  │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐  │
│  │            Leader Election（领导者选举）              │  │
│  │  Leases: clustersecret-operator/clustersecret.io    │  │
│  │  一个副本活跃，N-1 个候选等待。                      │  │
│  │  领导者宕机后 15 秒内自动切换。                      │  │
│  └─────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────┘
            │
            ▼
┌───────────────────────────────────────────────────────────┐
│                  子 Secret（每个匹配的命名空间）            │
│                                                             │
│  命名空间: team-alpha     命名空间: team-beta               │
│  ┌──────────────────┐     ┌──────────────────┐             │
│  │ shared-credentials│     │ shared-credentials│            │
│  │ (Opaque)          │     │ (Opaque)          │            │
│  │ 标签:             │     │ 标签:             │            │
│  │   managed-by: cs  │     │   managed-by: cs  │            │
│  │   parent: shared  │     │   parent: shared  │            │
│  └──────────────────┘     └──────────────────┘             │
└───────────────────────────────────────────────────────────┘
```

---

## 关键技术决策

### 为什么不用 kubebuilder / operator-sdk？

| 关注点 | 决策 |
|--------|------|
| 生成的代码难以调试 | 手动组装迫使理解每个组件 |
| Windows 工具链兼容性 | `controller-gen` 是唯一的外部工具，其余都是纯 Go |
| 学习价值 | 脚手架隐藏了连接逻辑——手动连接 `Manager` → `Controller` → `Reconciler` 才能真正理解架构 |

唯一由工具生成的代码是 `zz_generated.deepcopy.go`（controller-gen 生成），
因为 DeepCopy 方法是机械性的，手写容易出错且难以维护。

### 水平触发（Level-Triggered）的 Reconcile

controller-runtime 是**水平触发**而非边沿触发的。每次 `Reconcile` 调用
从 Informer 缓存读取**当前状态**，然后向期望状态收敛。这意味着：

- 重启 Operator 会自动重新 Reconcile 所有资源（不会丢失状态）
- 事件顺序无关紧要——Reconcile 循环始终收敛到期望状态
- 临时故障通过重新入队 + 指数退避处理

对比 Python/Kopf 版本使用内存缓存（UID → body）。重启后会丢失所有状态，
需要从头重新同步每个 ClusterSecret——而且如果 Operator 错过了事件，
缓存会与实际情况产生偏差。

### Finalizer + 标签保护机制

Operator 使用两种互补机制确保安全清理：

1. **Finalizer**（`clustersecret.io/finalizer`）：阻止 API Server 在
   Operator 清理完所有子 Secret 之前删除 ClusterSecret CR。如果在删除请求
   时 Operator 宕机，ClusterSecret 会保持在 `Terminating` 状态，等
   Operator 重启后继续处理。

2. **`managed-by` 标签**：每个子 Secret 都会被标记
   `clustersecret.io/managed-by: clustersecret-operator`。Operator 拒绝
   修改或删除没有此标签的 Secret，防止误覆盖用户创建的 Secret。

### 监听：主资源 + 从资源

Controller 监听两种资源类型：

| 资源 | 角色 | 触发条件 |
|------|------|----------|
| `ClusterSecret` | 主资源 | 创建/更新/删除时直接触发 Reconcile |
| `Namespace` | 从资源 | 将**所有** ClusterSecret 重新入队以重新评估 |

从资源监听**不做**正则预过滤——它把所有 ClusterSecret 都入队。这是有意为之：
ClusterSecret 的数量通常比命名空间事件量小几个数量级，而 Reconcile 循环
本身就会计算权威的匹配集。预过滤只会增加复杂性和漂移风险，而性能收益可以忽略。

---

## 组件说明

### cmd/main.go

入口点，负责：

1. 解析 CLI 参数（`--metrics-bind-address`、`--health-probe-bind-address`、
   `--leader-elect`）
2. 创建 controller-runtime `Manager`，配置 Scheme、Metrics、Health 和
   Leader Election
3. 将 `ClusterSecretReconciler` 注册到 Manager
4. 启动 Manager（阻塞直到 SIGINT/SIGTERM）

`version` 变量在构建时通过 `-ldflags -X` 注入——详见 Makefile 的 `build` 目标。

### internal/controller/clustersecret_controller.go

Reconciler。`Reconcile(ctx, req) → (Result, error)` 在监听的资源发生
任何事件时被调用。流程如下：

```
获取 ClusterSecret
  ├─ 未找到 → 返回（已被删除）
  ├─ DeletionTimestamp 已设置 → reconcileDelete → 移除 finalizer
  └─ 正常 → 确保 finalizer → 解析数据 → 匹配命名空间 →
              同步到匹配的 → 从不匹配的删除 → 更新状态
```

### internal/kubernetes/namespace.go

包含 `MatchNamespace(name, include, exclude)` —— 一个纯函数，应用正则
模式并返回布尔值。不依赖 Kubernetes，拥有 11 个单元测试覆盖边界情况
（空列表、非法正则、优先级规则）。

### internal/metrics/metrics.go

定义了四个 Prometheus 指标，全部注册到 controller-runtime 的全局注册表中，
因此与内置指标共享同一个 `/metrics` 端点：

| 指标 | 类型 | 标签 | 用途 |
|------|------|------|------|
| `clustersecret_reconcile_total` | Counter | `result` | Reconcile 成功/失败/删除的速率 |
| `clustersecret_reconcile_duration_seconds` | Histogram | — | Reconcile 延迟（分桶） |
| `clustersecret_synced_namespaces` | Gauge | `clustersecret` | 每个 CR 当前的同步数 |
| `clustersecret_sync_errors_total` | Counter | `operation` | 按命名空间统计的同步失败数 |

### config/rbac/

三个文件定义 Operator 的身份和权限：

- `service_account.yaml` —— Operator 的身份标识
- `role.yaml` —— ClusterRole，包含 CRD CRUD + Secrets + Namespaces +
  Leases + Events 权限
- `role_binding.yaml` —— 将 ServiceAccount 绑定到 ClusterRole

### deploy/

- `namespace.yaml` —— `clustersecret-operator` 命名空间
- `deployment.yaml` —— Deployment，包含探针、资源限制、安全上下文
  （受限的 Pod Security）和 Leader Election

---

## 数据流：创建 ClusterSecret

```
用户: kubectl apply -f clustersecret.yaml
  │
  ▼
API Server: 存储 ClusterSecret，返回 201
  │
  ▼
Informer: 收到 "added" 事件，将 Reconcile 请求入队
  │
  ▼
Reconcile:
  1. GET ClusterSecret → 确认存在
  2. DeletionTimestamp 为零 → 不是删除
  3. Finalizer 不存在 → 添加 finalizer，UPDATE（触发新一轮 Reconcile）
  4. （第二轮 Reconcile）Finalizer 已存在 → 继续
  5. resolveData → 构建键值映射
  6. listMatchingNamespaces → 列出所有命名空间，应用正则
  7. 对每个匹配的命名空间：
       CreateOrUpdate 子 Secret，添加 managed-by 和 parent 标签
  8. 删除过期的 Secret（首次 Reconcile 没有）
  9. 更新 status.syncedNamespaces
```

---

## 数据流：删除 ClusterSecret

```
用户: kubectl delete clustersecret my-csec
  │
  ▼
API Server: 设置 deletionTimestamp，不立即删除
            （finalizer 阻止删除）
  │
  ▼
Informer: 收到 "updated" 事件（deletionTimestamp 已设置）
  │
  ▼
Reconcile:
  1. GET ClusterSecret → 找到，deletionTimestamp 已设置
  2. reconcileDelete:
     a. 对 status.syncedNamespaces 中的每个命名空间：
          删除子 Secret（如果 managed-by 标签匹配）
     b. 移除 finalizer
     c. UPDATE ClusterSecret（移除 finalizer）
  │
  ▼
API Server: Finalizer 列表为空 → 删除 ClusterSecret 对象
  │
  ▼
Informer: 收到 "deleted" 事件 → Reconcile 被调用，IsNotFound → 返回
```

---

## 可观测性

### 日志

通过 zap 日志库输出结构化日志（开发模式启用控制台友好输出）。关键日志行：

```
INFO  controllers.ClusterSecret  reconciling          clustersecret=my-csec
INFO  controllers.ClusterSecret  reconciled           matched=3 removed=0
INFO  controllers.ClusterSecret  clustersecret cleaned up  namespaces=3
DEBUG controllers.ClusterSecret  finalizer added
DEBUG controllers.ClusterSecret  enqueuing clustersecrets for namespace event  count=5
```

### 指标

在 `:8080/metrics` 可用。最小化的 Prometheus 采集配置：

```yaml
scrape_configs:
  - job_name: clustersecret
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_app_kubernetes_io_name]
        action: keep
        regex: clustersecret-operator
```

### 健康探针

- `GET /healthz` → 存活探针：Manager 健康时返回 200
- `GET /readyz` → 就绪探针：Manager 可以服务请求时返回 200

---

## 高可用

Operator 通过 Kubernetes Leader Election 支持主备高可用：

- **Lease**：`clustersecret-operator/clustersecret.io`
- **时长**：15 秒
- **故障切换**：当领导者停止续约时，候选者在大约 10-15 秒内获取 Lease
- **行为**：非领导者 Pod 响应健康探针但不会启动 Controller——它们消耗极少的资源

详见 [deployment.yaml](../deploy/deployment.yaml) —— 设置 `replicas: 3` 启用高可用。
