package controller

import (
	"context"
	"fmt"
	"sort"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clustersecretv1 "github.com/satoukick/clustersecret-go/api/v1"
	csk8s "github.com/satoukick/clustersecret-go/internal/kubernetes"
)

const (
	clusterSecretFinalizer = "clustersecret.io/finalizer"
	managedByLabel         = "clustersecret.io/managed-by"
	managedByValue         = "clustersecret-operator"
	parentNameLabel        = "clustersecret.io/parent"
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

// Reconcile is the main reconciliation loop. It is invoked whenever a
// ClusterSecret changes, or whenever a Namespace event triggers a re-evaluation.
//
// The flow is:
//  1. Fetch the ClusterSecret. If gone, nothing to do (finalizer logic
//     guarantees children were already cleaned up before deletion completed).
//  2. If the object is being deleted, run the cleanup branch and remove the
//     finalizer so the API server can finish deletion.
//  3. Ensure the finalizer is present so we get a chance to clean up later.
//  4. Resolve the desired data (literal map or copied from another Secret).
//  5. Compute the set of matching namespaces.
//  6. Diff against status: create/update for new+existing matches, delete from
//     namespaces that no longer match.
//  7. Update status to reflect the new synced set.
func (r *ClusterSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("clustersecret", req.Name)

	var csec clustersecretv1.ClusterSecret
	if err := r.Get(ctx, req.NamespacedName, &csec); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ClusterSecret: %w", err)
	}

	// Deletion path: clean up children, then drop the finalizer.
	if !csec.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, log, &csec)
	}

	// Ensure finalizer present before we create any children.
	if !controllerutil.ContainsFinalizer(&csec, clusterSecretFinalizer) {
		controllerutil.AddFinalizer(&csec, clusterSecretFinalizer)
		if err := r.Update(ctx, &csec); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		// Update will trigger a fresh reconcile; return early.
		return ctrl.Result{}, nil
	}

	data, err := r.resolveData(ctx, &csec)
	if err != nil {
		log.Error(err, "resolve data failed")
		return ctrl.Result{}, err
	}

	matched, err := r.listMatchingNamespaces(ctx, &csec)
	if err != nil {
		log.Error(err, "list matching namespaces failed")
		return ctrl.Result{}, err
	}
	matchedSet := stringSet(matched)
	previouslySynced := stringSet(csec.Status.SyncedNamespaces)

	// Sync to all currently-matched namespaces.
	for _, ns := range matched {
		if err := r.syncSecretToNamespace(ctx, &csec, ns, data); err != nil {
			log.Error(err, "sync secret failed", "namespace", ns)
			return ctrl.Result{}, err
		}
	}

	// Delete from namespaces that previously had it but no longer match.
	for ns := range previouslySynced {
		if _, ok := matchedSet[ns]; ok {
			continue
		}
		if err := r.deleteSecretFromNamespace(ctx, &csec, ns); err != nil {
			log.Error(err, "delete stale secret failed", "namespace", ns)
			return ctrl.Result{}, err
		}
	}

	// Update status with the new synced list (sorted for stable output).
	sort.Strings(matched)
	csec.Status.SyncedNamespaces = matched
	if err := r.Status().Update(ctx, &csec); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	log.Info("reconciled", "matched", len(matched))
	return ctrl.Result{}, nil
}

// reconcileDelete cleans up all child Secrets, then removes the finalizer so
// the API server can complete deletion of the ClusterSecret.
func (r *ClusterSecretReconciler) reconcileDelete(ctx context.Context, log logr.Logger, csec *clustersecretv1.ClusterSecret) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(csec, clusterSecretFinalizer) {
		return ctrl.Result{}, nil
	}

	// Walk the recorded synced list rather than recomputing matches — at
	// deletion time the spec has already been removed from intent and we
	// only want to clean up what we previously created.
	for _, ns := range csec.Status.SyncedNamespaces {
		if err := r.deleteSecretFromNamespace(ctx, csec, ns); err != nil {
			log.Error(err, "cleanup secret failed", "namespace", ns)
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(csec, clusterSecretFinalizer)
	if err := r.Update(ctx, csec); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}

	log.Info("clustersecret cleaned up", "namespaces", len(csec.Status.SyncedNamespaces))
	return ctrl.Result{}, nil
}

// resolveData returns the final key-value map to put into each child Secret.
// It supports two mutually exclusive sources:
//   - Spec.Data: literal map declared inline on the ClusterSecret
//   - Spec.ValueFrom: copy data from an existing Secret in another namespace,
//     optionally restricted to a list of keys.
func (r *ClusterSecretReconciler) resolveData(ctx context.Context, csec *clustersecretv1.ClusterSecret) (map[string][]byte, error) {
	if csec.Spec.ValueFrom != nil && len(csec.Spec.Data) > 0 {
		return nil, fmt.Errorf("spec.data and spec.valueFrom are mutually exclusive")
	}

	if csec.Spec.ValueFrom != nil {
		vf := csec.Spec.ValueFrom
		var src corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Name: vf.Name, Namespace: vf.Namespace}, &src); err != nil {
			return nil, fmt.Errorf("get source secret %s/%s: %w", vf.Namespace, vf.Name, err)
		}

		if len(vf.Keys) == 0 {
			return src.Data, nil
		}
		out := make(map[string][]byte, len(vf.Keys))
		for _, k := range vf.Keys {
			if v, ok := src.Data[k]; ok {
				out[k] = v
			}
		}
		return out, nil
	}

	out := make(map[string][]byte, len(csec.Spec.Data))
	for k, v := range csec.Spec.Data {
		out[k] = []byte(v)
	}
	return out, nil
}

// listMatchingNamespaces returns the names of namespaces this ClusterSecret
// should sync to, based on its include/exclude regex patterns.
func (r *ClusterSecretReconciler) listMatchingNamespaces(ctx context.Context, csec *clustersecretv1.ClusterSecret) ([]string, error) {
	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList); err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}

	out := make([]string, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		// Skip terminating namespaces — creating Secrets in them fails.
		if ns.Status.Phase == corev1.NamespaceTerminating {
			continue
		}
		match, err := csk8s.MatchNamespace(ns.Name, csec.Spec.MatchNamespace, csec.Spec.AvoidNamespaces)
		if err != nil {
			return nil, err
		}
		if match {
			out = append(out, ns.Name)
		}
	}
	return out, nil
}

// syncSecretToNamespace creates or updates the child Secret in ns to match
// the desired state. It is idempotent — running it twice with the same input
// produces the same result.
func (r *ClusterSecretReconciler) syncSecretToNamespace(ctx context.Context, csec *clustersecretv1.ClusterSecret, ns string, data map[string][]byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csec.Name,
			Namespace: ns,
		},
	}

	secretType := corev1.SecretType(csec.Spec.Type)
	if secretType == "" {
		secretType = corev1.SecretTypeOpaque
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		// Refuse to overwrite a Secret we don't own. Without this guard, a
		// user-created Secret with a name colliding with the ClusterSecret
		// would be silently rewritten — and erased on cleanup.
		if existing, ok := secret.Labels[managedByLabel]; ok && existing != managedByValue {
			return fmt.Errorf("refusing to overwrite Secret %s/%s not managed by clustersecret-operator", ns, secret.Name)
		}
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels[managedByLabel] = managedByValue
		secret.Labels[parentNameLabel] = csec.Name
		secret.Type = secretType
		secret.Data = data
		return nil
	})
	if err != nil {
		return fmt.Errorf("create-or-update secret in %s: %w", ns, err)
	}
	return nil
}

// deleteSecretFromNamespace deletes the child Secret in ns, but only if it is
// still managed by this operator. Already-gone or foreign Secrets are skipped
// silently.
func (r *ClusterSecretReconciler) deleteSecretFromNamespace(ctx context.Context, csec *clustersecretv1.ClusterSecret, ns string) error {
	var secret corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Name: csec.Name, Namespace: ns}, &secret)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get secret %s/%s: %w", ns, csec.Name, err)
	}

	// Same guard as in sync: do not delete a Secret we do not own.
	if secret.Labels[managedByLabel] != managedByValue {
		return nil
	}

	if err := r.Delete(ctx, &secret); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete secret %s/%s: %w", ns, csec.Name, err)
	}
	return nil
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
// requests for every ClusterSecret whose patterns might match the namespace.
//
// We don't pre-filter by regex here on purpose: the Reconcile loop already
// computes the authoritative match set, and ClusterSecret count is typically
// orders of magnitude smaller than namespace event volume. Enqueuing them all
// is simpler and avoids subtle drift between the watch filter and the
// reconcile filter.
func (r *ClusterSecretReconciler) findClusterSecretsForNamespace(ctx context.Context, obj client.Object) []reconcile.Request {
	log := r.Log.WithValues("namespace", obj.GetName())

	var list clustersecretv1.ClusterSecretList
	if err := r.List(ctx, &list); err != nil {
		log.Error(err, "list clustersecrets for namespace mapping failed")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(list.Items))
	for _, item := range list.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: item.Name},
		})
	}
	log.V(1).Info("enqueuing clustersecrets for namespace event", "count", len(requests))
	return requests
}

// stringSet returns a set view of the input slice for O(1) lookups.
func stringSet(in []string) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, s := range in {
		out[s] = struct{}{}
	}
	return out
}
