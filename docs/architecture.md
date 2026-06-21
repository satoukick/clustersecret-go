# ClusterSecret Operator — Architecture

## Overview

ClusterSecret is a Kubernetes operator that synchronizes Secret data across
namespaces based on regex patterns. It is implemented with
[controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)
— no kubebuilder, no operator-sdk — as a learning exercise in building
production-grade operators from scratch.

## Architecture Diagram

```
┌──────────────────────────────────────────────────────────────────┐
│                      Kubernetes API Server                        │
│                                                                  │
│  ┌──────────────────┐     ┌──────────────────┐                  │
│  │ ClusterSecret CR │     │  Namespace  CR   │                  │
│  │ (cluster-scoped) │     │ (cluster-scoped) │                  │
│  └────────┬─────────┘     └────────┬─────────┘                  │
│           │ watch                  │ watch                      │
└───────────┼─────────────────────────┼───────────────────────────┘
            │                         │
            ▼                         ▼
┌───────────────────────────────────────────────────────────┐
│                    controller-runtime Manager               │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐  │
│  │              Informer / Cache (shared)               │  │
│  │  Stores typed objects, reduces API server load,      │  │
│  │  provides thread-safe access via Reader interface.   │  │
│  └──────────────────────┬──────────────────────────────┘  │
│                         │                                  │
│                         ▼                                  │
│  ┌─────────────────────────────────────────────────────┐  │
│  │              Workqueue (rate-limited)                │  │
│  │  Deduplicates & retries reconcile requests.         │  │
│  │  Failed keys are re-enqueued with exponential       │  │
│  │  backoff (base 5ms, max ~16 min).                   │  │
│  └──────────────────────┬──────────────────────────────┘  │
│                         │                                  │
│                         ▼                                  │
│  ┌─────────────────────────────────────────────────────┐  │
│  │           ClusterSecretReconciler                    │  │
│  │                                                      │  │
│  │  1. Get ClusterSecret (from cache)                   │  │
│  │  2. Check DeletionTimestamp → cleanup branch         │  │
│  │  3. Ensure finalizer present                         │  │
│  │  4. Resolve data (literal or ValueFrom)              │  │
│  │  5. List & match namespaces (via informer cache)     │  │
│  │  6. CreateOrUpdate child Secrets                     │  │
│  │  7. Delete stale Secrets (no longer matched)         │  │
│  │  8. Update status.syncedNamespaces                   │  │
│  └──────────────────────┬──────────────────────────────┘  │
│                         │                                  │
│  ┌─────────────────────────────────────────────────────┐  │
│  │          Metrics (:8080)                             │  │
│  │  reconcile_total, reconcile_duration_seconds,        │  │
│  │  synced_namespaces (gauge), sync_errors_total        │  │
│  └─────────────────────────────────────────────────────┘  │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐  │
│  │          Health (:8081)                              │  │
│  │  /healthz → liveness probe                          │  │
│  │  /readyz → readiness probe                          │  │
│  └─────────────────────────────────────────────────────┘  │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐  │
│  │          Leader Election (coordination.k8s.io)       │  │
│  │  Leases: clustersecret-operator/clustersecret.io    │  │
│  │  One replica active, N-1 candidates idling.         │  │
│  │  Failover within 15s on leader loss.                │  │
│  └─────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────┘
            │
            ▼
┌───────────────────────────────────────────────────────────┐
│               Child Secrets (per matching namespace)       │
│                                                             │
│  Namespace: team-alpha     Namespace: team-beta             │
│  ┌──────────────────┐     ┌──────────────────┐             │
│  │ shared-credentials│     │ shared-credentials│            │
│  │ (Opaque)          │     │ (Opaque)          │            │
│  │ labels:           │     │ labels:           │            │
│  │   managed-by: cs  │     │   managed-by: cs  │            │
│  │   parent: shared  │     │   parent: shared  │            │
│  └──────────────────┘     └──────────────────┘             │
└───────────────────────────────────────────────────────────┘
```

## Key Design Decisions

### Why Not kubebuilder / operator-sdk?

| Concern | Decision |
|---------|----------|
| Generated code is hard to debug | Manual assembly forces understanding every component |
| Windows toolchain compatibility | `controller-gen` is the only external tool; everything else is plain Go |
| Learning value | Scaffolding hides the wiring — hand-wiring `Manager` → `Controller` → `Reconciler` teaches the architecture |

The only generated code is `zz_generated.deepcopy.go` from controller-gen,
because DeepCopy methods are mechanical and error-prone to maintain by hand.

### Level-Triggered Reconciliation

controller-runtime is **level-triggered**, not edge-triggered. Each `Reconcile`
call reads the **current state** from the informer cache and converges toward
the desired state. This means:

- Restarting the operator automatically reconciles all resources (no state loss)
- The order of events is irrelevant — the Reconcile loop always converges
- Transient failures are handled by re-queue with exponential backoff

Compare with the Python/Kopf version which used an in-memory cache (UID →
body). A restart there lost all state and required re-syncing every
ClusterSecret from scratch — and the cache could drift from reality if the
operator missed events.

### Finalizer + Label Guard

The operator uses two complementary mechanisms for safe cleanup:

1. **Finalizer** (`clustersecret.io/finalizer`): prevents the API server from
   deleting the ClusterSecret CR until the operator has cleaned up all child
   Secrets. If the operator is down when a delete is requested, the
   ClusterSecret stays in `Terminating` state and is reconciled when the
   operator comes back up.

2. **`managed-by` label**: every child Secret gets
   `clustersecret.io/managed-by: clustersecret-operator`. The operator refuses
   to modify or delete a Secret without this label, preventing accidental
   overwrites of user-created Secrets.

### Watches: Primary + Secondary

The controller watches two resource types:

| Resource | Role | Trigger |
|----------|------|---------|
| `ClusterSecret` | Primary | Direct reconcile on create/update/delete |
| `Namespace` | Secondary | Re-enqueues **all** ClusterSecrets for re-evaluation |

The secondary watch does **not** pre-filter by regex — it enqueues every
ClusterSecret. This is intentional: ClusterSecret count is typically orders of
magnitude smaller than namespace event volume, and the Reconcile loop already
computes the authoritative match set. Pre-filtering would add complexity and a
source of drift for negligible performance gain.

## Component Reference

### cmd/main.go

Entry point that:

1. Parses CLI flags (`--metrics-bind-address`, `--health-probe-bind-address`,
   `--leader-elect`)
2. Creates a controller-runtime `Manager` with Scheme, Metrics, Health, and
   Leader Election configured
3. Registers `ClusterSecretReconciler` with the Manager
4. Starts the Manager (blocks until SIGINT/SIGTERM)

The `version` variable is injected at build time via `-ldflags -X` — see the
Makefile's `build` target.

### internal/controller/clustersecret_controller.go

The reconciler. `Reconcile(ctx, req) → (Result, error)` is called for every
event on watched resources. The flow:

```
Get ClusterSecret
  ├─ Not Found → return (already deleted)
  ├─ DeletionTimestamp set → reconcileDelete → remove finalizer
  └─ Normal → ensure finalizer → resolve data → match namespaces →
              sync to matched → delete from unmatched → update status
```

### internal/kubernetes/namespace.go

Contains `MatchNamespace(name, include, exclude)` — a pure function that
applies regex patterns and returns a boolean. No Kubernetes dependencies; it
has 11 unit tests covering edge cases (empty lists, invalid regexes, priority
rules).

### internal/metrics/metrics.go

Defines four Prometheus collectors, all registered against
controller-runtime's global registry so they're served alongside built-in
metrics on the same `/metrics` endpoint:

| Metric | Type | Labels | Purpose |
|--------|------|--------|---------|
| `clustersecret_reconcile_total` | Counter | `result` | Reconcile success/error/deleted rate |
| `clustersecret_reconcile_duration_seconds` | Histogram | — | Reconcile latency (bucketed) |
| `clustersecret_synced_namespaces` | Gauge | `clustersecret` | Current sync count per CR |
| `clustersecret_sync_errors_total` | Counter | `operation` | Per-namespace sync failures |

### config/rbac/

Three files that define the operator's identity and permissions:

- `service_account.yaml` — the operator's identity
- `role.yaml` — ClusterRole with CRD CRUD + Secrets + Namespaces + Leases +
  Events permissions
- `role_binding.yaml` — binds the SA to the ClusterRole

### deploy/

- `namespace.yaml` — `clustersecret-operator` namespace
- `deployment.yaml` — Deployment with probes, resource limits, security
  context (restricted Pod Security), and leader election

## Data Flow: Create a ClusterSecret

```
User: kubectl apply -f clustersecret.yaml
  │
  ▼
API Server: stores ClusterSecret, returns 201
  │
  ▼
Informer: receives "added" event, enqueues reconcile request
  │
  ▼
Reconcile:
  1. GET ClusterSecret → verify it exists
  2. DeletionTimestamp is zero → not a delete
  3. Finalizer absent → add it, UPDATE (triggers another reconcile)
  4. (second reconcile) Finalizer present → proceed
  5. resolveData → build the key-value map
  6. listMatchingNamespaces → List all Namespaces, apply regex
  7. For each matched NS:
       CreateOrUpdate child Secret with managed-by + parent labels
  8. Delete stale Secrets (none on first reconcile)
  9. Update status.syncedNamespaces
```

## Data Flow: Delete a ClusterSecret

```
User: kubectl delete clustersecret my-csec
  │
  ▼
API Server: sets deletionTimestamp, does NOT delete immediately
            (finalizer blocks it)
  │
  ▼
Informer: receives "updated" event (deletionTimestamp set)
  │
  ▼
Reconcile:
  1. GET ClusterSecret → found, deletionTimestamp set
  2. reconcileDelete:
     a. For each NS in status.syncedNamespaces:
          delete child Secret (if managed-by label matches)
     b. Remove finalizer
     c. UPDATE ClusterSecret (removes finalizer)
  │
  ▼
API Server: finalizer list now empty → deletes the ClusterSecret object
  │
  ▼
Informer: receives "deleted" event → Reconcile called, IsNotFound → return
```

## Observability

### Logging

Structured JSON via zap logger (development mode enables console-friendly
output). Key log lines:

```
INFO  controllers.ClusterSecret  reconciling          clustersecret=my-csec
INFO  controllers.ClusterSecret  reconciled           matched=3 removed=0
INFO  controllers.ClusterSecret  clustersecret cleaned up  namespaces=3
DEBUG controllers.ClusterSecret  finalizer added
DEBUG controllers.ClusterSecret  enqueuing clustersecrets for namespace event  count=5
```

### Metrics

Available at `:8080/metrics`. A minimal Prometheus scrape config:

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

- `GET /healthz` → liveness: returns 200 if the Manager is healthy
- `GET /readyz` → readiness: returns 200 if the Manager can serve requests

## High Availability

The operator supports active-passive HA via Kubernetes leader election:

- **Lease**: `clustersecret-operator/clustersecret.io`
- **Duration**: 15 seconds
- **Failover**: when the leader stops renewing, a candidate acquires the lease
  within ~10-15 seconds
- **Behavior**: non-leader pods respond to health probes but do not start
  controllers — they consume minimal resources

See [deployment.yaml](../deploy/deployment.yaml) — set `replicas: 3` for HA.
