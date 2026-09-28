package nodesoftware

import (
	"fmt"
	"strings"

	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
)

// Render returns a root bash snippet for new-node software bootstrap. A nil or
// default Standard/None config returns an empty string to preserve current behavior.
func Render(config *api.NodeSoftwareConfig, kubernetesVersion string) (string, error) {
	if err := Validate(config, kubernetesVersion); err != nil {
		return "", err
	}
	if config == nil {
		return "", nil
	}

	profile := runtimeProfile(config.RuntimeProfile)
	gpu := gpuMode(config.GPUMode)
	if profile == RuntimeProfileStandard && !config.NFSClient && gpu == GPUModeNone {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -euo pipefail\n")
	b.WriteString("if [ \"${EUID:-$(id -u)}\" -ne 0 ]; then echo 'node software bootstrap must run as root' >&2; exit 1; fi\n")
	b.WriteString("if [ -f /etc/kubernetes/kubelet.conf ]; then echo 'refusing node software changes on an already joined Kubernetes node' >&2; exit 1; fi\n")
	b.WriteString("export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n")

	if config.NFSClient {
		b.WriteString(renderNFSClient())
	}
	if profile == RuntimeProfileStatefulMigration {
		b.WriteString(renderMigrationRuntime(config.MigrationRuntime, kubernetesMinor(kubernetesVersion)))
	}
	switch gpu {
	case GPUModeDevicePlugin:
		b.WriteString("# GPUMode=DevicePlugin: node bootstrap only; deploy the NVIDIA device plugin from cluster management after join.\n")
	case GPUModeDRA:
		b.WriteString("# GPUMode=DRA: node bootstrap only; deploy NVIDIA DRA components from cluster management after join.\n")
	}

	return b.String(), nil
}

func renderNFSClient() string {
	return strings.Join([]string{
		"apt-get update",
		"DEBIAN_FRONTEND=noninteractive apt-get install -y nfs-common",
		"command -v mount.nfs >/dev/null",
		"",
	}, "\n")
}

func renderMigrationRuntime(cfg *api.MigrationRuntimeConfig, kubeMinor string) string {
	return fmt.Sprintf(`workdir="$(mktemp -d)"
cleanup_node_software() { rm -rf "$workdir"; }
trap cleanup_node_software EXIT
runtime_pkg="$workdir/stateful-migration-runtime.deb"
curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 -o "$runtime_pkg" %s
printf '%%s  %%s\n' %s "$runtime_pkg" | sha256sum -c -
DEBIAN_FRONTEND=noninteractive apt-get install -y "$runtime_pkg"
test -f %s
python3 - %s <<'PY'
import hashlib
import json
import os
import re
import stat
import subprocess
import sys

manifest_path = sys.argv[1]
expected = {
    "formatVersion": 1,
    "kubernetesMinor": %q,
    "crioCommit": %q,
    "criuCommit": %q,
    "adapterSHA256": %q,
}
fixed_paths = [%q, %q, %q, %q]
with open(manifest_path, "r", encoding="utf-8") as f:
    manifest = json.load(f)
for key, value in expected.items():
    if manifest.get(key) != value:
        raise SystemExit(f"runtime manifest {key}={manifest.get(key)!r}, want {value!r}")
binaries = manifest.get("binaries")
if not isinstance(binaries, dict):
    raise SystemExit("runtime manifest binaries must be an object")
if set(binaries.keys()) != set(fixed_paths):
    raise SystemExit(f"runtime manifest binaries keys={sorted(binaries.keys())!r}, want {sorted(fixed_paths)!r}")
for path in fixed_paths:
    want = binaries[path]
    if not isinstance(want, str) or len(want) != 64 or any(c not in "0123456789abcdefABCDEF" for c in want):
        raise SystemExit(f"runtime manifest hash for {path} is not hex64")
    st = os.lstat(path)
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISREG(st.st_mode):
        raise SystemExit(f"{path} must be a non-symlink regular file")
    if path != %q and not os.access(path, os.X_OK):
        raise SystemExit(f"{path} must be executable")
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    if h.hexdigest().lower() != want.lower():
        raise SystemExit(f"sha256 mismatch for {path}")
crio_version = subprocess.check_output([%q, "--version"], text=True, stderr=subprocess.STDOUT)
criu_version = subprocess.check_output([%q, "--version"], text=True, stderr=subprocess.STDOUT)
match = re.search(r"\b(\d+)\.(\d+)(?:\.\d+)?\b", crio_version)
if not match:
    raise SystemExit("crio --version did not include a parseable version")
actual_minor = f"{int(match.group(1))}.{int(match.group(2))}"
if actual_minor != expected["kubernetesMinor"]:
    raise SystemExit(f"crio --version Kubernetes minor {actual_minor}, want {expected['kubernetesMinor']}")
if not criu_version.strip():
    raise SystemExit("criu --version returned empty output")
commit = re.search(r"GitCommit:\s*([0-9a-f]{40})\b", crio_version)
if not commit or commit.group(1) != expected["crioCommit"]:
    raise SystemExit("crio binary GitCommit does not match runtime manifest")
PY
mkdir -p /etc/systemd/system/crio.service.d /etc/crio/crio.conf.d /etc/criu /etc/cdi /var/run/cdi
# An explicit --config path must exist even when the distro supplies only drop-ins.
if [ ! -e /etc/crio/crio.conf ]; then
  touch /etc/crio/crio.conf
  chmod 0644 /etc/crio/crio.conf
fi
cat >/etc/systemd/system/crio.service.d/20-stateful-migration.conf <<'EOF'
[Service]
Environment="PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
ExecStart=
ExecStart=/usr/local/bin/crio --config=/etc/crio/crio.conf --config-dir=/etc/crio/crio.conf.d
EOF
cat >/etc/crio/crio.conf.d/70-stateful-migration.conf <<'EOF'
[crio.runtime]
enable_criu_support = true
cdi_spec_dirs = ["/etc/cdi", "/var/run/cdi"]
EOF
ensure_criu_plugin_dir() {
  conf="$1"
  mkdir -p "$(dirname "$conf")"
  touch "$conf"
  # Migrate the unsupported legacy option and keep one effective libdir entry.
  sed -i -E '/^[[:space:]]*(plugin-dir|libdir)[[:space:]]/d' "$conf"
  printf '\nlibdir /usr/local/lib/criu\n' >>"$conf"
}
ensure_criu_plugin_dir /etc/criu/default.conf
ensure_criu_plugin_dir /etc/criu/runc.conf
ensure_criu_plugin_dir /etc/criu/crun.conf
printf '%%s\n' %s > /usr/local/share/stateful-migration/package.sha256
systemctl daemon-reload
`, shellQuote(cfg.PackageURL), shellQuote(cfg.PackageSHA256), shellQuote(RuntimeManifestPath), shellQuote(RuntimeManifestPath), kubeMinor, strings.ToLower(cfg.CRIOCommit), strings.ToLower(cfg.CRIUCommit), strings.ToLower(cfg.AdapterSHA256), CRIOBinaryPath, CRIUBinaryPath, CUDACheckpointPath, CUDAPluginPath, CUDAPluginPath, CRIOBinaryPath, CRIUBinaryPath, shellQuote(strings.ToLower(cfg.PackageSHA256)))
}
