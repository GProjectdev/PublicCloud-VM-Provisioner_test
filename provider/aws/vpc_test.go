package aws

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func TestVPCStartupWithoutVPN(t *testing.T) {
	p := CloudInitParams{
		DirectVPC: true, NodeName: "aws-worker", IsGPUNode: true,
		JoinCommand:       "kubeadm join 10.50.1.53:6443 --token abcdef.0123456789abcdef",
		KubernetesVersion: "1.32.3", KubernetesMinorVersion: "1.32",
	}
	script, err := BuildStartupScript(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"wg-quick", "/etc/wireguard", "apt_install wireguard", "Configuring WireGuard", "provider=OnPrem"} {
		if strings.Contains(script, unwanted) {
			t.Errorf("VPC script contains %q", unwanted)
		}
	}
	for _, wanted := range []string{"latest/api/token", "latest/meta-data/local-ipv4", "--node-ip=", "provider=AWS", "kubeadm join"} {
		if !strings.Contains(script, wanted) {
			t.Errorf("VPC script is missing %q", wanted)
		}
	}
	if bash := os.Getenv("TEST_BASH"); bash != "" {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bash -n: %v: %s", err, out)
		}
	}
	p.DirectVPC = false
	if _, err := BuildStartupScript(p); err == nil {
		t.Fatal("WireGuard must still require VPN settings")
	}
	p.WGConfig, p.VpnIP = "[Interface]\nPrivateKey = test", "10.200.0.3"
	script, err = BuildStartupScript(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "wg-quick") {
		t.Fatal("legacy WireGuard path was removed")
	}
}

func TestVPCLaunchOptions(t *testing.T) {
	np := &mlv1alpha1.NodeProvision{Spec: mlv1alpha1.NodeProvisionSpec{
		NetworkMode: "VPC", Region: "ap-northeast-2", InstanceType: "g5.xlarge",
		AWSConfig: &mlv1alpha1.AWSConfig{AMI: "ami-test", VPCID: "vpc-test", SubnetID: "subnet-test", SecurityGroupIDs: []string{"sg-test"}},
	}}
	if err := ValidateAWSConfig(np.Spec); err != nil {
		t.Fatal(err)
	}
	input := buildRunInstancesInput(np, "userdata")
	if input.MetadataOptions == nil || input.MetadataOptions.HttpTokens != types.HttpTokensStateRequired {
		t.Fatal("VPC bootstrap requires IMDSv2")
	}
	if !awssdk.ToBool(input.NetworkInterfaces[0].AssociatePublicIpAddress) {
		t.Fatal("legacy public IP default changed")
	}
	np.Spec.AWSConfig.AssociatePublicIP = awssdk.Bool(false)
	input = buildRunInstancesInput(np, "userdata")
	if awssdk.ToBool(input.NetworkInterfaces[0].AssociatePublicIpAddress) {
		t.Fatal("private-subnet public IP opt-out ignored")
	}
	np.Spec.AWSConfig.VPCID = ""
	if err := ValidateAWSConfig(np.Spec); err == nil {
		t.Fatal("VPC mode requires explicit VPC")
	}
	np.Spec.NetworkMode = ""
	if err := ValidateAWSConfig(np.Spec); err != nil {
		t.Fatalf("legacy validation changed: %v", err)
	}
}
