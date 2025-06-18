package plugins

import (
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
)

// newTestResource is now provided by field_transformer_test.go in this package.
// The local, old version of newTestResource func will be deleted from this file.

func TestNamePrefixTransformer(t *testing.T) {
	testCases := []struct {
		name               string
		transformer        *namePrefixTransformer
		initialResources   []*resource.Resource
		expectedFinalNames []string
	}{
		{
			name: "apply prefix to all resources when no filters are set",
			transformer: CreateNamePrefixPlugin(NamePrefixConfig{
				Prefix: "my-prefix",
			}),
			initialResources: []*resource.Resource{
				newTestResource(t, "apps/v1", "Deployment", "backend", "", nil), // Updated call
				newTestResource(t, "v1", "Service", "frontend", "", nil),        // Updated call
			},
			expectedFinalNames: []string{"my-prefix-backend", "my-prefix-frontend"},
		},
		{
			name: "apply prefix only to included kinds",
			transformer: CreateNamePrefixPlugin(NamePrefixConfig{
				Prefix:        "my-prefix",
				ResourceKinds: []string{"Deployment"},
			}),
			initialResources: []*resource.Resource{
				newTestResource(t, "apps/v1", "Deployment", "backend", "", nil), // Updated call
				newTestResource(t, "v1", "Service", "frontend", "", nil),        // Updated call
			},
			expectedFinalNames: []string{"my-prefix-backend", "frontend"},
		},
		{
			name: "do not apply prefix to excluded kinds",
			transformer: CreateNamePrefixPlugin(NamePrefixConfig{
				Prefix:       "my-prefix",
				ExcludeKinds: []string{"Service"},
			}),
			initialResources: []*resource.Resource{
				newTestResource(t, "apps/v1", "Deployment", "backend", "", nil), // Updated call
				newTestResource(t, "v1", "Service", "frontend", "", nil),        // Updated call
			},
			expectedFinalNames: []string{"my-prefix-backend", "frontend"},
		},
		{
			name: "do not apply prefix if already present",
			transformer: CreateNamePrefixPlugin(NamePrefixConfig{
				Prefix: "my-prefix",
			}),
			initialResources: []*resource.Resource{
				newTestResource(t, "apps/v1", "Deployment", "my-prefix-backend", "", nil), // Updated call
				newTestResource(t, "v1", "Service", "frontend", "", nil),                  // Updated call
			},
			expectedFinalNames: []string{"my-prefix-backend", "my-prefix-frontend"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup
			resMap := resmap.New()
			for _, res := range tc.initialResources {
				err := resMap.Append(res)
				require.NoError(t, err)
			}

			// Action
			err := tc.transformer.Transform(resMap)
			require.NoError(t, err)

			// Assertion
			require.Equal(t, len(tc.initialResources), resMap.Size())

			actualFinalNames := []string{}
			for _, r := range resMap.Resources() {
				actualFinalNames = append(actualFinalNames, r.GetName())
			}
			require.ElementsMatch(t, tc.expectedFinalNames, actualFinalNames)
		})
	}
}
