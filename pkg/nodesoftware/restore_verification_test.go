package nodesoftware

import (
	"os/exec"
	"strings"
	"testing"
)

func TestRestoreProbeRejectsRuntimeDrift(t *testing.T) {
	cfg := statefulConfig()
	cfg.MigrationRuntime.CertifyRestore = true
	command, err := RestoreVerificationCommand(cfg, "v1.37.1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := extractPythonHeredoc(command)
	python, err := findPython()
	if err != nil {
		t.Skip("python unavailable")
	}
	for _, scenario := range []string{"ok", "running-binary", "config-args", "crun-path", "package-pin", "driver-linker", "cuda-helper"} {
		t.Run(scenario, func(t *testing.T) {
			harness := `import io, json, hashlib, os, re, stat, subprocess, sys
from unittest.mock import patch
body = sys.stdin.read()
scenario = sys.argv[1]
# Read the generated expected data, not a separately maintained fixture.
expected_line = next(x for x in body.splitlines() if x.startswith("expected = "))
ns = {"json": json}
exec(expected_line, ns)
manifest = dict(ns["expected"], formatVersion=1)
paths = ["/usr/local/bin/crio", "/usr/local/sbin/criu", "/usr/local/bin/cuda-checkpoint", "/usr/local/lib/criu/cuda_plugin.so"]
manifest["binaries"] = {p: hashlib.sha256(b"binary").hexdigest() for p in paths}
pin = sys.argv[2]
def opened(path, mode="r"):
    if path.endswith("runtime.json"): return io.StringIO(json.dumps(manifest))
    if path.endswith("package.sha256"): return io.StringIO("wrong" if scenario == "package-pin" else pin)
    if path.endswith("/cmdline"):
        return io.BytesIO(b"crio\x00" if scenario == "config-args" else b"crio\x00--config=/etc/crio/crio.conf\x00--config-dir=/etc/crio/crio.conf.d\x00")
    if path.startswith("/etc/criu/"): return io.StringIO("libdir /usr/local/lib/criu\n")
    return io.BytesIO(b"wrong" if scenario == "running-binary" and path.startswith("/proc/") else b"binary")
def output(args, **kwargs):
    if args[0] == "python3" or args[0] == paths[2]:
        assert "LD_LIBRARY_PATH" not in kwargs["env"]
        if (scenario == "driver-linker" and args[0] == "python3") or (scenario == "cuda-helper" and args[0] == paths[2]):
            raise subprocess.CalledProcessError(1, args)
        return ""
    if args[0] == "systemctl": return "123"
    if args[-1] == "config":
        crun = "/wrong" if scenario == "crun-path" else "/usr/libexec/crio/crun"
        return 'enable_criu_support = true\ndefault_runtime = "crun"\n[crio.runtime.runtimes.crun]\nruntime_path = "' + crun + '"\n[crio.runtime.runtimes.other]\nruntime_path = "/usr/libexec/crio/crun"\n'
    if args[0] == paths[0]: return "GitCommit: " + manifest["crioCommit"]
    return "+CRIU" if args[-1] == "--version" else "--tcp-close"
class Info: st_mode = stat.S_IFREG
with patch("builtins.open", side_effect=opened), patch("os.lstat", return_value=Info()), patch("subprocess.check_output", side_effect=output):
    exec(compile(body, "<probe>", "exec"), {})
`
			cmd := exec.Command(python, "-c", harness, scenario, strings.ToLower(cfg.MigrationRuntime.PackageSHA256))
			cmd.Stdin = strings.NewReader(body)
			out, err := cmd.CombinedOutput()
			if scenario == "ok" {
				if err != nil || !strings.Contains(string(out), "RESTORE_RUNTIME_VERIFIED") {
					t.Fatalf("%v: %s", err, out)
				}
			} else if err == nil {
				t.Fatalf("drift accepted: %s", out)
			}
		})
	}
}

func TestRestoreLinkerIsBoundedAndDoesNotRestartRuntime(t *testing.T) {
	cmd := RestoreLinkerCommand()
	for _, required := range []string{"timeout 45s", "ldconfig", "env -i", "ctypes.CDLL", "libcuda.so.1", "libnvidia-ml.so.1", "stateful-nvidia-driver.conf", "mktemp", "pipefail"} {
		if !strings.Contains(cmd, required) {
			t.Fatalf("missing %s", required)
		}
	}
	if strings.Contains(cmd, "systemctl restart") || strings.Contains(cmd, "apt-get") {
		t.Fatal("linker preparation must not replace live runtime")
	}
}

func TestRestoreVerificationRequiresExplicitCertification(t *testing.T) {
	cfg := statefulConfig()
	if _, err := RestoreVerificationCommand(cfg, "v1.37.1"); err == nil {
		t.Fatal("uncertified configuration accepted")
	}
	cfg.MigrationRuntime.CertifyRestore = true
	command, err := RestoreVerificationCommand(cfg, "v1.37.1")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"timeout 45s sudo -n", "gpu-file-v1", "/proc/", "package.sha256", "--tcp-close", "RESTORE_RUNTIME_VERIFIED"} {
		if !strings.Contains(command, required) {
			t.Fatalf("missing %s", required)
		}
	}
	python, err := findPython()
	if err != nil {
		t.Skip("python unavailable")
	}
	body, ok := extractPythonHeredoc(command)
	if !ok {
		t.Fatal("missing heredoc")
	}
	cmd := exec.Command(python, "-c", "import sys; compile(sys.stdin.read(), '<restore-probe>', 'exec')")
	cmd.Stdin = strings.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
