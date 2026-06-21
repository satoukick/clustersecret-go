// Package metrics defines the Prometheus collectors exposed by the
// ClusterSecret operator.
//
// All collectors are registered against controller-runtime's global
// metrics.Registry, which already serves the /metrics endpoint on the
// manager's metrics server (default :8080). That means a single scrape
// picks up both controller-runtime's own instrumentation (workqueue
// depth, reconcile rate, cache events) and the business metrics defined
// here — no extra HTTP server is needed.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	// namespace prefixes every metric so it groups cleanly in dashboards
	// and doesn't collide with other operators in a shared cluster.
	namespace = "clustersecret"
)

var (
	// ReconcileTotal counts completed reconcile attempts, labelled by
	// outcome. Use this to compute success rate and to spot error spikes.
	ReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "reconcile_total",
			Help:      "Total number of reconcile attempts, labelled by result.",
		},
		[]string{"result"},
	)

	// ReconcileDurationSeconds records how long a single reconcile took.
	// Buckets are tuned for controller work (sub-second to a few seconds);
	// long reconciles usually mean a stuck List/Watch or a slow API server.
	ReconcileDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "reconcile_duration_seconds",
			Help:      "Latency of a single reconcile loop.",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
	)

	// SyncedNamespaces is the current number of namespaces a given
	// ClusterSecret has been synced to. A gauge (not a counter) because it
	// goes up and down as match patterns change — this is the same value
	// stored in status.syncedNamespaces, surfaced for alerting.
	SyncedNamespaces = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "synced_namespaces",
			Help:      "Current number of namespaces a ClusterSecret is synced to.",
		},
		[]string{"clustersecret"},
	)

	// SyncErrorsTotal counts per-namespace sync failures, labelled by the
	// operation that failed. Use this to alert on partial syncs (a
	// ClusterSecret that syncs to 9/10 namespaces is silently broken
	// without this).
	SyncErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "sync_errors_total",
			Help:      "Total per-namespace sync errors, labelled by operation.",
		},
		[]string{"operation"},
	)
)

func init() {
	// Register against the controller-runtime global registry so our
	// collectors are served alongside the built-in ones on /metrics.
	metrics.Registry.MustRegister(
		ReconcileTotal,
		ReconcileDurationSeconds,
		SyncedNamespaces,
		SyncErrorsTotal,
	)
}
