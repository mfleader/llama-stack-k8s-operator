package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	llamav1alpha1 "github.com/llamastack/llama-stack-k8s-operator/api/v1alpha1"
	"github.com/llamastack/llama-stack-k8s-operator/pkg/deploy/plugins"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// RenderKustomize takes a filesystem and a path, runs kustomize, applies Go-based
// plugins, and returns the final set of in-memory resources.
func RenderKustomize(
	fs filesys.FileSystem,
	manifestPath string,
	ownerInstance *llamav1alpha1.LlamaStackDistribution,
) (*resmap.ResMap, error) {
	// Determine the correct path for the kustomization.yaml's directory,
	// falling back to "default" if needed.
	finalManifestPath := manifestPath // This is the directory Kustomize will run from

	// Check if kustomization.yaml exists in the base path.
	// If not, assume it's in a 'default' subdirectory.
	if exists := fs.Exists(filepath.Join(manifestPath, "kustomization.yaml")); !exists {
		finalManifestPath = filepath.Join(manifestPath, "default")
	}

	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())

	resMapVal, err := k.Run(fs, finalManifestPath) // resMapVal is resmap.ResMap (value)
	if err != nil {
		return nil, fmt.Errorf("failed to run kustomize: %w", err)
	}

	// Pass address of resMapVal to applyPlugins
	if err := applyPlugins(&resMapVal, ownerInstance); err != nil { // Pass pointer to applyPlugins
		return nil, err
	}

	return &resMapVal, nil // Return pointer to the modified resMapVal
}

// ApplyResources takes a Kustomize ResMap and applies the resources to the cluster.
func ApplyResources(
	ctx context.Context,
	cli client.Client,
	scheme *runtime.Scheme,
	ownerInstance *llamav1alpha1.LlamaStackDistribution,
	resMap *resmap.ResMap, // Receives pointer
) error {
	for _, res := range (*resMap).Resources() { // Correctly dereferences for Resources()
		if err := manageResource(ctx, cli, scheme, res, ownerInstance); err != nil {
			return fmt.Errorf("failed to manage resource %s/%s: %w", res.GetKind(), res.GetName(), err)
		}
	}
	return nil
}

// manageResource acts as a dispatcher, checking if a resource exists and then
// deciding whether to create it or patch it.
func manageResource(
	ctx context.Context,
	cli client.Client,
	scheme *runtime.Scheme,
	res *resource.Resource,
	ownerInstance *llamav1alpha1.LlamaStackDistribution,
) error {
	// Prevent the controller from trying to apply changes to its own CR.
	if res.GetKind() == llamav1alpha1.LlamaStackDistributionKind && res.GetName() == ownerInstance.Name && res.GetNamespace() == ownerInstance.Namespace {
		return nil
	}

	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(res.MustYaml()), u); err != nil {
		return fmt.Errorf("failed to unmarshal resource: %w", err)
	}

	kGvk := res.GetGvk()
	gvk := schema.GroupVersionKind{
		Group:   kGvk.Group,
		Version: kGvk.Version,
		Kind:    kGvk.Kind,
	}

	found := u.DeepCopy()
	err := cli.Get(ctx, client.ObjectKeyFromObject(u), found)
	if err != nil {
		if !k8serr.IsNotFound(err) {
			return fmt.Errorf("failed to get resource: %w", err)
		}
		return createResource(ctx, cli, u, ownerInstance, scheme, gvk)
	}
	return patchResource(ctx, cli, u, found, ownerInstance)
}

// createResource creates a new resource, setting an owner reference only if it's namespace-scoped.
func createResource(
	ctx context.Context,
	cli client.Client,
	obj *unstructured.Unstructured,
	ownerInstance *llamav1alpha1.LlamaStackDistribution,
	scheme *runtime.Scheme,
	gvk schema.GroupVersionKind,
) error {
	// We must check if the resource is cluster-scoped (like a ClusterRole) to avoid
	// incorrectly setting a namespace-bound owner reference on it.
	// The namespace is now set by the namespace_setter plugin.
	isClusterScoped, err := isClusterScoped(cli.RESTMapper(), gvk)
	if err != nil {
		return fmt.Errorf("failed to determine resource scope: %w", err)
	}

	if !isClusterScoped {
		// Namespace should already be set by the namespace_setter plugin.
		// obj.SetNamespace(ownerInstance.Namespace) // This line is no longer strictly needed but harmless if namespace is already correct.
		if err := ctrl.SetControllerReference(ownerInstance, obj, scheme); err != nil {
			return fmt.Errorf("failed to set controller reference for %s: %w", gvk.Kind, err)
		}
	}

	return cli.Create(ctx, obj)
}

// isClusterScoped checks if a given GVK refers to a cluster-scoped resource.
// This function remains in this file as it's used by createResource.
func isClusterScoped(mapper meta.RESTMapper, gvk schema.GroupVersionKind) (bool, error) {
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return false, fmt.Errorf("failed to get REST mapping for GVK %v: %w", gvk, err)
	}
	return mapping.Scope.Name() == meta.RESTScopeNameRoot, nil
}

// patchResource patches an existing resource, but only if we own it.
func patchResource(ctx context.Context, cli client.Client, desired, existing *unstructured.Unstructured, ownerInstance *llamav1alpha1.LlamaStackDistribution) error {
	// This is a critical safety check to prevent the operator from "stealing" or
	// overwriting a resource that was created by another user or controller.
	isOwner := false
	for _, ref := range existing.GetOwnerReferences() {
		if ref.UID == ownerInstance.GetUID() {
			isOwner = true
			break
		}
	}
	if !isOwner {
		return fmt.Errorf("failed to patch resource %s/%s because it is not owned by this instance",
			existing.GetKind(), existing.GetName())
	}

	data, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("failed to marshal desired state: %w", err)
	}

	return cli.Patch(
		ctx,
		existing,
		client.RawPatch(k8stypes.ApplyPatchType, data),
		client.ForceOwnership,
		client.FieldOwner(ownerInstance.GetName()),
	)
}

// applyPlugins runs all Go-based transformations on the resource map.
// It now consistently operates on a pointer to resmap.ResMap.
func applyPlugins(resMap *resmap.ResMap, ownerInstance *llamav1alpha1.LlamaStackDistribution) error { // UPDATED: Accepts *resmap.ResMap
	namePrefixPlugin := plugins.CreateNamePrefixPlugin(plugins.NamePrefixConfig{
		Prefix: ownerInstance.GetName(),
	})
	if err := namePrefixPlugin.Transform(*resMap); err != nil { // Correctly dereferences for Transform
		return fmt.Errorf("failed to apply name prefix: %w", err)
	}

	// NEW: Apply the namespace setter plugin. It handles scope internally.
	namespaceSetterPlugin := plugins.CreateNamespacePlugin(ownerInstance.GetNamespace())
	if err := namespaceSetterPlugin.Transform(*resMap); err != nil { // Correctly dereferences for Transform
		return fmt.Errorf("failed to apply namespace setter plugin: %w", err)
	}

	fieldTransformerPlugin := plugins.CreateFieldTransformer(plugins.FieldTransformerConfig{
		Mappings: []plugins.FieldMapping{
			{
				SourceValue:       getStorageSize(ownerInstance),
				DefaultValue:      llamav1alpha1.DefaultStorageSize.String(),
				TargetField:       "spec.resources.requests.storage",
				TargetKind:        "PersistentVolumeClaim",
				CreateIfNotExists: true,
			},
		},
	})
	if err := fieldTransformerPlugin.Transform(*resMap); err != nil { // Correctly dereferences for Transform
		return fmt.Errorf("failed to apply field transformer: %w", err)
	}

	return nil
}

// getStorageSize extracts the storage size from the CR spec.
func getStorageSize(instance *llamav1alpha1.LlamaStackDistribution) string {
	if instance.Spec.Server.Storage != nil && instance.Spec.Server.Storage.Size != nil {
		return instance.Spec.Server.Storage.Size.String()
	}
	// Returning an empty string signals the field transformer to use the default value.
	return ""
}
