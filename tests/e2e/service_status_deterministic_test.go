//nolint:testpackage
package e2e

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// randomString generates a random string of specified length.
func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

// TestServiceStatusDeterministic is designed to deterministically test
// the Service status issue across different branches.
func TestServiceStatusDeterministic(t *testing.T) {
	t.Log("=== Deterministic Service Status Test ===")

	// Create a test namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "service-status-test-" + randomString(5),
		},
	}

	err := TestEnv.Client.Create(TestEnv.Ctx, ns)
	require.NoError(t, err)

	defer func() {
		_ = TestEnv.Client.Delete(TestEnv.Ctx, ns)
	}()

	// Create a basic ClusterIP service directly (like LlamaStack does)
	testService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service-status",
			Namespace: ns.Name,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": "test",
			},
			Ports: []corev1.ServicePort{
				{
					Port:     80,
					Protocol: corev1.ProtocolTCP,
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}

	t.Logf("Creating test Service %s/%s", ns.Name, testService.Name)
	err = TestEnv.Client.Create(TestEnv.Ctx, testService)
	require.NoError(t, err)

	// Test the status availability over time with multiple approaches
	testServiceStatusAvailability(t, ns.Name, testService.Name)
}

func testServiceStatusAvailability(t *testing.T, namespace, serviceName string) {
	t.Helper()

	serviceGVK := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}

	// Test 1: Immediate check - how quickly is status available?
	t.Log("=== Test 1: Immediate Status Check ===")
	checkServiceStatusImmediately(t, namespace, serviceName, serviceGVK)

	// Test 2: Polling check - when does status become available?
	t.Log("=== Test 2: Polling Status Check ===")
	pollServiceStatusAvailability(t, namespace, serviceName, serviceGVK)

	// Test 3: Reproduce the exact LlamaStack readiness logic
	t.Log("=== Test 3: LlamaStack Readiness Logic Test ===")
	testLlamaStackReadinessLogic(t, namespace, serviceName, serviceGVK)
}

func checkServiceStatusImmediately(t *testing.T, namespace, serviceName string, gvk schema.GroupVersionKind) {
	t.Helper()

	serviceObj := &unstructured.Unstructured{}
	serviceObj.SetGroupVersionKind(gvk)
	serviceKey := client.ObjectKey{Namespace: namespace, Name: serviceName}

	err := TestEnv.Client.Get(TestEnv.Ctx, serviceKey, serviceObj)
	require.NoError(t, err, "Service should exist immediately after creation")

	spec, specFound, _ := unstructured.NestedMap(serviceObj.Object, "spec")
	status, statusFound, _ := unstructured.NestedMap(serviceObj.Object, "status")

	t.Logf("IMMEDIATE CHECK:")
	t.Logf("  Spec found: %v, Spec nil: %v", specFound, spec == nil)
	t.Logf("  Status found: %v, Status nil: %v", statusFound, status == nil)
	t.Logf("  Status content: %+v", status)

	// Log what the buggy logic would return
	buggyResult := specFound && statusFound && spec != nil && status != nil
	fixedResult := specFound && spec != nil
	t.Logf("  Buggy logic result: %v", buggyResult)
	t.Logf("  Fixed logic result: %v", fixedResult)
}

func pollServiceStatusAvailability(t *testing.T, namespace, serviceName string, gvk schema.GroupVersionKind) {
	t.Helper()

	serviceObj := &unstructured.Unstructured{}
	serviceObj.SetGroupVersionKind(gvk)
	serviceKey := client.ObjectKey{Namespace: namespace, Name: serviceName}

	pollCount := 0

	err := wait.PollUntilContextTimeout(TestEnv.Ctx, 1*time.Second, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		pollCount++

		err := TestEnv.Client.Get(ctx, serviceKey, serviceObj)
		if err != nil {
			t.Logf("POLL %d: Error getting service: %v", pollCount, err)
			return false, nil
		}

		spec, specFound, _ := unstructured.NestedMap(serviceObj.Object, "spec")
		status, statusFound, _ := unstructured.NestedMap(serviceObj.Object, "status")

		buggyResult := specFound && statusFound && spec != nil && status != nil
		fixedResult := specFound && spec != nil

		t.Logf("POLL %d:", pollCount)
		t.Logf("  Spec found: %v, Status found: %v", specFound, statusFound)
		t.Logf("  Status content: %+v", status)
		t.Logf("  Buggy logic: %v, Fixed logic: %v", buggyResult, fixedResult)

		// Stop polling when status is available (or after reasonable attempts)
		if statusFound && status != nil {
			t.Logf("Status became available after %d polls", pollCount)
			return true, nil
		}

		if pollCount >= 10 {
			t.Logf("Stopping after %d polls - status still not available", pollCount)
			return true, nil
		}

		return false, nil
	})

	if err != nil {
		t.Logf("Polling completed with error: %v", err)
	}
}

func testLlamaStackReadinessLogic(t *testing.T, namespace, serviceName string, gvk schema.GroupVersionKind) {
	t.Helper()

	// This reproduces exactly what the LlamaStack e2e test does
	serviceObj := &unstructured.Unstructured{}
	serviceObj.SetGroupVersionKind(gvk)
	serviceKey := client.ObjectKey{Namespace: namespace, Name: serviceName}

	// Try the readiness check multiple times like EnsureResourceReady does
	for attempt := 1; attempt <= 5; attempt++ {
		t.Logf("=== Readiness Attempt %d ===", attempt)

		err := TestEnv.Client.Get(TestEnv.Ctx, serviceKey, serviceObj)
		require.NoError(t, err)

		spec, specFound, _ := unstructured.NestedMap(serviceObj.Object, "spec")
		status, statusFound, _ := unstructured.NestedMap(serviceObj.Object, "status")

		// Exact logic from LlamaStack test
		originalResult := specFound && statusFound && spec != nil && status != nil
		fixedResult := specFound && spec != nil

		t.Logf("Attempt %d: Buggy=%v, Fixed=%v", attempt, originalResult, fixedResult)
		t.Logf("Attempt %d: Status content=%+v", attempt, status)

		if originalResult {
			t.Logf("SUCCESS: Buggy logic passed on attempt %d", attempt)
			return
		}

		if fixedResult && !originalResult {
			t.Logf("DIFFERENCE: Fixed logic would pass but buggy logic fails on attempt %d", attempt)
		}

		// Wait before next attempt
		if attempt < 5 {
			time.Sleep(2 * time.Second)
		}
	}

	t.Logf("RESULT: Buggy logic never succeeded in 5 attempts")
}
