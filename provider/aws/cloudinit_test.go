package aws

import (
	"strings"
	"testing"
)

func TestStartupScriptUsesStandardCRIOWhenCustomRuntimeDisabled(t *testing.T) {
	script, err := BuildStartupScript(CloudInitParams{
		WGConfig:               "[Interface]\\nPrivateKey = test",
		VpnIP:                  "10.200.0.3",
		JoinCommand:            "kubeadm join 10.200.0.1:6443 --token token",
		KubernetesVersion:      "1.35.8",
		KubernetesMinorVersion: "1.35",
		NodeName:               "worker-001",
	})
	if err != nil {
		t.Fatalf("BuildStartupScript returned an error: %v", err)
	}
	if !strings.Contains(script, "Installing standard CRI-O") {
		t.Fatal("startup script does not install standard CRI-O")
	}
	if strings.Contains(script, "Installing cnlab-runtime") {
		t.Fatal("startup script unexpectedly installs cnlab-runtime")
	}
	for _, legacyPath := range []string{
		`runtime_path = "/usr/bin/runc"`,
		`conmon = "/usr/local/bin/conmon"`,
		`runtime_path = "/usr/local/nvidia/toolkit/nvidia-container-runtime.cdi"`,
	} {
		if strings.Contains(script, legacyPath) {
			t.Fatalf("startup script still configures legacy runtime path %q", legacyPath)
		}
	}
}
