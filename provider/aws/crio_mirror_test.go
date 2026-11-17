package aws

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCRIOFailedMirrorUsesSignedDirectMirror(t *testing.T) {
	script, err := BuildStartupScript(CloudInitParams{WGConfig: "[Interface]", VpnIP: "10.200.0.3", JoinCommand: "kubeadm join example:6443 --token example", KubernetesVersion: "1.37.2", KubernetesMinorVersion: "1.37", NodeName: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(script, "if ! { apt_update && apt_install cri-o criu; }; then")
	if start < 0 {
		t.Fatal("missing mirror fallback")
	}
	end := strings.Index(script[start:], "\nfi")
	if end < 0 {
		t.Fatal("unterminated fallback")
	}
	block := script[start : start+end+3]
	path := filepath.Join(t.TempDir(), "cri-o.list")
	line := "deb [signed-by=/etc/apt/keyrings/cri-o-apt-keyring.gpg] https://downloadcontent.opensuse.org/repositories/isv:/cri-o:/stable:/v1.37/deb/ /\n"
	if err := os.WriteFile(path, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	block = strings.ReplaceAll(block, "/etc/apt/sources.list.d/cri-o.list", path)
	testScript := "set -eu\nc=0\nreport() { :; }\napt_update() { :; }\napt_install() { c=$((c+1)); if [ \"$c\" -eq 1 ]; then return 1; fi; }\n" + block + "\ntest \"$c\" -eq 2\n"
	if out, err := exec.Command("bash", "-c", testScript).CombinedOutput(); err != nil {
		t.Fatalf("fallback failed: %v %s", err, out)
	}
	// An index download failure must use the same signed fallback before install.
	indexFailure := "set -eu\nu=0\ni=0\nreport() { :; }\napt_update() { u=$((u+1)); if [ \"$u\" -eq 1 ]; then return 1; fi; }\napt_install() { i=$((i+1)); }\n" + block + "\ntest \"$u\" -eq 2\ntest \"$i\" -eq 1\n"
	if out, err := exec.Command("bash", "-c", indexFailure).CombinedOutput(); err != nil {
		t.Fatalf("index fallback failed: %v %s", err, out)
	}
	// A failed fallback must stop bootstrap instead of reporting success.
	bothFail := "set -eu\nreport() { :; }\napt_update() { :; }\napt_install() { return 1; }\n" + block + "\necho UNSAFE_SUCCESS\n"
	if out, err := exec.Command("bash", "-c", bothFail).CombinedOutput(); err == nil || strings.Contains(string(out), "UNSAFE_SUCCESS") {
		t.Fatalf("failed fallback did not fail closed: %v %s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "https://download.opensuse.org/") || !strings.Contains(string(data), "signed-by=/etc/apt/keyrings/cri-o-apt-keyring.gpg") {
		t.Fatalf("mirror or signature gate changed: %s", data)
	}
	for _, s := range []string{"Acquire::http::Timeout=30", "Acquire::https::Timeout=30"} {
		if !strings.Contains(script, s) {
			t.Fatal("missing bound", s)
		}
	}
	if strings.Contains(block, "--allow-unauthenticated") {
		t.Fatal("signature verification bypassed")
	}
}
