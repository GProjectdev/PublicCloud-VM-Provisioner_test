package ml

import (
	"context"
	"errors"
	"testing"

	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	awsprovision "dcn.ssu.ac.kr/infra/provider/aws"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestFenceRequiresProviderConfirmation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mlv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	np := &mlv1alpha1.NodeProvision{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "np-uid", Generation: 2},
		Spec: mlv1alpha1.NodeProvisionSpec{Provider: mlv1alpha1.CloudProviderAWS, NetworkMode: "VPC",
			Fence: &mlv1alpha1.NodeProvisionFenceRequest{OperationUID: "recovery-uid", InstanceID: "i-old"}},
		Status: mlv1alpha1.NodeProvisionStatus{InstanceID: "i-old", Phase: mlv1alpha1.NodeProvisionPhaseReady},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(np).WithObjects(np).Build()
	calls := 0
	providerErr := awsprovision.ErrInstanceTerminationPending
	r := &NodeProvisionReconciler{Client: c, FenceInstance: func(_ context.Context, got *mlv1alpha1.NodeProvision) error {
		calls++
		if got.Status.InstanceID != "i-old" || got.Spec.Fence.InstanceID != "i-old" {
			t.Fatal("wrong instance")
		}
		return providerErr
	}}
	reconcile := func(wantError bool) {
		t.Helper()
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(np)})
		if (err != nil) != wantError {
			t.Fatalf("error=%v, wantError=%v", err, wantError)
		}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(np), np); err != nil {
			t.Fatal(err)
		}
	}
	reconcile(false)
	if calls != 0 || np.Status.Fence != nil || len(np.Finalizers) != 1 || np.Finalizers[0] != nodeProvisionFinalizer {
		t.Fatal("finalizer must precede fence intent and provider action")
	}
	stale := np.DeepCopy()
	reconcile(false)
	if calls != 0 || np.Status.Fence.Phase != "Fencing" {
		t.Fatal("intent must precede provider action")
	}
	if err := r.updateNodeProvisionStatus(context.Background(), stale); !apierrors.IsConflict(err) {
		t.Fatalf("stale writer must not erase fence intent: %v", err)
	}
	reconcile(false)
	if np.Status.Fence.Phase == "Fenced" {
		t.Fatal("pending termination is not a fence")
	}
	providerErr = errors.New("provider unavailable")
	reconcile(true)
	if np.Status.Fence.Phase == "Fenced" {
		t.Fatal("provider failure is not a fence")
	}
	providerErr = nil
	reconcile(false)
	if np.Status.Fence.Phase != "Fenced" || np.Status.Fence.ObservedGeneration != 2 || np.Status.Phase == mlv1alpha1.NodeProvisionPhaseReady {
		t.Fatalf("bad receipt: %+v", np.Status)
	}
	before := calls
	reconcile(false)
	if calls != before {
		t.Fatal("confirmed fence repeated provider action")
	}
	np.Spec.Fence = nil
	if err := c.Update(context.Background(), np); err != nil {
		t.Fatal(err)
	}
	reconcile(true)
	if calls != before {
		t.Fatal("removed fence must not provision or terminate")
	}
}

func TestFenceRejectsWrongIdentityBeforeProviderAction(t *testing.T) {
	for _, mutate := range []func(*mlv1alpha1.NodeProvision){
		func(n *mlv1alpha1.NodeProvision) { n.Spec.Fence.InstanceID = "i-other" },
		func(n *mlv1alpha1.NodeProvision) { n.Spec.Fence.OperationUID = "" },
		func(n *mlv1alpha1.NodeProvision) { n.Spec.NetworkMode = "VPN" },
		func(n *mlv1alpha1.NodeProvision) { n.Spec.Provider = mlv1alpha1.CloudProviderGCP },
	} {
		scheme := runtime.NewScheme()
		_ = mlv1alpha1.AddToScheme(scheme)
		np := &mlv1alpha1.NodeProvision{
			ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "np-uid", Generation: 1, Finalizers: []string{nodeProvisionFinalizer}},
			Spec:       mlv1alpha1.NodeProvisionSpec{Provider: mlv1alpha1.CloudProviderAWS, NetworkMode: "VPC", Fence: &mlv1alpha1.NodeProvisionFenceRequest{OperationUID: "operation", InstanceID: "i-old"}},
			Status:     mlv1alpha1.NodeProvisionStatus{InstanceID: "i-old"},
		}
		mutate(np)
		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(np).WithObjects(np).Build()
		r := &NodeProvisionReconciler{Client: c, FenceInstance: func(context.Context, *mlv1alpha1.NodeProvision) error {
			t.Fatal("invalid fence reached provider")
			return nil
		}}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(np)}); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(np), np); err != nil {
			t.Fatal(err)
		}
		if np.Status.Fence.Phase != "Rejected" {
			t.Fatal("invalid fence was accepted")
		}
	}
}
