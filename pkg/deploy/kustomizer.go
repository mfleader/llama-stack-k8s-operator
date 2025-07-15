package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	llamav1alpha1 "github.com/llamastack/llama-stack-k8s-operator/api/v1alpha1"
	"github.com/llamastack/llama-stack-k8s-operator/pkg/compare"
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
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// RenderManifest takes a manifest directory and transforms it through
// kustomization and plugins to produce final Kubernetes resources.
func RenderManifest(
	fs filesys.FileSystem,
	manifestPath string,
	ownerInstance *llamav1alpha1.LlamaStackDistribution,
) (*resmap.ResMap, error) {
	// Track disk usage during rendering
	log.Log.Info("Disk usage before kustomize render", "path", manifestPath, "df", getDiskUsageInfo())

	// fallback to the 'default' directory' if we cannot initially find
	// the kustomization file
	finalManifestPath := manifestPath
	if exists := fs.Exists(filepath.Join(manifestPath, "kustomization.yaml")); !exists {
		finalManifestPath = filepath.Join(manifestPath, "default")
	}

	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())

	log.Log.Info("Running kustomize", "path", finalManifestPath)
	resMapVal, err := k.Run(fs, finalManifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to run kustomize: %w", err)
	}

	log.Log.Info("Disk usage after kustomize run", "resource_count", resMapVal.Size(), "df", getDiskUsageInfo())

	if err := applyPlugins(&resMapVal, ownerInstance); err != nil {
		return nil, err
	}

	log.Log.Info("Disk usage after plugins", "df", getDiskUsageInfo())

	return &resMapVal, nil
}

// Helper function to get disk usage info.
func getDiskUsageInfo() string {
	cmd := exec.Command("df", "-h", "/")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	lines := strings.Split(string(output), "\n")
	if len(lines) >= 2 {
		return lines[1] // Return just the data line
	}
	return "unknown"
}

// ApplyResources takes a Kustomize ResMap and applies the resources to the cluster.
func ApplyResources(
	ctx context.Context,
	cli client.Client,
	scheme *runtime.Scheme,
	ownerInstance *llamav1alpha1.LlamaStackDistribution,
	resMap *resmap.ResMap,
) error {
	for _, res := range (*resMap).Resources() {
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
	// prevent the controller from trying to apply changes to its own CR
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
	// Check if the resource is cluster-scoped (like a ClusterRole) to avoid
	// incorrectly setting a namespace-bound owner reference on it.
	isClusterScoped, err := isClusterScoped(cli.RESTMapper(), gvk)
	if err != nil {
		return fmt.Errorf("failed to determine resource scope: %w", err)
	}
	if !isClusterScoped {
		if err := ctrl.SetControllerReference(ownerInstance, obj, scheme); err != nil {
			return fmt.Errorf("failed to set controller reference for %s: %w", gvk.Kind, err)
		}
	}
	return cli.Create(ctx, obj)
}

// isClusterScoped checks if a given GVK refers to a cluster-scoped resource.
func isClusterScoped(mapper meta.RESTMapper, gvk schema.GroupVersionKind) (bool, error) {
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return false, fmt.Errorf("failed to get REST mapping for GVK %v: %w", gvk, err)
	}
	return mapping.Scope.Name() == meta.RESTScopeNameRoot, nil
}

// patchResource patches an existing resource, but only if we own it.
func patchResource(ctx context.Context, cli client.Client, desired, existing *unstructured.Unstructured, ownerInstance *llamav1alpha1.LlamaStackDistribution) error {
	logger := log.FromContext(ctx)

	// Critical safety check to prevent the operator from "stealing" or
	// overwriting a resource that was created by another user or controller.
	isOwner := false
	for _, ref := range existing.GetOwnerReferences() {
		if ref.UID == ownerInstance.GetUID() {
			isOwner = true
			break
		}
	}
	if !isOwner {
		logger.Info("Skipping resource not owned by this instance",
			"kind", existing.GetKind(),
			"name", existing.GetName(),
			"namespace", existing.GetNamespace())
		return nil
	}

	if existing.GetKind() == "PersistentVolumeClaim" {
		logger.Info("Skipping PVC patch - PVCs are immutable after creation",
			"name", existing.GetName(),
			"namespace", existing.GetNamespace())
		return nil
	} else if existing.GetKind() == "Service" {
		if err := compare.CheckAndLogServiceChanges(ctx, cli, desired); err != nil {
			return fmt.Errorf("failed to validate resource mutations while patching: %w", err)
		}
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
func applyPlugins(resMap *resmap.ResMap, ownerInstance *llamav1alpha1.LlamaStackDistribution) error {
	namePrefixPlugin := plugins.CreateNamePrefixPlugin(plugins.NamePrefixConfig{
		Prefix: ownerInstance.GetName(),
	})
	if err := namePrefixPlugin.Transform(*resMap); err != nil {
		return fmt.Errorf("failed to apply name prefix: %w", err)
	}

	namespaceSetterPlugin, err := plugins.CreateNamespacePlugin(ownerInstance.GetNamespace())
	if err != nil {
		return err
	}
	if err := namespaceSetterPlugin.Transform(*resMap); err != nil {
		return fmt.Errorf("failed to apply namespace setter plugin: %w", err)
	}

	fieldTransformerPlugin := plugins.CreateFieldMutator(plugins.FieldMutatorConfig{
		Mappings: []plugins.FieldMapping{
			{
				SourceValue:       getStorageSize(ownerInstance),
				DefaultValue:      llamav1alpha1.DefaultStorageSize.String(),
				TargetField:       "spec.resources.requests.storage",
				TargetKind:        "PersistentVolumeClaim",
				CreateIfNotExists: true,
			},
			{
				SourceValue:       getServicePort(ownerInstance),
				DefaultValue:      llamav1alpha1.DefaultServerPort,
				TargetField:       "spec.ports[0].port",
				TargetKind:        "Service",
				CreateIfNotExists: true,
			},
			{
				SourceValue:       getServicePort(ownerInstance),
				DefaultValue:      llamav1alpha1.DefaultServerPort,
				TargetField:       "spec.ports[0].targetPort",
				TargetKind:        "Service",
				CreateIfNotExists: true,
			},
		},
	})
	if err := fieldTransformerPlugin.Transform(*resMap); err != nil {
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

// getServicePort extracts the service port from the CR spec.
func getServicePort(instance *llamav1alpha1.LlamaStackDistribution) int32 {
	if instance.Spec.Server.ContainerSpec.Port != 0 {
		return instance.Spec.Server.ContainerSpec.Port
	}
	// Returning 0 signals the field transformer to use the default value.
	return 0
}

func FilterExcludeKinds(resMap *resmap.ResMap, kindsToExclude []string) (*resmap.ResMap, error) {
	filteredResMap := resmap.New()
	for _, res := range (*resMap).Resources() {
		if !slices.Contains(kindsToExclude, res.GetKind()) {
			if err := filteredResMap.Append(res); err != nil {
				return nil, fmt.Errorf("failed to append resource while filtering %s/%s: %w", res.GetKind(), res.GetName(), err)
			}
		}
	}
	return &filteredResMap, nil
}
