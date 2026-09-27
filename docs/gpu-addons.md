# Cluster GPU Add-ons

This repository provisions worker nodes. GPU add-ons are cluster-wide ownership, so they are installed once from an operator workstation or CI job with a kubeconfig, not by every VM and not by a management-runtime controller.

Use `scripts/install-gpu-addons.sh` to render or install the pinned add-on set. The script requires `--kubeconfig` and `--context`; it never relies on the current kubectl context. The default mode is a dry run that runs `helm template` and writes rendered YAML to stdout for review. Real cluster mutation requires `--apply`.

The workstation running the script needs `bash`, `kubectl`, `helm`, and `python3` or `python`. Python is used only as a structured JSON parser for Kubernetes and Helm responses.

## Supported Paths

### Device Plugin Mode

Use this when workloads request legacy extended resources such as `nvidia.com/gpu`.

Pinned install:

```bash
bash scripts/install-gpu-addons.sh \
  --kubeconfig /path/to/kubeconfig \
  --context workload-cluster \
  --mode device-plugin \
  --driver-owner gpu-operator
```

If the NodeProvisioner image already installs and owns the NVIDIA host driver and NVIDIA Container Toolkit, prevent the GPU Operator from configuring them a second time:

```bash
bash scripts/install-gpu-addons.sh \
  --kubeconfig /path/to/kubeconfig \
  --context workload-cluster \
  --mode device-plugin \
  --driver-owner preinstalled
```

With `--driver-owner preinstalled`, the script renders or installs the GPU Operator with `driver.enabled=false` and `toolkit.enabled=false`.

### DRA Mode

Use this when workloads allocate GPUs through Kubernetes Dynamic Resource Allocation. This repo intentionally uses the conservative GPU Operator 26.3 ClusterPolicy plus standalone NVIDIA DRA driver chart path. It does not use the newer GPU Operator 26.7 `GPUCluster` workflow.

Prerequisites for GPU DRA allocation:

- Kubernetes server version `v1.34.2` or newer.
- DRA-labeled GPU nodes use CRI-O `v1.27` or newer so CDI is available through CRI-O. Non-GPU control-plane nodes are not part of this runtime gate.
- NVIDIA driver and toolkit ownership is explicit: `--driver-owner gpu-operator` or `--driver-owner preinstalled`.
- GPU nodes are labeled `nvidia.com/dra-kubelet-plugin=true` before install.
- The cluster serves `resource.k8s.io` `DeviceClass` APIs.

For this Operator-based DRA path, use NVIDIA driver 580 or later, Container Toolkit 1.18.0 or later, and NFD 0.18.2 or later. Check GPU model support against the [NVIDIA prerequisites](https://dra-driver-nvidia-gpu.sigs.k8s.io/docs/prerequisites/). With `--driver-owner preinstalled`, these host dependencies are your responsibility; the script does not SSH into nodes to validate them. Operator-managed driver/toolkit installation happens after node join. A successful Helm command is not GPU readiness or checkpoint/restore validation.

Dry-run render:

```bash
bash scripts/install-gpu-addons.sh \
  --kubeconfig /path/to/kubeconfig \
  --context workload-cluster \
  --mode dra \
  --driver-owner gpu-operator
```

Apply after reviewing the rendered ownership and prerequisites:

```bash
bash scripts/install-gpu-addons.sh \
  --kubeconfig /path/to/kubeconfig \
  --context workload-cluster \
  --mode dra \
  --driver-owner gpu-operator \
  --apply
```

In DRA mode, the GPU Operator chart is pinned to `v26.3.3`, disables the legacy NVIDIA device plugin, and sets `NODE_LABEL_FOR_GPU_POD_EVICTION=nvidia.com/dra-kubelet-plugin`. The standalone NVIDIA DRA driver chart is pinned to `0.5.0`, sets `gpuResourcesEnabledOverride=true`, and disables ComputeDomains by default. Add `--enable-compute-domains` only when the cluster is prepared for that API surface.

When the GPU Operator owns the driver, the DRA driver uses `nvidiaDriverRoot=/run/nvidia/driver`. When the host image owns the driver, the script uses `nvidiaDriverRoot=/` and disables GPU Operator driver/toolkit management.

## Ownership and Refusal Rules

The installer is deliberately non-takeover:

- It refuses to run if the GPU Operator Helm release already exists.
- It refuses to run if an NVIDIA `ClusterPolicy` already exists.
- It refuses to run if an NVIDIA `GPUCluster` exists.
- In DRA mode, it refuses to run if the standalone DRA Helm release or `gpu.nvidia.com` `DeviceClass` already exists.

This avoids silent upgrades or ownership changes. If an operator already manages the cluster, update that release through its existing runbook instead of using this bootstrap script.

## What This Does Not Do

- It does not install Helm, kubectl, CRI-O, CRIU, NFS tools, GPU drivers, or toolkit packages directly on every VM. With `--driver-owner gpu-operator`, the GPU Operator can still deploy its normal driver/toolkit management workloads after `--apply`.
- It does not reconcile node runtime configuration from the management plane.
- It does not auto-upgrade an existing GPU Operator, DRA driver, `ClusterPolicy`, or `GPUCluster`.
- It does not label nodes automatically; label only the intended GPU nodes with `nvidia.com/dra-kubelet-plugin=true` before DRA install.

The worker image or node software bootstrap remains responsible for CRI-O, CRIU, CDI-capable runtime configuration, NFS utilities, and any preinstalled driver/toolkit path chosen by `--driver-owner preinstalled`.

## Official References

- NVIDIA GPU Operator 26.3 getting started: <https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/26.3/getting-started.html>
- NVIDIA GPU Operator 26.3 DRA installation: <https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/26.3/dra-intro-install.html>
- NVIDIA DRA driver 0.5 installation: <https://dra-driver-nvidia-gpu.sigs.k8s.io/docs/install/>
- NVIDIA DRA driver prerequisites: <https://dra-driver-nvidia-gpu.sigs.k8s.io/docs/prerequisites/>
- NVIDIA DRA driver Helm values: <https://dra-driver-nvidia-gpu.sigs.k8s.io/docs/reference/helm-values/>
- Kubernetes Dynamic Resource Allocation: <https://kubernetes.io/docs/concepts/resource-management/dynamic-resource-allocation/>
- CRI-O CDI configuration reference: <https://github.com/cri-o/cri-o/blob/main/docs/crio.conf.5.md>
