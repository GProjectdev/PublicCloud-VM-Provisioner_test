package ml

import (
	"context"
	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
	"time"
)

func TestFailedProvisionPreservesCapacityErrorDuringStatusWatchCooldown(t *testing.T) {
	for _, mode := range []string{"recent", "missing-timestamp", "expired"} {
		t.Run(mode, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = mlv1alpha1.AddToScheme(scheme)
			at := metav1.NewTime(time.Now().Add(-5 * time.Second))
			if mode == "expired" {
				at = metav1.NewTime(time.Now().Add(-2 * requeueFailed))
			}
			np := &mlv1alpha1.NodeProvision{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "case", UID: "exact", Finalizers: []string{nodeProvisionFinalizer}}, Spec: mlv1alpha1.NodeProvisionSpec{Provider: mlv1alpha1.CloudProviderAWS, NetworkMode: "VPC"}, Status: mlv1alpha1.NodeProvisionStatus{Phase: mlv1alpha1.NodeProvisionPhaseFailed, Message: "InsufficientInstanceCapacity", LastUpdated: &at}}
			if mode == "missing-timestamp" {
				np.Status.LastUpdated = nil
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(np).WithObjects(np).Build()
			r := &NodeProvisionReconciler{Client: c, Scheme: scheme}
			ctx := context.Background()
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(np)}
			result, err := r.Reconcile(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			got := &mlv1alpha1.NodeProvision{}
			if err := c.Get(ctx, req.NamespacedName, got); err != nil {
				t.Fatal(err)
			}
			if mode == "expired" {
				if got.Status.Phase != "" || got.Status.Message != "Retrying after failure" {
					t.Fatalf("retry not released after persisted cooldown: %+v", got.Status)
				}
				return
			}
			if got.Status.Phase != mlv1alpha1.NodeProvisionPhaseFailed || got.Status.Message != "InsufficientInstanceCapacity" || got.Status.LastUpdated == nil || result.RequeueAfter < requeueFailed-10*time.Second {
				t.Fatalf("failure lost or cooldown skipped: %+v %+v", got.Status, result)
			}
			// An immediate subsequent status-watch event must not reset the timestamp,
			// erase the failure or enter provisioning. No AWS client is configured.
			before := got.DeepCopy()
			result, err = r.Reconcile(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, req.NamespacedName, got); err != nil {
				t.Fatal(err)
			}
			if got.ResourceVersion != before.ResourceVersion || !got.Status.LastUpdated.Equal(before.Status.LastUpdated) || result.RequeueAfter <= 0 {
				t.Fatal("status-watch hot loop or unbounded cooldown refresh")
			}
		})
	}
}
