# ClusterSecret Operator (Go) — 项目路线图

用 Go + controller-runtime 重写 Python 版 ClusterSecret Operator，作为 Kubernetes Operator 与 CRD 开发的实践项目。

---

## 项目架构

### 核心设计理念

- **不依赖 kubebuilder 脚手架**：从零基于 `sigs.k8s.io/controller-runtime` 手动组装 Manager / Reconciler / Watch
- **controller-gen 只用来生成 DeepCopy**：避免手写易错的对象拷贝方法
- **API 设计改进**：把原 Python 版的 `valueFrom` 从 `data` 字段中提升为显式字段，CRD schema 自描述、可校验
- **生产级特性**：Metrics、Health/Ready 探针、Leader Election、Finalizer 级联删除

### 数据流

```
┌──────────────────────────────────────────────────────────────┐
│                    Kubernetes API Server                      │
│                                                              │
│  ┌──────────────────┐     ┌──────────────────┐               │
│  │ ClusterSecret CR │     │  Namespace  CR   │               │
│  └────────┬─────────┘     └────────┬─────────┘               │
│           │ watch                 │ watch                    │
└───────────┼─────────────────────────┼────────────────────────┘
            │                         │
            ▼                         ▼
    ┌───────────────────────────────────────────┐
    │  controller-runtime Manager (in-cluster)  │
    │                                           │
    │  ┌─────────────────────────────────────┐  │
    │  │  ClusterSecretReconciler            │  │
    │  │  (shared indexer / local cache)     │  │
    │  └──────────────┬──────────────────────┘  │
    │                 │                         │
    │   Reconcile:    │ 1. Fetch ClusterSecret  │
    │                 │ 2. Resolve data          │
    │                 │ 3. Compute NS matches    │
    │                 │ 4. Sync / delete Secrets │
    │                 │ 5. Update status         │
    └─────────────────┼─────────────────────────┘
                      │
                      ▼
            ┌──────────────────┐
            │  Secret in NS-1  │
            │  Secret in NS-2  │
            │  ...             │
            └──────────────────┘
```

### 关键技术决策

| 决策 | 原因 |
|------|------|
| 不用 kubebuilder/operator-sdk | Windows 工具链体验差；手写更深入理解 |
| 用 controller-gen 生成 DeepCopy | K8s 类型系统硬性要求；手写易错且需同步 |
| `valueFrom` 独立字段 | 改进原版 `x-kubernetes-preserve-unknown-fields` 反模式 |
| Finalizer 清理机制 | 防止 ClusterSecret 删除时残留 Secret |
| OwnerReference | 透明的级联删除 + 垃圾回收 |
| 预编译正则 | 每次 Reconcile 都重编译会浪费 CPU |
| 不引入 informer 优化（MVP） | MVP 阶段先用 List，规模化后再优化 |

---

## 目录结构（目标）

```
clustersecret-go/
├── api/
│   └── v1/
│       ├── clustersecret_types.go        # CRD 类型定义（手写）
│       ├── groupversion_info.go          # Scheme 注册（手写）
│       └── zz_generated.deepcopy.go      # DeepCopy（controller-gen 生成）
├── cmd/
│   └── main.go                           # Manager 入口、启动配置
├── internal/
│   ├── controller/
│   │   └── clustersecret_controller.go   # Reconciler + Watch 注册
│   └── kubernetes/
│       └── namespace.go                  # 命名空间匹配、列表查询
├── config/
│   ├── crd/
│   │   └── clustersecret-crd.yaml        # CRD 清单
│   └── rbac/
│       └── role.yaml                     # ClusterRole / ClusterRoleBinding
├── deploy/
│   └── deployment.yaml                   # Operator 部署清单
├── Dockerfile                            # 多架构镜像构建
├── Makefile                              # 常用命令（generate / build / test）
├── ROADMAP.md                            # 本文件
├── go.mod
├── go.sum
└── README.md
```

---

## 已完成 ✅

### 阶段 1：项目初始化与类型系统

- [x] 初始化 Go module `github.com/satoukick/clustersecret-go`
- [x] 拉取核心依赖（controller-runtime v0.24.1、client-go v0.36.2、api/apimachinery v0.36.2、go-logr）
- [x] 创建标准目录结构（`api/v1`、`cmd`、`internal/controller`、`internal/kubernetes`、`config/crd`）
- [x] 定义 CRD types（`ClusterSecret`、`ClusterSecretSpec`、`ClusterSecretStatus`、`ValueFromSource`、`ClusterSecretList`）
- [x] 加上 `+kubebuilder:object:generate=true` 标记，controller-gen 生成完整 DeepCopy（含嵌套类型）
- [x] 手写 `groupversion_info.go` 完成 Scheme 注册
- [x] 写 CRD YAML（schema、printer columns、status subresource）
- [x] 验证：`go build ./...` 通过

### 阶段 2：Manager 入口

- [x] 编写 `cmd/main.go`：flags（metrics/leader-elect/probe 地址）、Manager 初始化、Scheme 注册
- [x] 接入 Healthz / Readyz 探针
- [x] 在 Manager 中注册 `ClusterSecretReconciler`

### 阶段 3：Reconciler 骨架

- [x] 定义 `ClusterSecretReconciler` 结构（Client、Scheme、Log）
- [x] 实现 `SetupWithManager`：使用 `For()` 注册主资源，使用 `Watches()` 注册 Namespace 联动
- [x] 加上 `+kubebuilder:rbac` 注释标记所需的权限

### 阶段 4：核心 Reconciler 逻辑

- [x] `MatchNamespace(name, include, exclude)` — 正则匹配函数（提取到 `internal/kubernetes/namespace.go`）
- [x] 单元测试 11 个 case：边界、优先级、非法正则
- [x] `listMatchingNamespaces` — 列出所有 Namespace 并过滤匹配项，跳过 Terminating 状态
- [x] `resolveData` — 处理 `Data` 与 `ValueFrom` 两种数据源，互斥校验
- [x] `syncSecretToNamespace` — 用 `controllerutil.CreateOrUpdate` 保证幂等，加 managed-by label 防误覆盖
- [x] `deleteSecretFromNamespace` — 只删带有 managed-by label 的 Secret
- [x] Reconcile 主循环：fetch → 删除分支 → finalizer → resolve → list → sync → cleanup → status
- [x] Finalizer 双阶段（添加/删除）已就位
- [x] `findClusterSecretsForNamespace` — Namespace 事件入队所有 ClusterSecret

---

## 待完成 🚧

### 阶段 5：删除与 Finalizer 的端到端验证

- [x] Finalizer 添加/移除逻辑已合并进阶段 4
- [ ] E2E 验证：用 kind 集群验证 ClusterSecret 删除时所有 child Secret 都被清掉

### 阶段 6：可观测性

- [ ] Prometheus Metrics：
  - `clustersecret_reconcile_total`（counter，按结果 label）
  - `clustersecret_reconcile_duration_seconds`（histogram）
  - `clustersecret_synced_namespaces`（gauge，按 csec 标签）
  - `clustersecret_sync_errors_total`（counter，按错误类型 label）
- [ ] 结构化日志：每个 Reconcile 包含 csec 名称、namespace 数量、错误等
- [ ] 调试日志：namespace 匹配命中/未命中的细节

### 阶段 7：构建与部署

- [ ] `Makefile`：generate / build / test / docker-build / deploy 目标
- [ ] `Dockerfile`：多架构（amd64 / arm64）构建
- [ ] `config/rbac/role.yaml`：手写 ClusterRole（因为不用 kubebuilder 生成）
- [ ] `deploy/deployment.yaml`：Operator Deployment
- [ ] `.github/workflows/ci.yaml`：lint + test + build

### 阶段 8：测试

- [ ] 单元测试：`matchNamespace` 边界（空列表、不合法正则、优先级）
- [ ] 单元测试：`buildDesiredSecret` 字段映射
- [ ] E2E 测试：用 `envtest` 或 `kind` 跑端到端流程
- [ ] 覆盖场景：
  - 创建 ClusterSecret → 验证 Secret 出现在匹配 NS
  - 创建匹配的新 NS → 验证自动同步
  - 改 data 字段 → 验证 Secret 更新
  - 改 matchNamespace → 验证多/删 Secret
  - 删除 ClusterSecret → 验证 child Secret 清理

### 阶段 9：文档

- [ ] `README.md`：项目介绍、快速开始、API 文档
- [ ] `docs/architecture.md`：架构图、组件说明
- [ ] `docs/operator-vs-pykopf.md`：与原 Python/Kopf 版本的设计对比

---

## 常用命令

```bash
# 进入项目目录
cd g:/GitHub/clustersecret-go

# 编译
go build ./...

# 重新生成 DeepCopy（修改 types 后）
go run sigs.k8s.io/controller-tools/cmd/controller-gen@latest object paths="./api/v1/"

# 运行单元测试
go test ./...

# 跑 Operator（需要 kubeconfig）
go run ./cmd/main.go
```
