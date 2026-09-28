package aws

import (
	"context"
	"errors"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type fenceEC2Stub struct {
	out        *ec2.DescribeInstancesOutput
	err        error
	terminated bool
}

func (s *fenceEC2Stub) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return s.out, s.err
}
func (s *fenceEC2Stub) TerminateInstances(_ context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	s.terminated = len(in.InstanceIds) == 1 && in.InstanceIds[0] == "i-source"
	return &ec2.TerminateInstancesOutput{}, nil
}
func TestFencePositiveTerminalEvidence(t *testing.T) {
	for _, state := range []types.InstanceStateName{types.InstanceStateNameRunning, types.InstanceStateNameStopped, types.InstanceStateNameShuttingDown, types.InstanceStateNameTerminated} {
		t.Run(string(state), func(t *testing.T) {
			s := &fenceEC2Stub{out: &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{{InstanceId: awssdk.String("i-source"), State: &types.InstanceState{Name: state}}}}}}}
			err := confirmInstanceFence(context.Background(), s, "i-source")
			if state == types.InstanceStateNameTerminated {
				if err != nil || s.terminated {
					t.Fatalf("terminal receipt: %v", err)
				}
			} else if !errors.Is(err, ErrInstanceTerminationPending) {
				t.Fatalf("nonterminal must wait: %v", err)
			}
			if (state == types.InstanceStateNameRunning || state == types.InstanceStateNameStopped) != s.terminated {
				t.Fatal("unexpected termination request")
			}
		})
	}
	for _, s := range []*fenceEC2Stub{{}, {out: &ec2.DescribeInstancesOutput{}}, {err: errors.New("InvalidInstanceID.NotFound")}} {
		if err := confirmInstanceFence(context.Background(), s, "i-source"); err == nil || s.terminated {
			t.Fatal("absence is not terminal proof")
		}
	}
}
