package nodesoftware

// RestoreLinkerCommand prepares only the GPU Operator library directories.
// It runs after GPU readiness, so a missing driver mount is retried, not certified.
func RestoreLinkerCommand() string {
	return `timeout 45s sudo -n /bin/bash -eu -o pipefail <<'SH'
dirs=()
for dir in /run/nvidia/driver/usr/lib/x86_64-linux-gnu /run/nvidia/driver/usr/lib64 /run/nvidia/driver/lib64; do
  if [ -d "$dir" ]; then
    dirs+=("$dir")
  fi
done
if [ "${#dirs[@]}" -gt 0 ]; then
  tmp="$(mktemp /etc/ld.so.conf.d/.stateful-nvidia.XXXXXX)"
  trap 'rm -f -- "$tmp"' EXIT
  printf '%s\n' "${dirs[@]}" > "$tmp"
  chmod 0644 "$tmp"
  mv -f -- "$tmp" /etc/ld.so.conf.d/stateful-nvidia-driver.conf
fi
ldconfig
env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin python3 - <<'PY'
import ctypes, subprocess
ctypes.CDLL("libcuda.so.1")
ctypes.CDLL("libnvidia-ml.so.1")
subprocess.run(["/usr/local/bin/cuda-checkpoint", "--help"], check=True, timeout=10)
PY
SH
`
}
