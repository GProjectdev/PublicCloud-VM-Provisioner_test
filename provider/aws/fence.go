package aws

import (
	"context"
	"fmt"

	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type fenceEC2 interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
}

// FenceInstance requires positive terminal-state evidence. Unlike cleanup,
// NotFound cannot prove fencing: credentials or region may point elsewhere.
func FenceInstance(ctx context.Context, np *mlv1alpha1.NodeProvision, creds AWSCredentials, instanceID string) error {
	if np.Spec.Provider != mlv1alpha1.CloudProviderAWS || np.Spec.NetworkMode != "VPC" ||
		instanceID == "" || instanceID != np.Status.InstanceID {
		return fmt.Errorf("invalid AWS VPC fence identity")
	}
	c, err := newEC2Client(ctx, np.Spec.Region, creds)
	if err != nil {
		return err
	}
	return confirmInstanceFence(ctx, c, instanceID)
}

func confirmInstanceFence(ctx context.Context, c fenceEC2, instanceID string) error {
	out, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return fmt.Errorf("describe fence instance: %w", err)
	}
	if out != nil {
		for _, reservation := range out.Reservations {
			for _, instance := range reservation.Instances {
				if awssdk.ToString(instance.InstanceId) != instanceID || instance.State == nil {
					continue
				}
				if instance.State.Name == types.InstanceStateNameTerminated {
					return nil
				}
				if instance.State.Name != types.InstanceStateNameShuttingDown {
					if _, err := c.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{instanceID}}); err != nil {
						return fmt.Errorf("terminate fence instance: %w", err)
					}
				}
				return ErrInstanceTerminationPending
			}
		}
	}
	return fmt.Errorf("no positive terminal-state evidence for %s", instanceID)
}
