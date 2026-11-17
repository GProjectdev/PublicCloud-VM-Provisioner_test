package ml

import (
	"context"
	ml "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"strings"
	"testing"
)

func TestPrepullJobNameFitsLabelAndDistinguishesVMUIDs(t *testing.T) {
	np := &ml.NodeProvision{ObjectMeta: meta.ObjectMeta{Name: strings.Repeat("n", 63), UID: "uid-a"}}
	first := nodeProvisionPrepullJobName(np)
	if len(first) > 63 || len(validation.IsDNS1123Label(first)) != 0 || first != nodeProvisionPrepullJobName(np) {
		t.Fatal(first)
	}
	np.UID = "uid-b"
	if first == nodeProvisionPrepullJobName(np) {
		t.Fatal("recreated VM reused long-name Job")
	}
	np.Name = "short"
	if nodeProvisionPrepullJobName(np) != "short-prepull" {
		t.Fatal("short-name compatibility lost")
	}
}
func TestPrepullRejectsForeignCompletedJob(t *testing.T) {
	np := &ml.NodeProvision{ObjectMeta: meta.ObjectMeta{Name: "worker", Namespace: "test", UID: types.UID("current")}, Status: ml.NodeProvisionStatus{Phase: ml.NodeProvisionPhasePrePullingImages}}
	controller := true
	job := &batch.Job{ObjectMeta: meta.ObjectMeta{Name: nodeProvisionPrepullJobName(np), Namespace: np.Namespace, OwnerReferences: []meta.OwnerReference{{APIVersion: ml.GroupVersion.String(), Kind: "NodeProvision", Name: np.Name, UID: "previous", Controller: &controller}}}, Status: batch.JobStatus{Conditions: []batch.JobCondition{{Type: batch.JobComplete, Status: core.ConditionTrue}}}}
	scheme := runtime.NewScheme()
	_ = ml.AddToScheme(scheme)
	_ = batch.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(np, job).Build()
	r := &NodeProvisionReconciler{Client: c}
	_, err := r.reconcileImagePrepullJob(context.Background(), np, &ml.NodeProvisionNetConfig{}, nil)
	if err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("foreign completion accepted: %v", err)
	}
	got := &ml.NodeProvision{}
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(np), got)
	if got.Status.Phase != ml.NodeProvisionPhasePrePullingImages {
		t.Fatal("foreign job marked Ready")
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(job), &batch.Job{}); err != nil {
		t.Fatal("foreign job deleted", err)
	}
}
