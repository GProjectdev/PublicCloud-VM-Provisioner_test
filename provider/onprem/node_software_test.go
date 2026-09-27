package onprem

import (
	"context"
	"strings"
	"testing"

	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	pkgruntime "dcn.ssu.ac.kr/infra/pkg/runtime"
)

func TestLegacyOnPremRejectsNodeSoftwareBeforeAnySSHOrVPN(t *testing.T) {
	cfg := &api.NodeProvisionNetConfig{}
	cfg.Spec.SoftwareConfig.NodeSoftware = &api.NodeSoftwareConfig{NFSClient: true}
	_, _, err := NewInClusterProvisioner(context.Background(), &api.NodeProvision{}, nil, nil, nil, cfg, nil, pkgruntime.Config{})
	if err == nil || !strings.Contains(err.Error(), "AWS/GCP") {
		t.Fatalf("expected explicit unsupported error before SSH: %v", err)
	}
}
