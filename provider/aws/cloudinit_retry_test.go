package aws

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPackageIndexRetryBehavior(t *testing.T) {
	script, err := BuildStartupScript(CloudInitParams{WGConfig: "[Interface]\nPrivateKey=test", VpnIP: "10.50.1.20", JoinCommand: "kubeadm join test:6443 --token test", KubernetesVersion: "1.35.8", KubernetesMinorVersion: "1.35", NodeName: "test-worker"})
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(script, "apt_update() {")
	if start < 0 {
		t.Fatal("helper missing")
	}
	end := strings.Index(script[start:], "\napt_install() {")
	if end < 0 {
		t.Fatal("helper end missing")
	}
	helper := script[start : start+end]
	for _, tc := range []struct {
		name, stub, want string
		success          bool
	}{
		{"mirror recovers", "apt-get() { count=$((count+1)); [ \"$count\" -ge 3 ]; }", "calls=3 sleeps=2", true},
		{"persistent repository error", "apt-get() { count=$((count+1)); return 100; }", "calls=5 sleeps=4", false},
		{"first refresh succeeds", "apt-get() { count=$((count+1)); return 0; }", "calls=1 sleeps=0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "set -eu\ncount=0; sleeps=0\nreport() { :; }\nsleep() { sleeps=$((sleeps+1)); }\n" + tc.stub + "\n" + helper + "\nif apt_update; then code=0; else code=$?; fi\necho calls=$count sleeps=$sleeps\nexit $code\n"
			out, e := exec.Command("bash", "-c", body).CombinedOutput()
			if (e == nil) != tc.success || !strings.Contains(string(out), tc.want) {
				t.Fatalf("%s error=%v", out, e)
			}
		})
	}
}
