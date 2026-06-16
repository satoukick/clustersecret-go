# ClusterSecret Operator

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.26+-blue.svg)](https://golang.org)
[![Kubernetes](https://img.shields.io/badge/kubernetes-1.29+-326CE5.svg)](https://kubernetes.io)

A Kubernetes Operator Written in Go that synchronizes Secrets across matching namespaces. Define a `ClusterSecret` custom resource once, and the operator clones it into every namespace that matches your criteria — automatically picking up new namespaces as they're created.

Inspired by [ClusterSecret](https://github.com/zakkg3/ClusterSecret)

For Personal Study Purpose

## Features

- **Pattern-based namespace matching** using regex (`matchNamespace` / `avoidNamespaces`)
- **Automatic sync to new namespaces** via Namespace watch
- **Reference existing Secrets** with `valueFrom` instead of duplicating data
- **Configurable Secret type** (defaults to `Opaque`)
- **Status reporting** with synced namespace list
- **Finalizer-based cleanup** to prevent orphaned Secrets
- **Prometheus metrics** for sync operations
- **Health and readiness probes** for production deployment
- **Leader election** for high availability

## Quick Start

### Prerequisites

- Go 1.26+
- A Kubernetes cluster (1.29+) or `kind`
- `kubectl` configured to talk to your cluster

### Install CRD

```bash
kubectl apply -f config/crd/clustersecret-crd.yaml
```

### Run locally

```bash
go run ./cmd/main.go
```

### Create your first ClusterSecret

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

Apply it:

```bash
kubectl apply -f my-clustersecret.yaml
```

Every namespace matching `^team-.*` will get a `shared-credentials` Secret with `api-key: secret-value`. New matching namespaces are picked up automatically.

## API Reference

### Spec

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
| `keys` | `[]string` | Restrict to specific keys; empty means all |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `syncedNamespaces` | `[]string` | Namespaces the Secret has been synced to |
| `conditions` | `[]Condition` | Latest observed state |

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

## Architecture

ClusterSecret uses [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) directly, without kubebuilder scaffolding. The Manager watches two resources:

1. **ClusterSecret** — primary resource; Reconcile syncs the Secret to all matching namespaces
2. **Namespace** — secondary; Reconcile is triggered when a new namespace might match existing ClusterSecrets

Reconciliation is idempotent: it always converges the cluster state to the desired state defined in the spec.

## Development

```bash
# Build
go build ./...

# Test
go test ./...

# Regenerate DeepCopy after modifying types
go run sigs.k8s.io/controller-tools/cmd/controller-gen@latest object paths="./api/v1/"
```

## License

This project is licensed under the [MIT License](./LICENSE).
