package v1

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is group version used to register these objects.
var GroupVersion = schema.GroupVersion{
	Group:   "clustersecret.io",
	Version: "v1",
}

// SchemeGroupVersion is an alias for GroupVersion for compatibility.
var SchemeGroupVersion = GroupVersion

var (
	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// Resource takes an unqualified resource and returns a Group-qualified GroupResource.
func Resource(resource string) schema.GroupResource {
	return GroupVersion.WithResource(resource).GroupResource()
}

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&ClusterSecret{},
		&ClusterSecretList{},
	)
	return nil
}
