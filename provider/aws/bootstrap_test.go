package aws

import (
	"context"
	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestBootstrapRequiresTerminalOwnerProof(t *testing.T) {
	for _, tc := range []string{"terminated", "running", "shutting-down", "wrong-uid", "not-found"} {
		t.Run(tc, func(t *testing.T) {
			np := &api.NodeProvision{ObjectMeta: meta.ObjectMeta{Name: "worker", Namespace: "test", UID: "owner"}, Status: api.NodeProvisionStatus{InstanceID: "i-test"}}
			i := types.Instance{InstanceId: sdk.String("i-test"), State: &types.InstanceState{Name: types.InstanceStateNameTerminated}, Tags: []types.Tag{{Key: sdk.String("node-provision-uid"), Value: sdk.String("owner")}, {Key: sdk.String("node-provision-name"), Value: sdk.String("worker")}, {Key: sdk.String("node-provision-namespace"), Value: sdk.String("test")}}}
			if tc == "running" {
				i.State.Name = types.InstanceStateNameRunning
			}
			if tc == "shutting-down" {
				i.State.Name = types.InstanceStateNameShuttingDown
			}
			if tc == "wrong-uid" {
				i.Tags[0].Value = sdk.String("foreign")
			}
			out := &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{i}}}}
			if tc == "not-found" {
				out.Reservations = nil
			}
			stub := &fenceEC2Stub{out: out}
			ok, e := bootstrapInstanceTerminated(context.Background(), stub, np)
			if ok != (tc == "terminated") {
				t.Fatalf("terminal=%v err=%v", ok, e)
			}
			if (tc == "wrong-uid" || tc == "not-found") && e == nil {
				t.Fatal("missing evidence accepted")
			}
		})
	}
}
func TestBootstrapLaunchTokenStableAndChangesOnlyAfterTerminalReceipt(t *testing.T) {
	np := &api.NodeProvision{ObjectMeta: meta.ObjectMeta{UID: "owner", Annotations: map[string]string{}}, Spec: api.NodeProvisionSpec{AWSConfig: &api.AWSConfig{}}}
	a := buildRunInstancesInput(np, "data")
	b := buildRunInstancesInput(np, "different-refreshed-join-token")
	if sdk.ToString(a.ClientToken) == "" || sdk.ToString(a.ClientToken) != sdk.ToString(b.ClientToken) {
		t.Fatal("launch retry is not idempotent")
	}
	np.Annotations[BootstrapHistoryAnnotation] = "i-dead"
	c := buildRunInstancesInput(np, "data")
	if sdk.ToString(c.ClientToken) == sdk.ToString(a.ClientToken) {
		t.Fatal("terminal bootstrap retry reused old launch")
	}
}
