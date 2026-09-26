#!/usr/bin/env bash
set -euo pipefail

GPU_OPERATOR_VERSION="v26.3.3"
DRA_DRIVER_VERSION="0.5.0"

GPU_OPERATOR_RELEASE="gpu-operator"
GPU_OPERATOR_NAMESPACE="gpu-operator"
GPU_OPERATOR_REPO="https://helm.ngc.nvidia.com/nvidia"
GPU_OPERATOR_CHART="gpu-operator"

DRA_RELEASE="dra-driver-nvidia-gpu"
DRA_NAMESPACE="dra-driver-nvidia-gpu"
DRA_CHART="oci://registry.k8s.io/dra-driver-nvidia/charts/dra-driver-nvidia-gpu"
DRA_NODE_LABEL="nvidia.com/dra-kubelet-plugin"

usage() {
  cat <<'USAGE'
Install or render the cluster-wide NVIDIA GPU add-ons for NodeProvisioner clusters.

This script never uses the current kubectl context implicitly. By default it only
renders Helm manifests with pinned chart versions. Pass --apply to mutate a cluster.

Required:
  --kubeconfig PATH             kubeconfig file to use
  --context NAME                kubeconfig context to use
  --mode device-plugin|dra      device-plugin installs GPU Operator device plugin;
                                dra installs GPU Operator plus standalone NVIDIA DRA driver
  --driver-owner gpu-operator|preinstalled
                                gpu-operator owns host driver/toolkit, or node image does

Optional:
  --apply                       perform helm installs; default is render-only dry run
  --enable-compute-domains      keep DRA ComputeDomain resources enabled; default disables them
  --help                        show this help

Pinned versions:
  GPU Operator: v26.3.3
  NVIDIA DRA driver chart: 0.5.0
USAGE
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

info() {
  printf '==> %s\n' "$*" >&2
}

quote_cmd() {
  local first=1 arg
  for arg in "$@"; do
    if [[ $first -eq 0 ]]; then
      printf ' '
    fi
    first=0
    printf '%q' "$arg"
  done
  printf '\n'
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

json_get() {
  "$PYTHON_JSON" -c 'import json,sys,functools,operator; print(functools.reduce(operator.getitem, sys.argv[1].split("."), json.load(sys.stdin)))' "$1"
}

json_items_len() {
  "$PYTHON_JSON" -c 'import json,sys; data=json.load(sys.stdin); print(len(data if isinstance(data, list) else data.get("items", [])))'
}

json_has_item_name() {
  "$PYTHON_JSON" -c 'import json,sys; data=json.load(sys.stdin); name=sys.argv[1]; print("true" if any(item.get("metadata", {}).get("name") == name for item in data.get("items", [])) else "false")' "$1"
}

version_ge() {
  local have="$1" need="$2"
  local h_major h_minor h_patch n_major n_minor n_patch rest
  have="${have#v}"
  need="${need#v}"
  have="${have%%[-+]*}"
  need="${need%%[-+]*}"
  IFS=. read -r h_major h_minor h_patch rest <<<"$have"
  IFS=. read -r n_major n_minor n_patch rest <<<"$need"
  h_patch="${h_patch:-0}"
  n_patch="${n_patch:-0}"
  [[ "$h_major" =~ ^[0-9]+$ && "$h_minor" =~ ^[0-9]+$ && "$h_patch" =~ ^[0-9]+$ ]] || return 1
  [[ "$n_major" =~ ^[0-9]+$ && "$n_minor" =~ ^[0-9]+$ && "$n_patch" =~ ^[0-9]+$ ]] || return 1
  (( h_major > n_major )) && return 0
  (( h_major < n_major )) && return 1
  (( h_minor > n_minor )) && return 0
  (( h_minor < n_minor )) && return 1
  (( h_patch >= n_patch ))
}

KUBECONFIG_PATH=""
KUBE_CONTEXT=""
MODE=""
DRIVER_OWNER=""
APPLY=false
ENABLE_COMPUTE_DOMAINS=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --kubeconfig)
      [[ $# -ge 2 ]] || die "--kubeconfig requires a path"
      KUBECONFIG_PATH="$2"
      shift 2
      ;;
    --context)
      [[ $# -ge 2 ]] || die "--context requires a name"
      KUBE_CONTEXT="$2"
      shift 2
      ;;
    --mode)
      [[ $# -ge 2 ]] || die "--mode requires device-plugin or dra"
      MODE="$2"
      shift 2
      ;;
    --driver-owner)
      [[ $# -ge 2 ]] || die "--driver-owner requires gpu-operator or preinstalled"
      DRIVER_OWNER="$2"
      shift 2
      ;;
    --apply)
      APPLY=true
      shift
      ;;
    --enable-compute-domains)
      ENABLE_COMPUTE_DOMAINS=true
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ -n "$KUBECONFIG_PATH" ]] || die "--kubeconfig is required"
[[ -n "$KUBE_CONTEXT" ]] || die "--context is required"
[[ -f "$KUBECONFIG_PATH" ]] || die "kubeconfig does not exist: $KUBECONFIG_PATH"
case "$MODE" in
  device-plugin|dra) ;;
  *) die "--mode must be device-plugin or dra" ;;
esac
case "$DRIVER_OWNER" in
  gpu-operator|preinstalled) ;;
  *) die "--driver-owner must be gpu-operator or preinstalled" ;;
esac

require_cmd kubectl
require_cmd helm
if command -v python3 >/dev/null 2>&1 && python3 -c 'import json' >/dev/null 2>&1; then
  PYTHON_JSON=python3
elif command -v python >/dev/null 2>&1 && python -c 'import json' >/dev/null 2>&1; then
  PYTHON_JSON=python
else
  die "required command not found: python3 or python"
fi

KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" --context "$KUBE_CONTEXT")

"${KUBECTL[@]}" config get-contexts "$KUBE_CONTEXT" >/dev/null 2>&1 || die "context not found in kubeconfig: $KUBE_CONTEXT"

server_json="$("${KUBECTL[@]}" version -o json)" || die "failed to query Kubernetes server version"
server_version="$(printf '%s\n' "$server_json" | json_get 'serverVersion.gitVersion')" || die "could not parse Kubernetes server gitVersion"
server_version="${server_version#v}"
[[ -n "$server_version" ]] || die "could not parse Kubernetes server gitVersion"

get_list_count() {
  local resource="$1" json err rc
  err="$(mktemp)"
  if json="$("${KUBECTL[@]}" get "$resource" -o json 2>"$err")"; then
    rm -f "$err"
    printf '%s\n' "$json" | json_items_len
    return 0
  else
    rc=$?
  fi
  if grep -Eqi "(the server doesn't have a resource type|no matches for kind|not found)" "$err"; then
    rm -f "$err"
    printf '0\n'
    return 0
  fi
  printf 'error: failed to read %s: %s\n' "$resource" "$(tr '\n' ' ' <"$err")" >&2
  rm -f "$err"
  return "$rc"
}

get_list_json() {
  local resource="$1" err rc json
  err="$(mktemp)"
  if json="$("${KUBECTL[@]}" get "$resource" -o json 2>"$err")"; then
    rm -f "$err"
    printf '%s\n' "$json"
    return 0
  else
    rc=$?
  fi
  if grep -Eqi "(the server doesn't have a resource type|no matches for kind|not found)" "$err"; then
    rm -f "$err"
    printf '{"items":[]}\n'
    return 0
  fi
  printf 'error: failed to read %s: %s\n' "$resource" "$(tr '\n' ' ' <"$err")" >&2
  rm -f "$err"
  return "$rc"
}

helm_release_exists() {
  local release="$1" namespace="$2" json
  json="$(helm --kubeconfig "$KUBECONFIG_PATH" --kube-context "$KUBE_CONTEXT" list --namespace "$namespace" --filter "^${release}$" --output json)" || die "failed to list Helm releases in namespace $namespace"
  [[ "$(printf '%s\n' "$json" | json_items_len)" != "0" ]]
}

if [[ "$MODE" == "dra" ]]; then
  version_ge "$server_version" "1.34.2" || die "DRA GPU allocation requires Kubernetes >= 1.34.2; server is v$server_version"
  "${KUBECTL[@]}" api-resources --api-group=resource.k8s.io 2>/dev/null | grep -Eq '(^|[[:space:]])deviceclasses([[:space:]]|$)' || die "resource.k8s.io DeviceClass API is not served"
  dra_nodes_json="$("${KUBECTL[@]}" get nodes -l "${DRA_NODE_LABEL}=true" -o json)" || die "failed to read DRA-labeled nodes"
  if [[ "$(printf '%s\n' "$dra_nodes_json" | json_items_len)" == "0" ]]; then
    die "DRA mode requires at least one node labeled ${DRA_NODE_LABEL}=true before install"
  fi
  node_runtimes="$(printf '%s\n' "$dra_nodes_json" | "$PYTHON_JSON" -c 'import json,sys; data=json.load(sys.stdin); [print(node["metadata"]["name"] + "\t" + node["status"]["nodeInfo"]["containerRuntimeVersion"]) for node in data.get("items", [])]')" || die "failed to parse DRA-labeled node runtimes"
  while IFS=$'\t' read -r node runtime; do
    [[ -n "$node" ]] || continue
    [[ "$runtime" == cri-o://* ]] || die "DRA node $node uses $runtime; this DRA path is scoped to CRI-O/CDI GPU nodes"
    crio_version="${runtime#cri-o://}"
    version_ge "$crio_version" "1.27.0" || die "DRA node $node has CRI-O $crio_version; DRA CDI requires CRI-O 1.27+"
  done <<<"$node_runtimes"
fi

if helm_release_exists "$GPU_OPERATOR_RELEASE" "$GPU_OPERATOR_NAMESPACE"; then
  die "Helm release $GPU_OPERATOR_NAMESPACE/$GPU_OPERATOR_RELEASE already exists; refusing takeover or upgrade"
fi
clusterpolicy_count="$(get_list_count clusterpolicies.nvidia.com)" || exit 1
if [[ "$clusterpolicy_count" != "0" ]]; then
  die "an NVIDIA ClusterPolicy already exists; refusing cluster-wide ownership takeover"
fi
gpucluster_count="$(get_list_count gpuclusters.nvidia.com)" || exit 1
if [[ "$gpucluster_count" != "0" ]]; then
  die "an NVIDIA GPUCluster exists; this script uses the 26.3 ClusterPolicy plus standalone DRA path, not GPUCluster"
fi

if [[ "$MODE" == "dra" ]]; then
  if helm_release_exists "$DRA_RELEASE" "$DRA_NAMESPACE"; then
    die "Helm release $DRA_NAMESPACE/$DRA_RELEASE already exists; refusing takeover or upgrade"
  fi
  deviceclasses_json="$(get_list_json deviceclasses.resource.k8s.io)" || exit 1
  deviceclass_exists="$(printf '%s\n' "$deviceclasses_json" | json_has_item_name gpu.nvidia.com)" || die "failed to parse DeviceClass list"
  if [[ "$deviceclass_exists" == "true" ]]; then
    die "NVIDIA DRA DeviceClass gpu.nvidia.com already exists; refusing ownership takeover"
  fi
fi

gpu_operator_sets=()
if [[ "$MODE" == "dra" ]]; then
  gpu_operator_sets+=(--set devicePlugin.enabled=false)
  gpu_operator_sets+=(--set 'driver.manager.env[0].name=NODE_LABEL_FOR_GPU_POD_EVICTION')
  gpu_operator_sets+=(--set-string "driver.manager.env[0].value=${DRA_NODE_LABEL}")
fi
if [[ "$DRIVER_OWNER" == "preinstalled" ]]; then
  gpu_operator_sets+=(--set driver.enabled=false)
  gpu_operator_sets+=(--set toolkit.enabled=false)
fi

gpu_operator_template=(helm template "$GPU_OPERATOR_RELEASE" "$GPU_OPERATOR_CHART" --repo "$GPU_OPERATOR_REPO" --version "$GPU_OPERATOR_VERSION" --namespace "$GPU_OPERATOR_NAMESPACE" "${gpu_operator_sets[@]}")
gpu_operator_apply=(helm --kubeconfig "$KUBECONFIG_PATH" --kube-context "$KUBE_CONTEXT" install "$GPU_OPERATOR_RELEASE" nvidia/gpu-operator --version "$GPU_OPERATOR_VERSION" --namespace "$GPU_OPERATOR_NAMESPACE" --create-namespace "${gpu_operator_sets[@]}")

dra_sets=()
if [[ "$MODE" == "dra" ]]; then
  dra_sets+=(--set gpuResourcesEnabledOverride=true)
  if [[ "$ENABLE_COMPUTE_DOMAINS" == false ]]; then
    dra_sets+=(--set resources.computeDomains.enabled=false)
  fi
  if [[ "$DRIVER_OWNER" == "gpu-operator" ]]; then
    dra_sets+=(--set nvidiaDriverRoot=/run/nvidia/driver)
  else
    dra_sets+=(--set nvidiaDriverRoot=/)
  fi
fi
dra_template=(helm template "$DRA_RELEASE" "$DRA_CHART" --version "$DRA_DRIVER_VERSION" --namespace "$DRA_NAMESPACE" "${dra_sets[@]}")
dra_apply=(helm --kubeconfig "$KUBECONFIG_PATH" --kube-context "$KUBE_CONTEXT" install "$DRA_RELEASE" "$DRA_CHART" --version "$DRA_DRIVER_VERSION" --namespace "$DRA_NAMESPACE" --create-namespace "${dra_sets[@]}")

info "target context: $KUBE_CONTEXT"
info "Kubernetes server: v$server_version"
info "mode: $MODE"
info "driver owner: $DRIVER_OWNER"
info "GPU Operator chart: $GPU_OPERATOR_VERSION"
if [[ "$MODE" == "dra" ]]; then
  info "NVIDIA DRA driver chart: $DRA_DRIVER_VERSION"
fi

if [[ "$APPLY" == false ]]; then
  info "dry run: rendering Helm templates to stdout"
  quote_cmd "${gpu_operator_template[@]}" >&2
  "${gpu_operator_template[@]}"
  if [[ "$MODE" == "dra" ]]; then
    printf '\n---\n'
    quote_cmd "${dra_template[@]}" >&2
    "${dra_template[@]}"
  fi
  info "render succeeded; rerun with --apply to install"
  exit 0
fi

info "apply requested: installing cluster-wide add-ons"
helm repo add nvidia "$GPU_OPERATOR_REPO" >/dev/null
helm repo update nvidia >/dev/null
quote_cmd "${gpu_operator_apply[@]}"
"${gpu_operator_apply[@]}"
if [[ "$MODE" == "dra" ]]; then
  quote_cmd "${dra_apply[@]}"
  "${dra_apply[@]}"
fi
info "install commands completed"
