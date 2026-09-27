#!/usr/bin/env bash
set -euo pipefail
PATH="/usr/bin:/bin:$PATH"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
INSTALLER="$REPO_ROOT/scripts/install-gpu-addons.sh"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

mkdir -p "$tmpdir/bin"
touch "$tmpdir/kubeconfig"

cat >"$tmpdir/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
args=("$@")
cmd=""
for i in "${!args[@]}"; do
  case "${args[$i]}" in
    config|version|get|api-resources)
      cmd="${args[$i]}"
      rest=("${args[@]:$((i+1))}")
      break
      ;;
  esac
done
case "$cmd" in
  config)
    exit 0
    ;;
  version)
    printf '{"serverVersion":{"gitVersion":"%s"}}\n' "${KUBE_VERSION:-v1.34.2}"
    ;;
  api-resources)
    printf 'NAME SHORTNAMES APIVERSION NAMESPACED KIND\ndeviceclasses resource.k8s.io/v1 false DeviceClass\n'
    ;;
  get)
    target="${rest[0]:-}"
    case "$target" in
      nodes)
        if printf '%s\n' "${rest[*]}" | grep -q -- '-o json'; then
          if [[ "${EMPTY_DRA_NODE_LABEL:-}" == 1 ]]; then
            printf '{"items":[]}\n'
          else
            printf '{"items":[{"metadata":{"name":"node-a"},"status":{"nodeInfo":{"containerRuntimeVersion":"cri-o://%s"}}}]}\n' "${CRIO_VERSION:-1.33.4}"
          fi
        else
          [[ "${NO_DRA_NODE_LABEL:-}" == 1 ]] && exit 1
          printf 'node-a Ready\n'
        fi
        ;;
      clusterpolicy|clusterpolicies.nvidia.com|gpucluster|gpuclusters.nvidia.com|deviceclasses.resource.k8s.io)
        if [[ "${KUBECTL_LIST_ERROR:-}" == 1 ]]; then
          printf 'Error from server (Forbidden): forbidden\n' >&2
          exit 1
        fi
        if [[ "${DEVICECLASS_LIST_ERROR:-}" == 1 && "$target" == deviceclasses.resource.k8s.io ]]; then
          printf 'Error from server (Forbidden): forbidden\n' >&2
          exit 1
        fi
        if [[ "${CLUSTERPOLICY_EXISTS:-}" == 1 && "$target" == clusterpolicies.nvidia.com ]]; then
          printf '{"items":[{"metadata":{"name":"nvidia-gpu-operator"}}]}\n'
        elif [[ "${DEVICECLASS_EXISTS:-}" == 1 && "$target" == deviceclasses.resource.k8s.io ]]; then
          printf '{"items":[{"metadata":{"name":"gpu.nvidia.com"}}]}\n'
        else
          printf '{"items":[]}\n'
        fi
        ;;
      *)
        exit 1
        ;;
    esac
    ;;
  *)
    printf 'unexpected kubectl args: %s\n' "$*" >&2
    exit 99
    ;;
esac
STUB
chmod +x "$tmpdir/bin/kubectl"

cat >"$tmpdir/bin/helm" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${HELM_LOG:?}"
cmd=""
for arg in "$@"; do
  case "$arg" in
    list|status|template|install|upgrade|repo)
      cmd="$arg"
      break
      ;;
  esac
done
case "$cmd" in
  list)
    if [[ "${HELM_STATUS_OK:-}" == 1 ]]; then
      printf '[{"name":"gpu-operator"}]\n'
    else
      printf '[]\n'
    fi
    ;;
  status)
    exit 99
    ;;
  template)
    printf -- '---\n# rendered by helm template %s\n' "${2:-}"
    exit 0
    ;;
  install|upgrade|repo)
    exit 0
    ;;
  *)
    printf 'unexpected helm args: %s\n' "$*" >&2
    exit 99
    ;;
esac
STUB
chmod +x "$tmpdir/bin/helm"

run_installer() {
  PATH="$tmpdir/bin:$PATH" HELM_LOG="$tmpdir/helm.log" bash "$INSTALLER" --kubeconfig "$tmpdir/kubeconfig" --context test-context "$@"
}

assert_contains() {
  local file="$1" needle="$2"
  grep -Fq -- "$needle" "$file" || {
    printf 'expected %s to contain: %s\n' "$file" "$needle" >&2
    printf 'actual:\n' >&2
    cat "$file" >&2
    exit 1
  }
}

assert_not_contains() {
  local file="$1" needle="$2"
  if grep -Fq -- "$needle" "$file"; then
    printf 'expected %s not to contain: %s\n' "$file" "$needle" >&2
    cat "$file" >&2
    exit 1
  fi
}

: >"$tmpdir/helm.log"
if bash "$INSTALLER" >/dev/null 2>"$tmpdir/noargs.err"; then
  printf 'expected no-arg invocation to fail\n' >&2
  exit 1
fi
assert_contains "$tmpdir/noargs.err" '--kubeconfig is required'

: >"$tmpdir/helm.log"
run_installer --mode dra --driver-owner gpu-operator >"$tmpdir/dra-render.yaml"
assert_contains "$tmpdir/helm.log" 'template gpu-operator gpu-operator --repo https://helm.ngc.nvidia.com/nvidia --version v26.3.3'
assert_contains "$tmpdir/helm.log" 'template dra-driver-nvidia-gpu oci://registry.k8s.io/dra-driver-nvidia/charts/dra-driver-nvidia-gpu --version 0.5.0'
assert_contains "$tmpdir/helm.log" '--set nvidiaDriverRoot=/run/nvidia/driver'
assert_not_contains "$tmpdir/helm.log" 'upgrade --install'
assert_contains "$tmpdir/dra-render.yaml" '# rendered by helm template gpu-operator'
assert_contains "$tmpdir/dra-render.yaml" '# rendered by helm template dra-driver-nvidia-gpu'

: >"$tmpdir/helm.log"
run_installer --mode device-plugin --driver-owner preinstalled >"$tmpdir/device-plugin-render.yaml"
assert_contains "$tmpdir/helm.log" '--set driver.enabled=false'
assert_contains "$tmpdir/helm.log" '--set toolkit.enabled=false'
assert_contains "$tmpdir/device-plugin-render.yaml" '# rendered by helm template gpu-operator'

: >"$tmpdir/helm.log"
run_installer --mode device-plugin --driver-owner gpu-operator --apply >/dev/null
assert_contains "$tmpdir/helm.log" 'install gpu-operator nvidia/gpu-operator --version v26.3.3'
assert_not_contains "$tmpdir/helm.log" 'upgrade --install'

: >"$tmpdir/helm.log"
if KUBE_VERSION=v1.33.9 run_installer --mode dra --driver-owner gpu-operator >/dev/null 2>"$tmpdir/oldk8s.err"; then
  printf 'expected old Kubernetes server to fail\n' >&2
  exit 1
fi
assert_contains "$tmpdir/oldk8s.err" 'requires Kubernetes >= 1.34.2'

: >"$tmpdir/helm.log"
if EMPTY_DRA_NODE_LABEL=1 run_installer --mode dra --driver-owner gpu-operator >/dev/null 2>"$tmpdir/nolabel.err"; then
  printf 'expected missing DRA node label to fail\n' >&2
  exit 1
fi
assert_contains "$tmpdir/nolabel.err" 'requires at least one node labeled nvidia.com/dra-kubelet-plugin=true'

: >"$tmpdir/helm.log"
if KUBECTL_LIST_ERROR=1 run_installer --mode device-plugin --driver-owner gpu-operator >/dev/null 2>"$tmpdir/rbac.err"; then
  printf 'expected Kubernetes list RBAC error to fail closed\n' >&2
  exit 1
fi
assert_contains "$tmpdir/rbac.err" 'failed to read clusterpolicies.nvidia.com'

: >"$tmpdir/helm.log"
if DEVICECLASS_LIST_ERROR=1 run_installer --mode dra --driver-owner gpu-operator >/dev/null 2>"$tmpdir/deviceclass-rbac.err"; then
  printf 'expected DeviceClass list RBAC error to fail closed\n' >&2
  exit 1
fi
assert_contains "$tmpdir/deviceclass-rbac.err" 'failed to read deviceclasses.resource.k8s.io'

: >"$tmpdir/helm.log"
if HELM_STATUS_OK=1 run_installer --mode device-plugin --driver-owner gpu-operator >/dev/null 2>"$tmpdir/existing.err"; then
  printf 'expected existing Helm release to fail\n' >&2
  exit 1
fi
assert_contains "$tmpdir/existing.err" 'already exists; refusing takeover or upgrade'

printf 'install-gpu-addons tests passed\n'
