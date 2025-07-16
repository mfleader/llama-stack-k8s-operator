//nolint:testpackage
package e2e

import (
	"testing"
	"time"

	"github.com/llamastack/llama-stack-k8s-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestServiceCreationDeterministic focuses specifically on Service creation
// via the operator to understand why Services aren't being created in e2e.
// Modified to reproduce the exact failure scenario from the original creation test.
func TestServiceCreationDeterministic(t *testing.T) {
	t.Log("=== Deterministic Service Creation E2E Test (Reproducing Original Failure) ===")

	// Use SAME namespace as original test
	testNS := "llama-stack-test"
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNS,
		},
	}

	err := TestEnv.Client.Create(TestEnv.Ctx, ns)
	if err != nil && !k8serrors.IsNotFound(err) {
		require.NoError(t, err)
	}

	defer func() {
		_ = TestEnv.Client.Delete(TestEnv.Ctx, ns)
	}()

	t.Logf("Using same namespace as original test: %s", testNS)

	// Step 1: Verify operator is running
	verifyOperatorRunning(t)

	// Step 2: Create SAME CR as original test
	llsd := createSameCRAsOriginal(t, testNS)

	// Step 3: Monitor with SAME logic as original test
	monitorServiceCreationWithOriginalLogic(t, testNS, llsd.Name)
}

// verifyOperatorRunning checks that the operator controller-manager is running and ready.
func verifyOperatorRunning(t *testing.T) {
	t.Helper()
	t.Log("=== Verifying Operator Status ===")

	operatorNS := "llama-stack-k8s-operator-system"
	deploymentName := "llama-stack-k8s-operator-controller-manager"

	// Check deployment exists and is ready
	deployment := &appsv1.Deployment{}
	err := TestEnv.Client.Get(TestEnv.Ctx, client.ObjectKey{
		Namespace: operatorNS,
		Name:      deploymentName,
	}, deployment)
	require.NoError(t, err, "Operator deployment should exist")

	t.Logf("Operator deployment found:")
	t.Logf("  Replicas: %d/%d", deployment.Status.ReadyReplicas, deployment.Status.Replicas)
	t.Logf("  Available: %d", deployment.Status.AvailableReplicas)
	t.Logf("  Conditions: %+v", deployment.Status.Conditions)

	require.Equal(t, int32(1), deployment.Status.ReadyReplicas, "Operator should have 1 ready replica")

	// Check operator pods are running
	podList := &corev1.PodList{}
	err = TestEnv.Client.List(TestEnv.Ctx, podList, client.InNamespace(operatorNS))
	require.NoError(t, err)

	var operatorPod *corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Labels["control-plane"] == "controller-manager" {
			operatorPod = pod
			break
		}
	}
	require.NotNil(t, operatorPod, "Operator pod should exist")

	t.Logf("Operator pod status:")
	t.Logf("  Phase: %s", operatorPod.Status.Phase)
	t.Logf("  Ready: %v", isPodReady(operatorPod))
	t.Logf("  Container statuses: %+v", operatorPod.Status.ContainerStatuses)

	require.Equal(t, corev1.PodRunning, operatorPod.Status.Phase, "Operator pod should be running")
	require.True(t, isPodReady(operatorPod), "Operator pod should be ready")

	t.Log("✅ Operator is running and ready")
}

// isPodReady checks if a pod is ready.
func isPodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// createSameCRAsOriginal creates the exact same CR as the original creation test.
func createSameCRAsOriginal(t *testing.T, namespace string) *v1alpha1.LlamaStackDistribution {
	t.Helper()
	t.Log("=== Creating SAME LlamaStackDistribution as Original Test ===")

	// Use GetSampleCR() exactly like the original test
	llsd := GetSampleCR(t)
	llsd.Namespace = namespace

	t.Logf("Creating LlamaStackDistribution: %s/%s", namespace, llsd.Name)
	t.Logf("CR name: %s (same as original)", llsd.Name)
	t.Logf("HasPorts() would return: %v", hasPortsWouldReturn(llsd))

	err := TestEnv.Client.Create(TestEnv.Ctx, llsd)
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}

	t.Log("✅ LlamaStackDistribution created successfully (same as original)")
	return llsd
}

// hasPortsWouldReturn simulates the HasPorts() method logic.
func hasPortsWouldReturn(llsd *v1alpha1.LlamaStackDistribution) bool {
	containerSpec := llsd.Spec.Server.ContainerSpec
	return containerSpec.Port != 0 || len(containerSpec.Env) > 0
}

// monitorServiceCreationWithOriginalLogic uses the EXACT same Service readiness logic as the original test.
func monitorServiceCreationWithOriginalLogic(t *testing.T, namespace, distributionName string) {
	t.Helper()
	t.Log("=== Using EXACT Original Service Readiness Logic with Enhanced Debugging ===")

	serviceName := distributionName + "-service"
	serviceGVK := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}

	t.Logf("Monitoring for Service: %s/%s", namespace, serviceName)
	t.Logf("Using original timeout: %v", ResourceReadyTimeout)
	t.Logf("Using original polling interval: %v", pollInterval)

	// EXACT pre-check logic from original test
	t.Logf("=== Pre-Service Check Debug (Original Logic) ===")
	serviceObj := &unstructured.Unstructured{}
	serviceObj.SetGroupVersionKind(serviceGVK)
	serviceKey := client.ObjectKey{Namespace: namespace, Name: serviceName}

	getErr := TestEnv.Client.Get(TestEnv.Ctx, serviceKey, serviceObj)
	if getErr != nil {
		t.Logf("Service %s/%s does not exist yet or error getting it: %v", namespace, serviceName, getErr)
	} else {
		t.Logf("Service %s/%s exists! Current object: %+v", namespace, serviceName, serviceObj.Object)

		// Check current status immediately
		preSpec, preSpecFound, _ := unstructured.NestedMap(serviceObj.Object, "spec")
		preStatus, preStatusFound, _ := unstructured.NestedMap(serviceObj.Object, "status")
		t.Logf("PRE-CHECK: Spec found: %v (nil: %v), Status found: %v, Status content: %+v", preSpecFound, preSpec == nil, preStatusFound, preStatus)
	}
	t.Logf("=== End Pre-Service Check ===")

	// ENHANCED: Check LlamaStackDistribution status before Service check
	checkLlamaStackDistributionStatus(t, namespace, distributionName)

	// Use EXACT EnsureResourceReady call from original test
	t.Logf("Starting Service readiness check with %v timeout", ResourceReadyTimeout)

	// ENHANCED: Track timing and attempts
	startTime := time.Now()
	attemptCount := 0

	err := EnsureResourceReady(t, TestEnv, serviceGVK, serviceName, namespace, ResourceReadyTimeout, func(u *unstructured.Unstructured) bool {
		attemptCount++
		elapsed := time.Since(startTime)

		// ENHANCED: More detailed logging with timing
		t.Logf("\n=== Service Readiness Check Debug (Attempt %d after %v) ===", attemptCount, elapsed.Truncate(time.Second))

		// Check spec field
		spec, specFound, _ := unstructured.NestedMap(u.Object, "spec")
		t.Logf("Spec found: %v, Spec nil: %v", specFound, spec == nil)

		// Check status field existence and content
		status, statusFound, _ := unstructured.NestedMap(u.Object, "status")
		t.Logf("Status found: %v, Status nil: %v, Status content: %+v", statusFound, status == nil, status)

		// Log service type for verification
		serviceType, typeFound, _ := unstructured.NestedString(u.Object, "spec", "type")
		t.Logf("Service type found: %v, Service type: %s", typeFound, serviceType)

		// ENHANCED: Log service details
		if specFound {
			ports, _, _ := unstructured.NestedSlice(u.Object, "spec", "ports")
			selector, _, _ := unstructured.NestedMap(u.Object, "spec", "selector")
			t.Logf("Service ports: %v", ports)
			t.Logf("Service selector: %v", selector)
		}

		// Log object keys to see what fields actually exist
		var objectKeys []string
		for key := range u.Object {
			objectKeys = append(objectKeys, key)
		}
		t.Logf("Service object keys: %v", objectKeys)

		// EXACT BUGGY LOGIC from original test - this should cause the failure!
		originalResult := specFound && statusFound && spec != nil && status != nil
		t.Logf("Original buggy logic result: %v", originalResult)

		// PROPOSED FIX LOGIC - checking only spec (for comparison)
		fixedResult := specFound && spec != nil
		t.Logf("Fixed logic result: %v", fixedResult)

		// ENHANCED: Show the difference clearly
		if fixedResult && !originalResult {
			t.Logf("🔍 DIFFERENCE: Fixed logic passes but buggy logic fails (attempt %d)", attemptCount)
		}

		t.Logf("=== End Service Readiness Check Debug ===")

		// Use the original buggy logic to reproduce the failure!
		return originalResult
	})

	// ENHANCED: Better failure analysis
	if err != nil {
		finalElapsed := time.Since(startTime)
		t.Logf("❌ REPRODUCED ORIGINAL FAILURE after %v (%d attempts): %v", finalElapsed, attemptCount, err)
		t.Log("=== This confirms the issue is in the Service readiness check logic ===")

		// ENHANCED: Show final state
		t.Log("=== Final State Analysis ===")
		checkLlamaStackDistributionStatus(t, namespace, distributionName)
		checkNamespaceEvents(t, namespace)
		checkOperatorHealth(t)

		// Show what the fix would be
		t.Log("=== Testing Fix: Using spec-only logic ===")
		fixErr := EnsureResourceReady(t, TestEnv, serviceGVK, serviceName, namespace, 30*time.Second, func(u *unstructured.Unstructured) bool {
			spec, specFound, _ := unstructured.NestedMap(u.Object, "spec")
			fixedResult := specFound && spec != nil
			t.Logf("Fixed logic result: %v", fixedResult)
			return fixedResult
		})

		if fixErr == nil {
			t.Log("✅ FIXED LOGIC WORKS: Service is ready when checking spec only!")
		} else {
			t.Logf("❌ Even fixed logic failed: %v", fixErr)
		}

		// Fail the test to show the reproduction
		require.NoError(t, err, "Reproduced original failure: Service readiness check timed out")
	} else {
		t.Log("🤔 Original logic passed - unable to reproduce failure")
	}
}

// ENHANCED: Add helper functions for better debugging
func checkLlamaStackDistributionStatus(t *testing.T, namespace, name string) {
	t.Helper()

	llsd := &v1alpha1.LlamaStackDistribution{}
	err := TestEnv.Client.Get(TestEnv.Ctx, client.ObjectKey{Namespace: namespace, Name: name}, llsd)
	if err != nil {
		t.Logf("⚠️  Error getting LlamaStackDistribution: %v", err)
		return
	}

	t.Logf("LlamaStackDistribution status:")
	t.Logf("  Phase: %s", llsd.Status.Phase)
	t.Logf("  Generation: %d", llsd.Generation)
	t.Logf("  ResourceVersion: %s", llsd.ResourceVersion)
	t.Logf("  Conditions: %+v", llsd.Status.Conditions)
}

func checkNamespaceEvents(t *testing.T, namespace string) {
	t.Helper()

	eventList := &corev1.EventList{}
	err := TestEnv.Client.List(TestEnv.Ctx, eventList, client.InNamespace(namespace))
	if err != nil {
		t.Logf("⚠️  Error getting events: %v", err)
		return
	}

	if len(eventList.Items) == 0 {
		t.Log("📝 No events found in namespace")
		return
	}

	t.Logf("📝 Found %d events in namespace %s:", len(eventList.Items), namespace)
	for _, event := range eventList.Items {
		t.Logf("  %s: %s (%s) - %s",
			event.LastTimestamp.Format("15:04:05"),
			event.Reason,
			event.Type,
			event.Message)
	}
}

func checkOperatorHealth(t *testing.T) {
	t.Helper()

	operatorNS := "llama-stack-k8s-operator-system"
	podList := &corev1.PodList{}
	err := TestEnv.Client.List(TestEnv.Ctx, podList, client.InNamespace(operatorNS))
	if err != nil {
		t.Logf("⚠️  Error getting operator pods: %v", err)
		return
	}

	for _, pod := range podList.Items {
		if pod.Labels["control-plane"] == "controller-manager" {
			t.Logf("Operator pod health: Phase=%s, Ready=%v", pod.Status.Phase, isPodReady(&pod))
			if len(pod.Status.ContainerStatuses) > 0 {
				container := pod.Status.ContainerStatuses[0]
				t.Logf("  Container: Ready=%v, RestartCount=%d", container.Ready, container.RestartCount)
			}
		}
	}
}
