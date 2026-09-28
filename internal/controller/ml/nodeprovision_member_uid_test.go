package ml

import (
	"context"
	"testing"

	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestStatusRecordsExactLocalMemberUID(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mlv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	np := &mlv1alpha1.NodeProvision{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "member-uid", Generation: 1},
		Status:     mlv1alpha1.NodeProvisionStatus{MemberUID: "incorrect-origin-uid"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(np).WithObjects(np).Build()
	ctx := context.Background()
	if err := c.Get(ctx, client.ObjectKeyFromObject(np), np); err != nil {
		t.Fatal(err)
	}
	r := &NodeProvisionReconciler{Client: c}
	if err := r.updateNodeProvisionStatus(ctx, np); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(np), np); err != nil {
		t.Fatal(err)
	}
	if np.Status.MemberUID != string(np.UID) {
		t.Fatalf("member identity mismatch: %q != %q", np.Status.MemberUID, np.UID)
	}
}
