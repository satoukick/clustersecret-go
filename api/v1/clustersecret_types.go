package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:generate=true

// ValueFromSource references an existing Secret to copy data from.
type ValueFromSource struct {
	// Name of the source Secret.
	Name string `json:"name"`
	// Namespace of the source Secret.
	Namespace string `json:"namespace"`
	// Keys restricts which keys to copy. Empty means all keys.
	Keys []string `json:"keys,omitempty"`
}

// +kubebuilder:object:generate=true

// ClusterSecretSpec defines the desired state of ClusterSecret.
type ClusterSecretSpec struct {
	// MatchNamespace is a list of regex patterns. Secrets are synced to
	// namespaces matching ANY of these patterns.
	MatchNamespace []string `json:"matchNamespace,omitempty"`

	// AvoidNamespaces is a list of regex patterns. Namespaces matching ANY
	// of these are excluded even if they match MatchNamespace.
	AvoidNamespaces []string `json:"avoidNamespaces,omitempty"`

	// Data contains the secret key-value pairs.
	Data map[string]string `json:"data,omitempty"`

	// ValueFrom references an existing Secret to copy data from.
	// Mutually exclusive with Data.
	ValueFrom *ValueFromSource `json:"valueFrom,omitempty"`

	// Type is the Kubernetes Secret type (e.g. Opaque, kubernetes.io/tls).
	// Defaults to Opaque.
	Type string `json:"type,omitempty"`
}

// +kubebuilder:object:generate=true

// ClusterSecretStatus defines the observed state of ClusterSecret.
type ClusterSecretStatus struct {
	// SyncedNamespaces lists the namespaces this ClusterSecret has been
	// synced to.
	SyncedNamespaces []string `json:"syncedNamespaces,omitempty"`

	// Conditions represent the latest available observations of the
	// ClusterSecret's state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=csec

// ClusterSecret is the Schema for the clustersecrets API.
type ClusterSecret struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterSecretSpec   `json:"spec,omitempty"`
	Status ClusterSecretStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterSecretList contains a list of ClusterSecret.
type ClusterSecretList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterSecret `json:"items"`
}
