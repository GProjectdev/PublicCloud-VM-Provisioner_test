package nodesoftware

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
)

func TestRuntimeConfigFilesPreserveBaseAndMigratePluginOption(t *testing.T) {
	bash := os.Getenv("TEST_BASH")
	if bash == "" {
		var err error
		bash, err = exec.LookPath("bash")
		if err != nil {
			t.Skip("bash unavailable")
		}
	}
	script, err := Render(statefulConfig(), "v1.37.1")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(script, "mkdir -p /etc/systemd/system/crio.service.d")
	if start < 0 {
		t.Fatal("missing config setup start")
	}
	end := strings.Index(script[start:], "systemctl daemon-reload")
	if end < 0 {
		t.Fatal("missing config setup")
	}
	setup := script[start : start+end]
	setup = strings.ReplaceAll(setup, "/etc/", "${root}/etc/")
	setup = strings.ReplaceAll(setup, "/var/run/cdi", "${root}/var/run/cdi")
	check := `set -eu
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
mkdir -p "$root/etc/criu"
printf 'tcp-close\nplugin-dir /old\nlibdir /other\n' > "$root/etc/criu/default.conf"
`
	check += setup
	check += `
test -f "$root/etc/crio/crio.conf"
printf '# existing configuration\n' > "$root/etc/crio/crio.conf"
`
	check += setup
	check += `
grep -qx '# existing configuration' "$root/etc/crio/crio.conf"
grep -qx 'tcp-close' "$root/etc/criu/default.conf"
for conf in "$root/etc/criu/default.conf" "$root/etc/criu/runc.conf"; do
  ! grep -q '^plugin-dir' "$conf"
  test "$(grep -c '^libdir ' "$conf")" = 1
  grep -qx 'libdir /usr/local/lib/criu' "$conf"
done
`
	cmd := exec.Command(bash, "-c", check)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("config setup: %v\n%s", err, out)
	}
}

const (
	hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	hex40 = "0123456789abcdef0123456789abcdef01234567"
)

func TestNilAndDefaultRenderEmpty(t *testing.T) {
	got, err := Render(nil, "v1.34.2")
	if err != nil {
		t.Fatalf("Render(nil) error = %v", err)
	}
	if got != "" {
		t.Fatalf("Render(nil) = %q, want empty", got)
	}

	got, err = Render(&api.NodeSoftwareConfig{}, "v1.34.2")
	if err != nil {
		t.Fatalf("Render(default) error = %v", err)
	}
	if got != "" {
		t.Fatalf("Render(default) = %q, want empty", got)
	}
}

func TestValidateMigrationRuntimeContract(t *testing.T) {
	valid := statefulConfig()
	if err := Validate(valid, "v1.34.2"); err != nil {
		t.Fatalf("Validate(valid) error = %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*api.NodeSoftwareConfig)
		want   string
	}{
		{"standard forbids migration runtime", func(c *api.NodeSoftwareConfig) { c.RuntimeProfile = RuntimeProfileStandard }, "forbidden"},
		{"http rejected", func(c *api.NodeSoftwareConfig) { c.MigrationRuntime.PackageURL = "http://example.com/pkg.deb" }, "https"},
		{"credentials rejected", func(c *api.NodeSoftwareConfig) {
			c.MigrationRuntime.PackageURL = "https://user:pass@example.com/pkg.deb"
		}, "credentials"},
		{"query rejected", func(c *api.NodeSoftwareConfig) {
			c.MigrationRuntime.PackageURL = "https://example.com/pkg.deb?token=secret"
		}, "query"},
		{"fragment rejected", func(c *api.NodeSoftwareConfig) { c.MigrationRuntime.PackageURL = "https://example.com/pkg.deb#sig" }, "fragment"},
		{"empty fragment separator rejected", func(c *api.NodeSoftwareConfig) {
			c.MigrationRuntime.PackageURL = "https://example.test/';touch /tmp/unsafe;#"
		}, "fragment"},
		{"empty query rejected", func(c *api.NodeSoftwareConfig) { c.MigrationRuntime.PackageURL = "https://example.com/pkg.deb?" }, "query"},
		{"raw quote rejected", func(c *api.NodeSoftwareConfig) { c.MigrationRuntime.PackageURL = "https://example.com/unsafe'path.deb" }, "quotes"},
		{"bad package sha", func(c *api.NodeSoftwareConfig) { c.MigrationRuntime.PackageSHA256 = "abc" }, "SHA256"},
		{"bad crio commit", func(c *api.NodeSoftwareConfig) { c.MigrationRuntime.CRIOCommit = "abc" }, "crioCommit"},
		{"bad criu commit", func(c *api.NodeSoftwareConfig) { c.MigrationRuntime.CRIUCommit = "abc" }, "criuCommit"},
		{"bad adapter sha", func(c *api.NodeSoftwareConfig) { c.MigrationRuntime.AdapterSHA256 = "abc" }, "adapterSHA256"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := statefulConfig()
			tc.mutate(cfg)
			err := Validate(cfg, "v1.34.2")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateStatefulRequiresStableKubernetesVersion(t *testing.T) {
	for _, version := range []string{"v1.34.2-alpha.1", "v1.34", "junk"} {
		if err := Validate(statefulConfig(), version); err == nil {
			t.Fatalf("Validate(StatefulMigration, %q) error = nil, want error", version)
		}
	}
}
func TestValidateDRARequiresKubernetes1342(t *testing.T) {
	cfg := &api.NodeSoftwareConfig{GPUMode: GPUModeDRA}
	if err := Validate(cfg, "v1.34.1"); err == nil {
		t.Fatal("Validate(DRA on v1.34.1) error = nil, want error")
	}
	if err := Validate(cfg, "v1.34.2"); err != nil {
		t.Fatalf("Validate(DRA on v1.34.2) error = %v", err)
	}
}

func TestRenderStatefulRuntimeGuardsAndManifest(t *testing.T) {
	script, err := Render(statefulConfig(), "v1.34.3")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	ordered := []string{
		"/etc/kubernetes/kubelet.conf",
		"curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2",
		"sha256sum -c -",
		"apt-get install -y \"$runtime_pkg\"",
		"\"formatVersion\": 1",
		"\"kubernetesMinor\": \"1.34\"",
		"/usr/local/bin/crio",
		"/usr/local/sbin/criu",
		"/usr/local/bin/cuda-checkpoint",
		"/usr/local/lib/criu/cuda_plugin.so",
		"stat.S_ISLNK",
		"os.access(path, os.X_OK)",
		"--version",
		"Environment=\"NVIDIA_DRIVER_ROOT=/run/nvidia/driver\"",
		"Environment=\"PATH=/run/nvidia/driver/usr/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\"",
		"Environment=\"LD_LIBRARY_PATH=/run/nvidia/driver/usr/lib/x86_64-linux-gnu:/run/nvidia/driver/usr/lib64:/run/nvidia/driver/lib64\"",
		"ExecStart=/usr/local/bin/crio --config=/etc/crio/crio.conf --config-dir=/etc/crio/crio.conf.d",
		"enable_criu_support = true",
		"cdi_spec_dirs = [\"/etc/cdi\", \"/var/run/cdi\"]",
		"ensure_criu_plugin_dir /etc/criu/default.conf",
		"ensure_criu_plugin_dir /etc/criu/runc.conf",
	}
	last := -1
	for _, needle := range ordered {
		idx := strings.Index(script, needle)
		if idx < 0 {
			t.Fatalf("rendered script missing %q\n%s", needle, script)
		}
		if idx < last {
			t.Fatalf("rendered script has %q before prior required step", needle)
		}
		last = idx
	}
	if strings.Contains(script, "nvidia-smi") || strings.Contains(script, "GPUReady") {
		t.Fatalf("rendered script claims prejoin GPU validation:\n%s", script)
	}
}

func TestRenderNFSClient(t *testing.T) {
	script, err := Render(&api.NodeSoftwareConfig{NFSClient: true}, "v1.33.0")
	if err != nil {
		t.Fatalf("Render(NFSClient) error = %v", err)
	}
	for _, needle := range []string{"apt-get install -y nfs-common", "command -v mount.nfs"} {
		if !strings.Contains(script, needle) {
			t.Fatalf("rendered NFS script missing %q\n%s", needle, script)
		}
	}
}

func TestRenderedPythonSyntaxWhenPythonAvailable(t *testing.T) {
	python, err := findPython()
	if err != nil {
		t.Skip("python not available")
	}
	script, err := Render(statefulConfig(), "v1.34.2")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	body, ok := extractPythonHeredoc(script)
	if !ok {
		t.Fatalf("rendered script missing Python heredoc:\n%s", script)
	}
	cmd := exec.Command(python, "-c", "import sys; compile(sys.stdin.read(), '<nodesoftware-rendered>', 'exec')")
	cmd.Stdin = strings.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("python compile failed: %v\n%s\npython:\n%s", err, string(out), body)
	}
}
func TestRenderedShellSyntaxWhenBashAvailable(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	script, err := Render(statefulConfig(), "v1.34.2")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s\nscript:\n%s", err, string(out), script)
	}
}

func findPython() (string, error) {
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, "-c", "import sys")
		if err := cmd.Run(); err == nil {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func extractPythonHeredoc(script string) (string, bool) {
	startMarker := "<<'PY'\n"
	start := strings.Index(script, startMarker)
	if start < 0 {
		return "", false
	}
	start += len(startMarker)
	end := strings.Index(script[start:], "\nPY\n")
	if end < 0 {
		return "", false
	}
	return script[start : start+end], true
}
func statefulConfig() *api.NodeSoftwareConfig {
	return &api.NodeSoftwareConfig{
		RuntimeProfile: RuntimeProfileStatefulMigration,
		MigrationRuntime: &api.MigrationRuntimeConfig{
			PackageURL:    "https://downloads.example.com/stateful-runtime.deb",
			PackageSHA256: hex64,
			CRIOCommit:    hex40,
			CRIUCommit:    hex40,
			AdapterSHA256: hex64,
		},
	}
}
