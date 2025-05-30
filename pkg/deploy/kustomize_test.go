package deploy_test

import (
	"path/filepath"
	"testing"

	"github.com/llamastack/llama-stack-k8s-operator/pkg/deploy"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

func TestRenderKustomize_InMemory(t *testing.T) {
	memFs := filesys.MakeFsInMemory()

	// Set up base
	baseDir := "manifests/base"
	require.NoError(t, memFs.MkdirAll(baseDir))
	require.NoError(t, memFs.WriteFile(
		filepath.Join(baseDir, "kustomization.yaml"),
		[]byte(`resources:
- deployment.yaml
`),
	))
	require.NoError(t, memFs.WriteFile(
		filepath.Join(baseDir, "deployment.yaml"),
		[]byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: foo
spec:
  replicas: 1
`),
	))

	// Updated overlay using patches
	overlayDir := "manifests/overlay"
	require.NoError(t, memFs.MkdirAll(overlayDir))
	require.NoError(t, memFs.WriteFile(
		filepath.Join(overlayDir, "kustomization.yaml"),
		[]byte(`resources:
- ../base
patches:
- path: replica-patch.yaml
  target:
    kind: Deployment
    name: foo
`),
	))
	require.NoError(t, memFs.WriteFile(
		filepath.Join(overlayDir, "replica-patch.yaml"),
		[]byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: foo
spec:
  replicas: 3
`),
	))

	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())

	objs, err := deploy.RenderKustomize(memFs, k, overlayDir)
	require.NoError(t, err)
	require.Len(t, objs, 1, "should render exactly one object")

	u := objs[0]
	require.Equal(t, "Deployment", u.GetKind())
	require.Equal(t, "foo", u.GetName())

	// Verify that the replicas field was patched to 3
	rep, found, err := unstructured.NestedInt64(u.Object, "spec", "replicas")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(3), rep)
}
