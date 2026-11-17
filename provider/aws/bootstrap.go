package aws

import (
	"context"
	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	"fmt"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const BootstrapHistoryAnnotation = "ml.dcn.ssu.ac.kr/bootstrap-terminated-instances"

type bootstrapEC2 interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

// Positive terminal state and immutable owner identity are mandatory. NotFound
// and a missing/mismatched UID never authorize forgetting a provider instance.
func BootstrapInstanceTerminated(ctx context.Context, np *api.NodeProvision, creds AWSCredentials) (bool, error) {
	c, e := newEC2Client(ctx, np.Spec.Region, creds)
	if e != nil {
		return false, e
	}
	return bootstrapInstanceTerminated(ctx, c, np)
}
func bootstrapInstanceTerminated(ctx context.Context, c bootstrapEC2, np *api.NodeProvision) (bool, error) {
	out, e := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{np.Status.InstanceID}})
	if e != nil {
		return false, e
	}
	if out != nil {
		for _, r := range out.Reservations {
			for _, i := range r.Instances {
				if sdk.ToString(i.InstanceId) != np.Status.InstanceID || i.State == nil {
					continue
				}
				tags := map[string]string{}
				for _, t := range i.Tags {
					tags[sdk.ToString(t.Key)] = sdk.ToString(t.Value)
				}
				if np.UID == "" || tags["node-provision-uid"] != string(np.UID) || tags["node-provision-name"] != np.Name || tags["node-provision-namespace"] != np.Namespace {
					return false, fmt.Errorf("bootstrap instance owner identity mismatch")
				}
				return i.State.Name == types.InstanceStateNameTerminated, nil
			}
		}
	}
	return false, fmt.Errorf("no positive bootstrap instance evidence")
}
