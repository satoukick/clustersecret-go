# ClusterSecret Operator

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.26+-blue.svg)](https://golang.org)
[![Kubernetes](https://img.shields.io/badge/kubernetes-1.29+-326CE5.svg)](https://kubernetes.io)

A Kubernetes operator written in Go that synchronizes Secrets across matching
namespaces. Define a `ClusterSecret` custom resource once, and the operator
clones it into every namespace that matches your criteria — automatically
picking up new namespaces as they are created.

Inspired by [ClusterSecret](https://github.com/zakkg3/ClusterSecret), rewritten
from Python/Kopf to Go/controller-runtime as a learning exercise in building
production-grade operators from scratch.

For a detailed design comparison between the original Python version and this
Go rewrite, see [docs/operator-vs-pykopf.md](docs/operator-vs-pykopf.md).

---

## Features

- **Pattern-based namespace matching** using regex (`matchNamespace` /
  `avoidNamespaces`)
- **Automatic sync to new namespaces** via Namespace watch
- **Reference existing Secrets** with `valueFrom` instead of duplicating data
- **Configurable Secret type** (defaults to `Opaque`)
- **Status reporting** with synced namespace list and conditions
- **Finalizer-based cleanup** — zero orphaned Secrets, even after operator
  crash
- **Label protection** — refuses to overwrite or delete Secrets not managed by
  this operator
- **Prometheus metrics** — reconcile rate, duration, sync count, error count
- **Health and readiness probes** for production deployment
- **Leader election** for high availability (3+ replicas, ~15s failover)
- **Multi-arch image** (amd64 + arm64)

---

## Quick Start

### Prerequisites

- Go 1.26+
- A Kubernetes cluster (1.29+) or [kind](https://kind.sigs.k8s.io/)
- `kubectl` configured to talk to your cluster

### 1. Install the CRD

```bash
kubectl apply -f config/crd/clustersecret-crd.yaml
```

### 2. Run locally (development)

```bash
go run ./cmd/main.go
```

### 3. Create your first ClusterSecret

```yaml
apiVersion: clustersecret.io/v1
kind: ClusterSecret
metadata:
  name: shared-credentials
spec:
  matchNamespace:
    - "^team-.*"
  data:
    api-key: "secret-value"
  type: Opaque
```

Save as `my-clustersecret.yaml` and apply:

```bash
kubectl apply -f my-clustersecret.yaml
```

Every namespace matching `^team-.*` gets a `shared-credentials` Secret with
`api-key: secret-value`. New matching namespaces are picked up automatically.

---

## Deploy to a Cluster

### Using the manifests directly

```bash
# Namespace, RBAC, CRD, and Deployment
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/deployment.yaml
kubectl apply -f config/crd/
kubectl apply -f config/rbac/
```

Or use the Makefile:

```bash
make deploy
```

### Build and push your own image

```bash
# Build for your local platform
make docker-build IMG=ghcr.io/your-org/clustersecret-go:v0.1.0

# Push to registry
make docker-push IMG=ghcr.io/your-org/clustersecret-go:v0.1.0

# Multi-arch (amd64 + arm64)
make docker-buildx IMG=ghcr.io/your-org/clustersecret-go:v0.1.0
```

Then update `deploy/deployment.yaml` with your image reference and re-apply.

### Verify

```bash
# Check the operator pod
kubectl get pods -n clustersecret-operator

# Check logs
kubectl logs -n clustersecret-operator -l app.kubernetes.io/name=clustersecret-operator

# Check the leader (if multiple replicas)
kubectl get lease -n clustersecret-operator clustersecret.io \
  -o jsonpath="{.spec.holderIdentity}"
```

---

## Configuration

### ClusterSecret Spec

| Field | Type | Description |
|-------|------|-------------|
| `matchNamespace` | `[]string` | Regex patterns; namespaces matching any are included |
| `avoidNamespaces` | `[]string` | Regex patterns; takes precedence over `matchNamespace` |
| `data` | `map[string]string` | Key-value data for the Secret |
| `valueFrom` | `ValueFromSource` | Reference an existing Secret instead of `data` |
| `type` | `string` | Kubernetes Secret type, defaults to `Opaque` |

### ValueFromSource

| Field | Type | Description |
|-------|------|-------------|
| `name` | `string` | Name of the source Secret (required) |
| `namespace` | `string` | Namespace of the source Secret (required) |
| `keys` | `[]string` | Restrict to specific keys; empty means all keys |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `syncedNamespaces` | `[]string` | Namespaces the Secret has been synced to |
| `conditions` | `[]Condition` | Latest observed state (standard Kubernetes conditions) |

### Operator CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--metrics-bind-address` | `:8080` | Metrics endpoint address |
| `--health-probe-bind-address` | `:8081` | Health/ready probe endpoint |
| `--leader-elect` | `false` | Enable leader election for HA |

---

## Examples

### Copy from an existing Secret

```yaml
apiVersion: clustersecret.io/v1
kind: ClusterSecret
metadata:
  name: tls-from-source
spec:
  matchNamespace:
    - ".*"
  valueFrom:
    name: root-ca
    namespace: cert-manager
    keys: ["ca.crt", "tls.crt"]
```

### Exclude system namespaces

```yaml
apiVersion: clustersecret.io/v1
kind: ClusterSecret
metadata:
  name: app-config
spec:
  matchNamespace:
    - ".*"
  avoidNamespaces:
    - "^kube-.*"
    - "^openshift-.*"
  data:
    log-level: "info"
```

---

## High Availability

The operator supports active-passive HA via Kubernetes leader election. To
enable, set `replicas: 3` in `deploy/deployment.yaml`:

```yaml
spec:
  replicas: 3
```

- **Lease**: `clustersecret-operator/clustersecret.io`
- **Duration**: 15 seconds
- **Failover**: when the leader stops renewing, a candidate acquires the lease
  within ~10-15 seconds
- **Behavior**: non-leader pods respond to health probes but do not start
  controllers

Find the current leader:

```bash
kubectl get lease -n clustersecret-operator clustersecret.io \
  -o json | python -c "import sys,json; d=json.load(sys.stdin); print(d['spec']['holderIdentity'].split('_')[0])"
```

---

## Monitoring

### Metrics

The operator exposes Prometheus metrics at `:8080/metrics`:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `clustersecret_reconcile_total` | Counter | `result` (success/error/deleted) | Completed reconcile attempts |
| `clustersecret_reconcile_duration_seconds` | Histogram | — | Reconcile latency (bucketed) |
| `clustersecret_synced_namespaces` | Gauge | `clustersecret` | Current sync count per CR |
| `clustersecret_sync_errors_total` | Counter | `operation` (sync/cleanup) | Per-namespace sync failures |

A minimal Prometheus scrape config:

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

### Health Probes

- `GET /healthz` (liveness, initial delay 15s, period 20s)
- `GET /readyz` (readiness, initial delay 5s, period 10s)

---

## Development

### Prerequisites

- Go 1.26+
- `kubectl` with a cluster context (optional, for E2E testing)

### Commands

```bash
# Build all packages
go build ./...

# Run unit tests
go test ./...

# Run the operator locally (uses current kubeconfig)
go run ./cmd/main.go

# Regenerate DeepCopy after modifying api/v1 types
go run sigs.k8s.io/controller-tools/cmd/controller-gen@latest object paths="./api/v1/"

# Run all Makefile targets
make fmt     # go fmt ./...
make vet     # go vet ./...
make build   # compile to bin/manager
make test    # go test ./...
```

### Project Structure

```
clustersecret-go/
├── api/v1/                          # CRD type definitions
│   ├── clustersecret_types.go       # ClusterSecret, Spec, Status, ValueFrom
│   ├── groupversion_info.go         # Scheme registration
│   └── zz_generated.deepcopy.go     # Auto-generated DeepCopy methods
├── cmd/
│   └── main.go                      # Manager entry point
├── internal/
│   ├── controller/
│   │   └── clustersecret_controller.go   # Reconciler + Watch setup
│   ├── kubernetes/
│   │   ├── namespace.go                  # MatchNamespace regex engine
│   │   └── namespace_test.go            # 11 unit tests
│   └── metrics/
│       └── metrics.go                    # Prometheus collectors
├── config/
│   ├── crd/
│   │   └── clustersecret-crd.yaml        # CRD manifest
│   └── rbac/
│       ├── service_account.yaml
│       ├── role.yaml                     # ClusterRole (CRD + Secrets + Leases + Events)
│       └── role_binding.yaml
├── deploy/
│   ├── namespace.yaml                    # Operator namespace
│   └── deployment.yaml                   # Deployment (probes, security, leader-elect)
├── docs/
│   ├── architecture.md                   # Architecture & data flow
│   └── operator-vs-pykopf.md             # Design comparison with Python version
├── .github/workflows/
│   └── ci.yaml                           # lint + test + docker build
├── Dockerfile                            # Multi-stage, multi-arch, distroless
├── Makefile
├── ROADMAP.md                            # Project roadmap
├── go.mod / go.sum
└── README.md                             # This file
```

---

## Architecture

For a detailed architecture overview including component diagrams and data
flows, see [docs/architecture.md](docs/architecture.md).

In short:

1. The operator watches `ClusterSecret` (primary) and `Namespace` (secondary)
   resources via controller-runtime's informer cache
2. On any event, the reconciler reads the current state and converges:
   - Computes matching namespaces via regex
   - Creates or updates child Secrets with managed-by labels
   - Deletes Secrets in namespaces that no longer match
   - Updates status with the current synced namespace list
3. A finalizer prevents orphaned Secrets if the ClusterSecret is deleted
4. All operations are **level-triggered** — restarting the operator is always
   safe

---

## Comparison with Python/Kopf Version

See [docs/operator-vs-pykopf.md](docs/operator-vs-pykopf.md) for a detailed
comparison. Key differences:

| Aspect | Python/Kopf | Go/controller-runtime |
|--------|-------------|----------------------|
| State | In-memory cache (lost on restart) | Informer cache (survives restart) |
| Cleanup | Best-effort (orphans possible) | Finalizer-guaranteed |
| Schema | `x-kubernetes-preserve-unknown-fields` | Explicit typed schema |
| Production | No HA, no metrics | HA, metrics, probes, security |

---

## License

This project is licensed under the [MIT License](./LICENSE).
