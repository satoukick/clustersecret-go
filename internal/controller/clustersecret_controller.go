package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"github.com/go-logr/logr"

	clustersecretv1 "github.com/satoukick/clustersecret-go/api/v1"
)

const (
	clusterSecretFinalizer = "clustersecret.io/finalizer"
	managedByLabel         = "clustersecret.io/managed-by"
	managedByValue         = "clustersecret-operator"
)

// ClusterSecretReconciler reconciles a ClusterSecret object.
type ClusterSecretReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=clustersecret.io,resources=clustersecrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clustersecret.io,resources=clustersecrets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clustersecret.io,resources=clustersecrets/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// Reconcile is the main reconciliation loop.
func (r *ClusterSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("clustersecret", req.Name)
	_ = logger

	// TODO: Fetch the ClusterSecret
	// TODO: Handle deletion with finalizer
	// TODO: Resolve data (direct or valueFrom)
	// TODO: Compute matching namespaces
	// TODO: Sync secrets to matching namespaces
	// TODO: Remove secrets from non-matching namespaces
	// TODO: Update status

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterSecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clustersecretv1.ClusterSecret{}).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.findClusterSecretsForNamespace),
		).
		Complete(r)
}

// findClusterSecretsForNamespace maps a Namespace event to reconciliation
// requests for all ClusterSecrets that might match it.
func (r *ClusterSecretReconciler) findClusterSecretsForNamespace(ctx context.Context, obj client.Object) []reconcile.Request {
	logger := r.Log.WithValues("namespace", obj.GetName())

	// TODO: List all ClusterSecrets and enqueue those whose matchNamespace
	// regex would match this namespace.

	logger.V(1).Info("namespace changed, enqueuing clustersecrets")
	return []reconcile.Request{}
}

// matchNamespace checks if a namespace name matches any pattern in include
// and does NOT match any pattern in exclude.
func matchNamespace(name string, include, exclude []string) (bool, error) {
	// TODO: Implement regex matching logic
	// This is a great place for your contribution!
	_ = name
	_ = include
	_ = exclude
	return false, nil
}
