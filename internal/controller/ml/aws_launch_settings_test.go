package ml

import (
	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	"testing"
)

func TestEffectiveSSHPort(t *testing.T) {
	for _, tc := range []struct {
		input, want int
		invalid     bool
	}{
		{0, 22, false}, {22, 22, false}, {2222, 2222, false}, {-1, 0, true}, {65536, 0, true},
	} {
		got, err := effectiveSSHPort(tc.input)
		if got != tc.want || (err != nil) != tc.invalid {
			t.Fatalf("port %d: got %d, %v", tc.input, got, err)
		}
	}
}

func TestAWSLaunchSnapshotSurvivesSpecReplacement(t *testing.T) {
	for _, name := range []string{"worker-00", "worker-00-replacement"} {
		t.Run(name, func(t *testing.T) {
			np := &api.NodeProvision{}
			np.Name = name
			np.Spec.AWSConfig = &api.AWSConfig{KeyPairName: "nodeprovision-" + name}
			snapshot, err := awsLaunchSnapshot(np)
			if err != nil {
				t.Fatal(err)
			}
			np.Spec.AWSConfig.KeyPairName = ""
			if snapshot.Spec.AWSConfig.KeyPairName != "nodeprovision-"+name {
				t.Fatal("launch key lost after spec refresh")
			}
		})
	}
}

func TestAWSLaunchSnapshotRejectsMissingKey(t *testing.T) {
	for _, cfg := range []*api.AWSConfig{nil, {}, {KeyPairName: " "}} {
		np := &api.NodeProvision{}
		np.Spec.AWSConfig = cfg
		if _, err := awsLaunchSnapshot(np); err == nil {
			t.Fatal("allowed launch without key")
		}
	}
}
