package aws

import (
	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	"testing"
)

func TestRunInstancesCarriesResolvedKey(t *testing.T) {
	for _, name := range []string{"worker-00", "worker-00-replacement"} {
		np := &api.NodeProvision{}
		np.Name = name
		np.Spec.AWSConfig = &api.AWSConfig{KeyPairName: "nodeprovision-" + name}
		input := buildRunInstancesInput(np, "userdata")
		if input.KeyName == nil || *input.KeyName != np.Spec.AWSConfig.KeyPairName {
			t.Fatal("EC2 request lost SSH key")
		}
	}
}
