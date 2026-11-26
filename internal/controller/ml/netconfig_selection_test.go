package ml

import (
	"context"
	"testing"

	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestExplicitWireGuardNetConfigDoesNotSelectFirstGlobalEntry(t *testing.T) {
	s := runtime.NewScheme()
	_ = api.AddToScheme(s)
	now := metav1.Now()
	old := &api.NodeProvisionNetConfig{ObjectMeta: metav1.ObjectMeta{Name: "aaa-old", Namespace: "old", UID: "old-uid"}}
	want := &api.NodeProvisionNetConfig{ObjectMeta: metav1.ObjectMeta{Name: "member1-verification", Namespace: "verify", UID: "wanted-uid"}, Status: api.NodeProvisionNetConfigStatus{ClusterJoinCommand: "opaque-test-join", JoinTokenRefreshedAt: &now}}
	np := &api.NodeProvision{ObjectMeta: metav1.ObjectMeta{Name: "gpu", Namespace: "verify", UID: "np-uid"}, Spec: api.NodeProvisionSpec{NetworkMode: "WireGuard", NetConfigName: want.Name}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(old, want, np).Build()
	r := &NodeProvisionReconciler{Client: c}
	got, err := r.requireNetConfig(context.Background(), np)
	if err != nil || got.UID != want.UID {
		t.Fatalf("wrong selection: %v %v", got, err)
	}
	changed, err := r.pinNetConfig(context.Background(), np)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	if err = c.Get(context.Background(), client.ObjectKeyFromObject(np), np); err != nil {
		t.Fatal(err)
	}
	if np.Annotations[selectedNetConfigUID] != string(want.UID) {
		t.Fatal("UID not pinned")
	}
	if changed, err = r.pinNetConfig(context.Background(), np); changed || err != nil {
		t.Fatal("pin is not idempotent")
	}
	np.Spec.NetConfigName = "aaa-old"
	if _, err = r.selectNetConfig(context.Background(), np); err == nil {
		t.Fatal("cross-namespace fallback accepted")
	}
	np.Spec.NetConfigName = want.Name
	np.Annotations[selectedNetConfigUID] = "previous-uid"
	if _, err = r.selectNetConfig(context.Background(), np); err == nil {
		t.Fatal("recreated config accepted")
	}
}

func TestLegacyWireGuardRequiresSingleConfig(t *testing.T) {
	s := runtime.NewScheme()
	_ = api.AddToScheme(s)
	np := &api.NodeProvision{ObjectMeta: metav1.ObjectMeta{Namespace: "work"}, Spec: api.NodeProvisionSpec{NetworkMode: "WireGuard"}}
	one := &api.NodeProvisionNetConfig{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "shared"}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(one).Build()
	r := &NodeProvisionReconciler{Client: c}
	if _, err := r.selectNetConfig(context.Background(), np); err != nil {
		t.Fatal(err)
	}
	two := one.DeepCopy()
	two.Name = "two"
	two.ResourceVersion = ""
	if err := c.Create(context.Background(), two); err != nil {
		t.Fatal(err)
	}
	if _, err := r.selectNetConfig(context.Background(), np); err == nil {
		t.Fatal("ambiguous global config accepted")
	}
}

func TestVPNCleanupFailureRetainsFinalizer(t *testing.T) {
	s := runtime.NewScheme()
	_ = api.AddToScheme(s)
	np := &api.NodeProvision{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "verify", Finalizers: []string{nodeProvisionFinalizer}}, Spec: api.NodeProvisionSpec{NetworkMode: "WireGuard", NetConfigName: "missing"}}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(np).WithObjects(np).Build()
	r := &NodeProvisionReconciler{Client: c}
	if _, err := r.handleDelete(context.Background(), np); err == nil {
		t.Fatal("unverified VPN cleanup reported success")
	}
	fresh := &api.NodeProvision{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(np), fresh); err != nil {
		t.Fatal(err)
	}
	if len(fresh.Finalizers) != 1 || fresh.Finalizers[0] != nodeProvisionFinalizer {
		t.Fatal("cleanup evidence lost")
	}
}
