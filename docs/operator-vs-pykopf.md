# ClusterSecret: Python/Kopf vs Go/controller-runtime

This document compares the original Python/Kopf implementation of
ClusterSecret with the Go/controller-runtime rewrite. It is written for
readers familiar with Kubernetes operator patterns who want to understand the
design trade-offs between the two approaches.

---

## Overview

| Aspect | Python / Kopf | Go / controller-runtime |
|--------|---------------|------------------------|
| Language | Python 3 | Go 1.26+ |
| Framework | [Kopf](https://kopf.readthedocs.io/) (Kubernetes Operator Framework) | [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) |
| API style | Decorator-based (declarative) | Interface-based (`Reconcile(ctx, req)`) |
| State management | In-memory dictionary (UID → body) | Informer cache (API server backed) |
| CRD schema | Preserve-unknown-fields | Explicit schema with validation |
| Build tool | setup.cfg / pip | go build / Makefile |
| Deployment | Helm chart + yaml | kubectl apply (static manifests) |
| Leader election | Not supported (single replica only) | Built-in via Leases |

---

## 1. Framework Philosophy

### Kopf (Python)

Kopf uses Python decorators to register event handlers. Each handler is a
plain function that receives the event payload as a dictionary:

```python
@kopf.on.create('clustersecret.io', 'v1', 'clustersecrets')
async def create_fn(logger, uid, name, body, **_):
    matchedns = get_ns_list(logger, body, v1)
    for ns in matchedns:
        sync_secret(logger, ns, body, v1)
    return {'syncedns': matchedns}
```

**Pros:**
- Low boilerplate — a working operator in ~50 lines
- Familiar Python ecosystem (pydantic, pytest)
- Async by default (asyncio event loop)

**Cons:**
- Everything is `Dict[str, Any]` — no type safety for CRD fields
- Event-driven (edge-triggered): if the operator misses an event, state is lost
- Decorators hide ordering and lifecycle — hard to reason about error paths

### controller-runtime (Go)

controller-runtime uses a single `Reconcile` interface. Every event on a
watched resource results in a `reconcile.Request` (just a name/namespace
tuple). The reconciler reads the current state and converges:

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

**Pros:**
- Type-safe CRD structs (generated from Go types)
- Level-triggered: restart is safe, order of events irrelevant
- Explicit control over Manager lifecycle, caches, and workqueues

**Cons:**
- More boilerplate (scheme registration, controller setup, DeepCopy)
- Go's type system is strict — every field change requires DeepCopy regeneration
- Harder to prototype quickly

---

## 2. State Management

This is the most important architectural difference.

### Python: In-Memory Cache

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

The Python version keeps a **write-through in-memory cache** keyed by UID.
This cache is:

1. **Volatile**: restart loses everything → must re-list on startup
2. **Manual**: every handler must explicitly read/write the cache
3. **Edge-triggered**: the cache only reflects events the operator has seen

If a `ClusterSecret` is created while the operator is down, the startup
re-list catches it. But if the cache drifts from reality (e.g., a missed
update event), the operator silently operates on stale data.

### Go: Informer Cache

```go
// controller-runtime's shared informer cache — no explicit cache code needed
mgr, _ := manager.New(config, manager.Options{
    Scheme: scheme,
    // ...
})
// r.Get() reads from the cache, not the API server
```

controller-runtime maintains a **shared informer cache** that:

1. **Persists across reconciles**: lives in the Manager, shared by all controllers
2. **Self-healing**: watch connections are re-established on errors; missed
   events trigger a full re-list
3. **Level-triggered**: `Get()` always returns the latest observed state, not
   the event that triggered the reconcile

The Go version has zero lines of cache management code — the framework
handles it. The reconciler simply reads whatever the cache has and converges.

---

## 3. CRD Schema Design

### Python: Preserve-Unknown-Fields

```yaml
# config/crd/clustersecret-crd.yaml (Python version)
spec:
  x-kubernetes-preserve-unknown-fields: true
```

`valueFrom` lives inside `data`:

```yaml
spec:
  data:
    valueFrom:
      secretKeyRef:
        name: root-ca
        namespace: cert-manager
```

This means:
- The API server accepts **any** field in `spec` — typos are silently ignored
- The operator must manually validate the structure
- `data` and `valueFrom` coexist in the same map, making validation awkward
- No printer columns, no status subresource

### Go: Explicit Schema

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

`valueFrom` is promoted to a top-level field, separate from `data`:

```yaml
spec:
  valueFrom:
    name: root-ca
    namespace: cert-manager
    keys: ["ca.crt"]
```

This means:
- The API server rejects invalid specs at admission time
- `data` and `valueFrom` are **mutually exclusive by type** — the operator
  checks this explicitly and returns an error
- Printer columns and status subresource are configured declaratively
- `kubectl get csec` shows useful output without extra flags

---

## 4. Cleanup Mechanism

### Python: No Finalizer

```python
@kopf.on.delete('clustersecret.io', 'v1', 'clustersecrets')
def on_delete(body, uid, name, logger, **_):
    syncedns = body.get('status', {}).get('create_fn', {}).get('syncedns', [])
    for ns in syncedns:
        delete_secret(logger, ns, name, v1)
    csecs_cache.remove_cluster_secret(uid)
```

Kopf handles finalizers transparently, but the Python version does **not**
explicitly use a finalizer. If the operator is down when a `ClusterSecret` is
deleted, the API server deletes the object immediately, and the child Secrets
become orphaned. They survive until manually cleaned up.

### Go: Explicit Finalizer

```go
const clusterSecretFinalizer = "clustersecret.io/finalizer"

// Ensure finalizer present before creating children
if !controllerutil.ContainsFinalizer(&csec, clusterSecretFinalizer) {
    controllerutil.AddFinalizer(&csec, clusterSecretFinalizer)
    r.Update(ctx, &csec)
    return ctrl.Result{}, nil
}
```

The Go version adds a finalizer **before** creating any child Secrets. This
guarantees:

- The API server cannot delete the `ClusterSecret` until the finalizer is removed
- If the operator is down, the `ClusterSecret` stays in `Terminating` state
- On restart, the operator reconciles the `Terminating` object, cleans up
  children, and removes the finalizer → deletion completes
- **Zero orphaned Secrets** under any failure scenario

---

## 5. Label Protection

### Python: Annotation-Based

```python
CREATE_BY_ANNOTATION = 'clustersecret.io/created-by'
CLUSTER_SECRET_LABEL = "clustersecret.io"
BLOCKED_ANNOTATIONS = ["kopf.zalando.org", "kubectl.kubernetes.io"]
BLOCKED_LABELS = ["app.kubernetes.io"]
```

The Python version uses an annotation `clustersecret.io/created-by` to mark
managed Secrets, and checks it before overwriting. However:

- The check only runs on **existing** Secrets — it won't prevent initial
  creation over a user-created Secret of the same name
- Annotations can be removed by users, breaking the protection
- The `REPLACE_EXISTING` env var can override the guard entirely

### Go: Label-Based

```go
const managedByLabel  = "clustersecret.io/managed-by"
const managedByValue  = "clustersecret-operator"
const parentNameLabel = "clustersecret.io/parent"
```

The Go version uses a label (`clustersecret.io/managed-by`) with a specific
value (`clustersecret-operator`). The guard is checked in both
`syncSecretToNamespace` and `deleteSecretFromNamespace`:

```go
if existing, ok := secret.Labels[managedByLabel]; ok && existing != managedByValue {
    return fmt.Errorf("refusing to overwrite Secret %s/%s not managed by clustersecret-operator", ns, secret.Name)
}
```

Key differences:
- **Both** create and delete paths are protected
- Labels are part of the object metadata that `CreateOrUpdate` merges — users
  can't accidentally strip them without triggering a re-sync
- The `parentNameLabel` enables easy discovery: `kubectl get secret --all-namespaces -l clustersecret.io/parent=<name>`

---

## 6. Event Handling

### Python: Edge-Triggered per Field

```python
@kopf.on.field('clustersecret.io', 'v1', 'clustersecrets', field='data')
def on_field_data(old, new, body, name, uid, logger, reason, **_):
    if reason == "create":
        return  # Skip on create — create_fn handles it
    # Re-sync to all existing namespaces with new data
    for ns in syncedns:
        sync_secret(logger, ns, body, v1)
```

Kopf fires separate handlers for each field change. This is efficient (only
the relevant handler runs) but fragile:

- The `data` handler and the `matchNamespace` handler have similar but
  different logic — bugs can diverge
- Both handlers must independently update the in-memory cache
- The `namespace_watcher` only handles namespace **creation**, not deletion
  (a deleted namespace would leave stale entries in `syncedns`)

### Go: Unified Reconcile Loop

```go
func (r *ClusterSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // 1. Fetch → 2. Delete? → 3. Finalizer → 4. Resolve → 5. Match → 6. Sync → 7. Cleanup → 8. Status
}
```

Every event — whether it's a data change, a pattern change, or a namespace
event — goes through the **same** Reconcile loop. The loop reads the current
state and converges. This means:

- One code path, one set of bugs
- No cache drift: the loop always works from the informer cache
- No special cases: creating a `ClusterSecret`, changing `data`, and changing
  `matchNamespace` all hit the same `sync` → `cleanup` → `status` flow
- Namespace **deletion** is handled implicitly: `listMatchingNamespaces` skips
  `Terminating` namespaces, so the cleanup branch removes their Secrets

---

## 7. Namespace Watch

### Python

```python
@kopf.on.create('', 'v1', 'namespaces')
async def namespace_watcher(logger, meta, **_):
    # Only fires on CREATE — not on update or delete
    for cluster_secret in csecs_cache.all_cluster_secret():
        # Re-compute match and sync if the new namespace matches
```

Only watches namespace **creation**. A namespace deletion does not trigger
cleanup — the operator relies on the next data change to notice the missing
namespace.

### Go

```go
Watches(
    &corev1.Namespace{},
    handler.EnqueueRequestsFromMapFunc(r.findClusterSecretsForNamespace),
)
```

Watches all namespace events (create, update, delete). Every event enqueues
**all** ClusterSecrets for re-reconciliation. The Reconcile loop then:

- Re-computes `listMatchingNamespaces` (which skips `Terminating` namespaces)
- Cleans up Secrets in namespaces that no longer match or have been deleted

---

## 8. Production Readiness

| Feature | Python | Go |
|---------|--------|-----|
| Health probes | ❌ Not exposed | ✅ /healthz + /readyz |
| Prometheus metrics | ❌ Not instrumented | ✅ 4 custom metrics + runtime defaults |
| Leader election | ❌ Not supported | ✅ Leases with 15s duration |
| Resource limits | ❌ Not in manifests | ✅ Requests + Limits in deployment.yaml |
| Pod Security | ❌ Not configured | ✅ runAsNonRoot, readOnlyRootFilesystem, seccomp |
| Multi-arch image | ✅ (amd64, arm64, s390x) | ✅ (amd64 + arm64 via buildx) |
| Debug logging | ✅ Verbose | ✅ Structured zap (development mode) |
| Graceful shutdown | ❌ Hard to control | ✅ Signal handler via Manager |

---

## Summary

The Python/Kopf version is excellent for **rapid prototyping** — you can go
from idea to working operator in an afternoon. It is what the original
ClusterSecret author chose, and it served well for years.

The Go/controller-runtime version trades development speed for **operational
safety**:

| Concern | Python/Kopf | Go/controller-runtime |
|---------|-------------|----------------------|
| State safety | In-memory cache, lost on restart | Informer cache, survives restart |
| Cleanup guarantee | Best-effort (orphans possible) | Finalizer-guaranteed (no orphans) |
| Schema validation | Runtime (operator must check) | Admission-time (API server rejects) |
| Code complexity | Low (~250 lines) | Higher (~700 lines) |
| Learning curve | Shallow (Python + decorators) | Steep (Go + interfaces + channels) |
| Production readiness | Low (no HA, no metrics) | High (HA, metrics, probes, security) |

**When to use which:**

- **Python/Kopf**: Prototyping, proof-of-concept, internal tools, teams
  already invested in Python
- **Go/controller-runtime**: Production operators, multi-replica deployments,
  security-sensitive environments, teams that value type safety
