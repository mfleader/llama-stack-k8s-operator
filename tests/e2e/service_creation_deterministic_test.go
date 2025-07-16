//nolint:testpackage
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/llamastack/llama-stack-k8s-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestServiceCreationDeterministic focuses specifically on Service creation
// via the operator to understand why Services aren't being created in e2e.
func TestServiceCreationDeterministic(t *testing.T) {
	t.Log("=== Deterministic Service Creation E2E Test ===")

	// Create isolated test namespace
	testNS := "service-creation-test-" + randomString(5)
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNS,
		},
	}

	err := TestEnv.Client.Create(TestEnv.Ctx, ns)
	require.NoError(t, err)

	defer func() {
		_ = TestEnv.Client.Delete(TestEnv.Ctx, ns)
	}()

	t.Logf("Created test namespace: %s", testNS)

	// Step 1: Verify operator is running
	verifyOperatorRunning(t)

	// Step 2: Create minimal LlamaStackDistribution
	llsd := createMinimalLlamaStackDistribution(t, testNS)

	// Step 3: Monitor reconciliation and Service creation
	monitorServiceCreation(t, testNS, llsd.Name)
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

// createMinimalLlamaStackDistribution creates a minimal CR for testing.
func createMinimalLlamaStackDistribution(t *testing.T, namespace string) *v1alpha1.LlamaStackDistribution {
	t.Helper()
	t.Log("=== Creating LlamaStackDistribution ===")

	llsd := &v1alpha1.LlamaStackDistribution{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-distribution",
			Namespace: namespace,
		},
		Spec: v1alpha1.LlamaStackDistributionSpec{
			Replicas: 1,
			Server: v1alpha1.ServerSpec{
				Distribution: v1alpha1.DistributionType{
					Name: "ollama",
				},
				ContainerSpec: v1alpha1.ContainerSpec{
					Name: v1alpha1.DefaultContainerName,
					Port: v1alpha1.DefaultServerPort,
					// Add environment variables to ensure HasPorts() returns true
					Env: []corev1.EnvVar{
						{
							Name:  "INFERENCE_MODEL",
							Value: "llama3.2:1b",
						},
						{
							Name:  "OLLAMA_URL",
							Value: "http://ollama-server-service.ollama-dist.svc.cluster.local:11434",
						},
					},
				},
			},
		},
	}

	t.Logf("Creating LlamaStackDistribution: %s/%s", namespace, llsd.Name)
	t.Logf("HasPorts() would return: %v", hasPortsWouldReturn(llsd))

	err := TestEnv.Client.Create(TestEnv.Ctx, llsd)
	require.NoError(t, err)

	t.Log("✅ LlamaStackDistribution created successfully")
	return llsd
}

// hasPortsWouldReturn simulates the HasPorts() method logic.
func hasPortsWouldReturn(llsd *v1alpha1.LlamaStackDistribution) bool {
	containerSpec := llsd.Spec.Server.ContainerSpec
	return containerSpec.Port != 0 || len(containerSpec.Env) > 0
}

// monitorServiceCreation watches for Service creation and provides detailed debugging.
func monitorServiceCreation(t *testing.T, namespace, distributionName string) {
	t.Helper()
	t.Log("=== Monitoring Service Creation ===")

	serviceName := distributionName + "-service"
	serviceKey := client.ObjectKey{Namespace: namespace, Name: serviceName}

	// Track timing
	startTime := time.Now()
	checkInterval := 5 * time.Second
	maxWaitTime := 2 * time.Minute // Shorter timeout for focused test

	t.Logf("Monitoring for Service: %s/%s", namespace, serviceName)
	t.Logf("Check interval: %v, Max wait: %v", checkInterval, maxWaitTime)

	// Monitor both the LlamaStackDistribution and Service creation
	ctx, cancel := context.WithTimeout(TestEnv.Ctx, maxWaitTime)
	defer cancel()

	attempt := 0
	err := wait.PollUntilContextTimeout(ctx, checkInterval, maxWaitTime, true, func(ctx context.Context) (bool, error) {
		attempt++
		elapsed := time.Since(startTime)

		t.Logf("\n--- Check %d (after %v) ---", attempt, elapsed.Truncate(time.Second))

		// 1. Check LlamaStackDistribution status
		checkLlamaStackDistributionStatus(t, namespace, distributionName)

		// 2. Check if Service exists
		service := &corev1.Service{}
		err := TestEnv.Client.Get(ctx, serviceKey, service)
		if err == nil {
			t.Logf("✅ SERVICE FOUND after %v!", elapsed.Truncate(time.Millisecond))
			logServiceDetails(t, service)
			return true, nil
		}

		if !k8serrors.IsNotFound(err) {
			t.Logf("❌ Unexpected error getting Service: %v", err)
			return false, err
		}

		t.Logf("❌ Service %s not found (attempt %d)", serviceName, attempt)

		// 3. Check events in the namespace
		if attempt%2 == 0 { // Every other attempt to reduce noise
			checkNamespaceEvents(t, namespace)
		}

		// 4. Check operator logs periodically
		if attempt%3 == 0 { // Every third attempt
			checkOperatorLogs(t)
		}

		return false, nil
	})

	if err != nil {
		t.Logf("❌ Service creation monitoring failed: %v", err)

		// Final diagnostic information
		t.Log("\n=== FINAL DIAGNOSTICS ===")
		checkLlamaStackDistributionStatus(t, namespace, distributionName)
		checkNamespaceEvents(t, namespace)
		checkOperatorLogs(t)
		listAllServicesInNamespace(t, namespace)

		require.NoError(t, err, "Service should be created within timeout")
	}
}

// checkLlamaStackDistributionStatus checks the status of the LlamaStackDistribution.
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

// checkNamespaceEvents looks for relevant events in the test namespace.
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

// checkOperatorLogs attempts to get recent operator logs.
func checkOperatorLogs(t *testing.T) {
	t.Helper()

	// Note: In e2e tests, we typically can't easily get logs via the client API
	// This would require kubectl or additional tooling
	t.Log("🔍 Operator logs check would require kubectl integration")

	// Instead, we can check if the operator pod is still healthy
	operatorNS := "llama-stack-k8s-operator-system"
	podList := &corev1.PodList{}
	err := TestEnv.Client.List(TestEnv.Ctx, podList, client.InNamespace(operatorNS))
	if err != nil {
		t.Logf("⚠️  Error getting operator pods: %v", err)
		return
	}

	for _, pod := range podList.Items {
		if pod.Labels["control-plane"] == "controller-manager" {
			t.Logf("Operator pod status: Phase=%s, Ready=%v", pod.Status.Phase, isPodReady(&pod))
			if len(pod.Status.ContainerStatuses) > 0 {
				container := pod.Status.ContainerStatuses[0]
				t.Logf("  Container: Ready=%v, RestartCount=%d", container.Ready, container.RestartCount)
			}
		}
	}
}

// logServiceDetails logs detailed information about a found Service.
func logServiceDetails(t *testing.T, service *corev1.Service) {
	t.Helper()

	t.Log("Service details:")
	t.Logf("  Name: %s", service.Name)
	t.Logf("  Namespace: %s", service.Namespace)
	t.Logf("  Type: %s", service.Spec.Type)
	t.Logf("  Ports: %+v", service.Spec.Ports)
	t.Logf("  Selector: %+v", service.Spec.Selector)
	t.Logf("  Labels: %+v", service.Labels)
	t.Logf("  CreationTimestamp: %s", service.CreationTimestamp.Format("15:04:05"))
}

// listAllServicesInNamespace lists all services in the namespace for debugging.
func listAllServicesInNamespace(t *testing.T, namespace string) {
	t.Helper()

	serviceList := &corev1.ServiceList{}
	err := TestEnv.Client.List(TestEnv.Ctx, serviceList, client.InNamespace(namespace))
	if err != nil {
		t.Logf("⚠️  Error listing services: %v", err)
		return
	}

	if len(serviceList.Items) == 0 {
		t.Logf("📋 No services found in namespace %s", namespace)
		return
	}

	t.Logf("📋 All services in namespace %s:", namespace)
	for _, svc := range serviceList.Items {
		t.Logf("  - %s (type: %s, ports: %d)", svc.Name, svc.Spec.Type, len(svc.Spec.Ports))
	}
}
