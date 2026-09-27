package ml

import (
	"context"
	"crypto/x509"
	"testing"

	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestVPCNoVPNDependency(t *testing.T) {
	r := &NodeProvisionReconciler{}
	np := &mlv1alpha1.NodeProvision{Spec: mlv1alpha1.NodeProvisionSpec{NetworkMode: "VPC"}}
	// No Kubernetes client or VPN credentials: this must return before accessing either.
	if err := r.cleanupVPNPeer(context.Background(), np); err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolveAWSDefaults(context.Background(), np, nil); err == nil {
		t.Fatal("missing explicit VPC network must fail before accessing AWS")
	}
	if _, err := r.getSSHClientByProvider(context.Background(), np); err == nil {
		t.Fatal("missing private IP must fail before accessing SSH")
	}
}

func TestVPCJoiningWaitsForReady(t *testing.T) {
	for _, ready := range []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionTrue} {
		t.Run(string(ready), func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := mlv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			np := &mlv1alpha1.NodeProvision{
				ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default", UID: "test-worker"},
				Spec:       mlv1alpha1.NodeProvisionSpec{Provider: mlv1alpha1.CloudProviderAWS, NetworkMode: "VPC", NodeLabel: "cpu"},
				Status:     mlv1alpha1.NodeProvisionStatus{IPAddress: "10.50.1.99", PrivateIP: "10.50.1.99"},
			}
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "worker"},
				Status: corev1.NodeStatus{
					Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.50.1.99"}},
					Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}},
				},
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(np).WithObjects(np, node).Build()
			r := &NodeProvisionReconciler{Client: c, Scheme: scheme}
			result, err := r.reconcileJoining(context.Background(), np)
			if err != nil {
				t.Fatal(err)
			}
			fresh := &mlv1alpha1.NodeProvision{}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(np), fresh); err != nil {
				t.Fatal(err)
			}
			if ready == corev1.ConditionTrue {
				if fresh.Status.Phase != mlv1alpha1.NodeProvisionPhaseReady {
					t.Fatalf("phase: %s", fresh.Status.Phase)
				}
			} else if fresh.Status.Phase != mlv1alpha1.NodeProvisionPhaseVerifyingHealth || result.RequeueAfter == 0 {
				t.Fatalf("unhealthy node marked complete: %+v", fresh.Status)
			}
		})
	}
}

func TestVPCJoiningLabelsStatefulMigrationArtifactNode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := mlv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	np := &mlv1alpha1.NodeProvision{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default", UID: "test-worker"},
		Spec:       mlv1alpha1.NodeProvisionSpec{Provider: mlv1alpha1.CloudProviderAWS, NetworkMode: "VPC", NodeLabel: "cpu"},
		Status:     mlv1alpha1.NodeProvisionStatus{IPAddress: "10.50.1.99", PrivateIP: "10.50.1.99", InstanceID: "i-0123456789abcdef0"},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Status: corev1.NodeStatus{
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.50.1.99"}},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	netConfig := &mlv1alpha1.NodeProvisionNetConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-vpc-netconfig", Namespace: "default"},
		Spec:       mlv1alpha1.NodeProvisionNetConfigSpec{SoftwareConfig: mlv1alpha1.SoftwareConfig{NodeSoftware: &mlv1alpha1.NodeSoftwareConfig{RuntimeProfile: "StatefulMigration"}}},
	}
	caSource := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "serving-ca", Namespace: "kube-system"}, Data: map[string]string{"ca.crt": string(newTestCAPEM(t))}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(np).WithObjects(np, node, netConfig, caSource).Build()
	r := &NodeProvisionReconciler{
		Client:           c,
		Scheme:           scheme,
		KubeletServingCA: KubeletServingCAConfig{SourceNamespace: "kube-system", SourceName: "serving-ca"},
		KubeletServingTLSChecker: kubeletServingTLSCheckerFunc(func(context.Context, string, string, *x509.CertPool) error {
			return nil
		}),
	}
	if _, err := r.reconcileJoining(context.Background(), np); err != nil {
		t.Fatal(err)
	}
	got := &corev1.Node{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "worker"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Labels["artifact-node"] != "true" {
		t.Fatalf("StatefulMigration node missing artifact-node label: %#v", got.Labels)
	}
	if got.Labels["restore-from-file"] == "true" {
		t.Fatalf("restore-from-file must not be asserted by NodeReady/runtime package verification alone: %#v", got.Labels)
	}
}
func TestVPCNetConfigNamespaceIsolation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mlv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	nc := &mlv1alpha1.NodeProvisionNetConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "member"},
		Status:     mlv1alpha1.NodeProvisionNetConfigStatus{ClusterJoinCommand: "test", JoinTokenRefreshedAt: &now},
	}
	other := nc.DeepCopy()
	other.Namespace = "other"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nc, other).Build()
	r := &NodeProvisionReconciler{Client: c}
	np := &mlv1alpha1.NodeProvision{ObjectMeta: metav1.ObjectMeta{Namespace: "member"}, Spec: mlv1alpha1.NodeProvisionSpec{NetworkMode: "VPC"}}
	got, err := r.requireNetConfig(context.Background(), np)
	if err != nil || got.Namespace != "member" {
		t.Fatalf("namespace selection: %v, %v", got, err)
	}
	nc2 := nc.DeepCopy()
	nc2.Name, nc2.ResourceVersion = "duplicate", ""
	if err := c.Create(context.Background(), nc2); err != nil {
		t.Fatal(err)
	}
	if _, err := r.requireNetConfig(context.Background(), np); err == nil {
		t.Fatal("ambiguous config accepted")
	}
	np.Namespace = "empty"
	if _, err := r.requireNetConfig(context.Background(), np); err == nil {
		t.Fatal("config from another namespace accepted")
	}
}

func TestVPCDeleteRejectsUnrelatedNode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "existing-master"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
	r := &NodeProvisionReconciler{Client: c}
	np := &mlv1alpha1.NodeProvision{
		ObjectMeta: metav1.ObjectMeta{Name: "existing-master"},
		Spec:       mlv1alpha1.NodeProvisionSpec{NetworkMode: "VPC"},
	}
	if _, err := r.deleteProvisionedNode(context.Background(), np); err == nil {
		t.Fatal("unowned Node without matching private IP must not be deleted")
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(node), &corev1.Node{}); err != nil {
		t.Fatalf("unrelated Node was removed: %v", err)
	}
}

func TestVPCDeleteStopsCloudBeforeRemovingNode(t *testing.T) {
	for _, instanceID := range []string{"i-still-running", ""} {
		t.Run("instance="+instanceID, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := mlv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			np := &mlv1alpha1.NodeProvision{
				ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default", Finalizers: []string{nodeProvisionFinalizer}},
				Spec:       mlv1alpha1.NodeProvisionSpec{Provider: mlv1alpha1.CloudProviderAWS, NetworkMode: "VPC"},
				Status:     mlv1alpha1.NodeProvisionStatus{InstanceID: instanceID},
			}
			np.Status.PrivateIP = "10.50.1.99"
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "worker", Finalizers: []string{nodeProvisionNodeFinalizer}},
				Status:     corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.50.1.99"}}},
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(np).WithObjects(np, node).Build()
			r := &NodeProvisionReconciler{Client: c}
			result, err := r.handleDelete(context.Background(), np)
			getErr := c.Get(context.Background(), client.ObjectKeyFromObject(node), &corev1.Node{})
			if instanceID != "" {
				// Missing credentials must fail cloud cleanup without touching the live Node.
				if err == nil || getErr != nil {
					t.Fatalf("live node removed before cloud cleanup: %v, %v", err, getErr)
				}
				return
			}
			if err != nil || getErr == nil || result.RequeueAfter == 0 {
				t.Fatalf("terminated instance node not removed: result=%v, err=%v, get=%v", result, err, getErr)
			}
			if _, err := r.handleDelete(context.Background(), np); err != nil {
				t.Fatal(err)
			}
			fresh := &mlv1alpha1.NodeProvision{}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(np), fresh); err != nil {
				t.Fatal(err)
			}
			for _, finalizer := range fresh.Finalizers {
				if finalizer == nodeProvisionFinalizer {
					t.Fatal("cleanup finalizer remains")
				}
			}
		})
	}
}
