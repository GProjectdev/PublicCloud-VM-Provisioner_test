package aws

import (
	"encoding/base64"
	"os"
	"os/exec"
	"strings"
	"testing"

	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
)

func softwareParams() CloudInitParams {
	return CloudInitParams{DirectVPC: true, JoinCommand: "kubeadm join 10.0.0.1:6443 --token test", KubernetesVersion: "1.35.8", KubernetesMinorVersion: "1.35", NodeName: "worker-1", IsGPUNode: true}
}

func migrationSoftware() *api.NodeSoftwareConfig {
	return &api.NodeSoftwareConfig{RuntimeProfile: "StatefulMigration", NFSClient: true, GPUMode: "DRA", MigrationRuntime: &api.MigrationRuntimeConfig{
		PackageURL: "https://packages.example.test/runtime.deb", PackageSHA256: strings.Repeat("a", 64), CRIOCommit: strings.Repeat("b", 40), CRIUCommit: strings.Repeat("c", 40), AdapterSHA256: strings.Repeat("d", 64),
	}}
}

func TestNodeSoftwareInstallsBeforeRuntimeStartAndJoin(t *testing.T) {
	p := softwareParams()
	p.NodeSoftware = migrationSoftware()
	script, err := BuildStartupScript(p)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.Index(script, "apt_install cri-o criu")
	custom := strings.Index(script, p.NodeSoftware.MigrationRuntime.PackageURL)
	start := strings.Index(script, "\nrestart_crio_and_wait\n")
	join := strings.Index(script, "\nJOIN_CMD=")
	if base < 0 || custom <= base || start <= custom || join <= start {
		t.Fatal("expected base packages, custom runtime, service start, then join")
	}
	for _, want := range []string{"mount.nfs", p.NodeSoftware.MigrationRuntime.PackageSHA256, "/etc/kubernetes/kubelet.conf"} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing prerequisite %q", want)
		}
	}
	earlyGuard := strings.Index(script, "Refusing node software bootstrap on an already joined node")
	if earlyGuard < 0 || earlyGuard > base {
		t.Fatal("joined-node guard must precede base installation")
	}
	bash := os.Getenv("TEST_BASH")
	if bash == "" {
		bash, err = exec.LookPath("bash")
		if err != nil {
			t.Skip("bash unavailable")
		}
	}
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash syntax: %v: %s", err, out)
	}
}

func TestNodeSoftwareFitsEC2UserDataLimit(t *testing.T) {
	p := softwareParams()
	p.NodeSoftware = migrationSoftware()
	data, err := base64.StdEncoding.DecodeString(BuildUserData(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 16*1024 {
		t.Fatalf("compressed EC2 user data exceeds 16 KiB: %d", len(data))
	}
}

func TestNodeSoftwareKubernetes137(t *testing.T) {
	p := softwareParams()
	p.KubernetesVersion = "1.37.0"
	p.KubernetesMinorVersion = "1.37"
	p.NodeSoftware = migrationSoftware()
	script, err := BuildStartupScript(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`K8S_VERSION="1.37.0"`, `K8S_MINOR="1.37"`, `"kubernetesMinor": "1.37"`, "nfs-common", "GPUMode=DRA"} {
		if !strings.Contains(script, want) {
			t.Fatalf("1.37 bootstrap missing %q", want)
		}
	}
	data, err := base64.StdEncoding.DecodeString(BuildUserData(p))
	if err != nil || len(data) == 0 || len(data) > 16*1024 {
		t.Fatalf("invalid 1.37 EC2 user data: bytes=%d err=%v", len(data), err)
	}
}

func TestNodeSoftwareRejectsIncompatibleGPUAndDRA(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*CloudInitParams)
	}{
		{"cpu", func(p *CloudInitParams) { p.IsGPUNode = false }},
		{"old-kubernetes", func(p *CloudInitParams) { p.KubernetesVersion = "1.33.4"; p.KubernetesMinorVersion = "1.33" }},
		{"unsafe-url", func(p *CloudInitParams) {
			p.NodeSoftware.MigrationRuntime.PackageURL = "https://example.test/';touch /tmp/unsafe;#"
		}},
		{"missing-digest", func(p *CloudInitParams) { p.NodeSoftware.MigrationRuntime.PackageSHA256 = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := softwareParams()
			p.NodeSoftware = migrationSoftware()
			tc.edit(&p)
			if _, err := BuildStartupScript(p); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestKubeletServingTLSBootstrapGenerationIsOptIn(t *testing.T) {
	standard := softwareParams()
	standard.NodeSoftware = &api.NodeSoftwareConfig{NFSClient: true}
	standardScript, err := BuildStartupScript(standard)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(standardScript, "serverTLSBootstrap") {
		t.Fatal("non-Stateful node software unexpectedly enables kubelet serving TLS bootstrap")
	}

	stateful := softwareParams()
	stateful.NodeSoftware = migrationSoftware()
	statefulScript, err := BuildStartupScript(stateful)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"serverTLSBootstrap: true", "artifact-node=true"} {
		if !strings.Contains(statefulScript, want) {
			t.Fatalf("StatefulMigration script missing %q", want)
		}
	}

	explicit := softwareParams()
	explicit.EnableKubeletServingTLSBootstrap = true
	explicitScript, err := BuildStartupScript(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explicitScript, "serverTLSBootstrap: true") {
		t.Fatal("explicit kubelet serving TLS bootstrap opt-in was not rendered")
	}
}
func TestNodeSoftwareNFSOnlyLeavesStandardRuntime(t *testing.T) {
	p := softwareParams()
	p.IsGPUNode = false
	p.NodeSoftware = &api.NodeSoftwareConfig{NFSClient: true}
	script, err := BuildStartupScript(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "nfs-common") || !strings.Contains(script, "Installing standard CRI-O") {
		t.Fatal("NFS option lost base runtime")
	}
	if strings.Contains(script, "runtime.json") {
		t.Fatal("NFS-only option unexpectedly installs custom runtime")
	}
}
