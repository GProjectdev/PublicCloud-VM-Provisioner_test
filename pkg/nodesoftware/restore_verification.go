package nodesoftware

import (
	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	"encoding/json"
	"fmt"
	"strings"
)

// RestoreVerificationCommand checks a running node without modifying its runtime.
// The caller supplies administrator qualification; this is not an E2E smoke test.
func RestoreVerificationCommand(cfg *api.NodeSoftwareConfig, version string) (string, error) {
	if err := Validate(cfg, version); err != nil {
		return "", err
	}
	if cfg == nil || cfg.MigrationRuntime == nil || !cfg.MigrationRuntime.CertifyRestore {
		return "", fmt.Errorf("restore certification is not enabled")
	}
	r := cfg.MigrationRuntime
	expected, err := json.Marshal(map[string]string{
		"kubernetesMinor": kubernetesMinor(version),
		"crioCommit":      strings.ToLower(r.CRIOCommit),
		"criuCommit":      strings.ToLower(r.CRIUCommit),
		"adapterSHA256":   strings.ToLower(r.AdapterSHA256),
		"restoreProfile":  "gpu-file-v1",
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`timeout 45s sudo -n python3 - <<'PY'
import hashlib, json, os, re, stat, subprocess
expected = json.loads(%q)
def require(ok, message):
    if not ok:
        raise SystemExit(message)
def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1048576), b""):
            h.update(block)
    return h.hexdigest()
with open("/usr/local/share/stateful-migration/runtime.json") as f:
    m = json.load(f)
require(m.get("formatVersion") == 1, "unsupported manifest format")
for key, value in expected.items():
    require(m.get(key) == value, "manifest mismatch: " + key)
with open("/usr/local/share/stateful-migration/package.sha256") as f:
    require(f.read().strip() == %q, "package pin differs from bootstrap")
paths = ["/usr/local/bin/crio", "/usr/local/sbin/criu",
         "/usr/local/bin/cuda-checkpoint", "/usr/local/lib/criu/cuda_plugin.so"]
require(set(m.get("binaries", {})) == set(paths), "unexpected manifest binaries")
for path in paths:
    require(stat.S_ISREG(os.lstat(path).st_mode), "not a regular file: " + path)
    require(digest(path) == m["binaries"][path], "binary digest mismatch: " + path)
pid = subprocess.check_output(["systemctl", "show", "crio", "-p", "MainPID", "--value"], text=True).strip()
require(pid.isdigit() and int(pid) > 0, "CRI-O is not running")
require(digest("/proc/" + pid + "/exe") == m["binaries"][paths[0]], "running CRI-O differs from package")
with open("/proc/" + pid + "/cmdline", "rb") as f:
    argv = f.read().decode().split("\x00")
require("--config=/etc/crio/crio.conf" in argv and "--config-dir=/etc/crio/crio.conf.d" in argv,
        "running CRI-O uses unqualified config arguments")
version = subprocess.check_output([paths[0], "--version"], text=True)
require(re.search(r"GitCommit:\s*" + re.escape(expected["crioCommit"]) + r"\b", version), "CRI-O commit mismatch")
config = subprocess.check_output([paths[0], "--config=/etc/crio/crio.conf",
    "--config-dir=/etc/crio/crio.conf.d", "config"], text=True)
require(re.search(r"(?m)^\s*enable_criu_support\s*=\s*true\s*$", config), "CRIU disabled")
require(re.search(r'(?m)^\s*default_runtime\s*=\s*"crun"\s*$', config), "unqualified default runtime")
crun = "/usr/libexec/crio/crun"
section = re.search(r'(?ms)^\[crio\.runtime\.runtimes\.crun\]\s*\n(.*?)(?=^\[|\Z)', config)
require(section is not None, "missing crun configuration")
require(re.search(r'(?m)^\s*runtime_path\s*=\s*"' + re.escape(crun) + r'"\s*$', section.group(1)), "unqualified crun path")
require("+CRIU" in subprocess.check_output([crun, "--version"], text=True), "crun lacks CRIU")
require("--tcp-close" in subprocess.check_output([crun, "restore", "--help"], text=True), "crun lacks tcp-close")
for path in ["/etc/criu/default.conf", "/etc/criu/crun.conf", "/etc/criu/runc.conf"]:
    with open(path) as f:
        require("libdir /usr/local/lib/criu" in f.read().splitlines(), "missing CUDA plugin libdir: " + path)
print("RESTORE_RUNTIME_VERIFIED")
PY
`, string(expected), strings.ToLower(r.PackageSHA256)), nil
}
