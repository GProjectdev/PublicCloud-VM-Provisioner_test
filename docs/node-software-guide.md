# Node software bootstrap 가이드

이 문서는 `NodeProvisionNetConfig.spec.softwareConfig.nodeSoftware`로 새 AWS/GCP worker를 만들 때 노드 소프트웨어를 지정하는 절차를 정리한다. 대상은 새로 provision되는 worker뿐이며, 이미 `Ready`인 노드나 SSH로 관리되는 기존 OnPrem 노드를 런타임 업그레이드하지 않는다.

## 적용 범위

- 지원 경로: 새 AWS EC2 worker, 새 GCP Compute Engine worker의 bootstrap 단계.
- 비지원 경로: legacy OnPrem SSH provisioning. `nodeSoftware`가 기본값이 아니면 OnPrem 경로는 VPN/SSH 작업 전에 실패해야 한다.
- OnPrem source 노드는 별도 수동 절차나 미리 준비한 이미지로 런타임을 맞춘다. source build/manual runtime 절차는 [Stateful Migration runtime installation guide](https://github.com/GProjectdev/Stateful-Migration-Operator-with-PV/blob/main/docs/runtime-installation.md)를 따른다. 이 NetConfig가 모든 member cluster에 자동 적용되는 기능은 아니다.
- NetConfig를 수정해도 기존 `Ready` worker에는 적용되지 않는다. 런타임, NFS client, GPU mode, migration package를 바꾸려면 새 worker를 만들어야 한다.

## 먼저 controller와 CRD를 올린다

`nodeSoftware`는 새 CRD/controller가 이해하는 필드다. 기존 cluster에 아직 이 필드가 없는 CRD가 설치되어 있으면 NetConfig 적용 전에 controller image와 CRD를 먼저 갱신한다. 기본 설치 절차는 [`docs/aws-vpc-worker-guide.md`](aws-vpc-worker-guide.md)의 controller 배포 절을 따른다.

```bash
export MEMBER_KUBECONFIG=<AWS_OR_GCP_MEMBER_CLUSTER_KUBECONFIG>
export IMG=<REGISTRY>/remote-cluster-provisioner:<TAG_WITH_NODE_SOFTWARE>

kubectl --kubeconfig "$MEMBER_KUBECONFIG" apply -k config/crd
kubectl --kubeconfig "$MEMBER_KUBECONFIG" wait \
  --for=condition=Established \
  crd/nodeprovisionnetconfigs.ml.dcn.ssu.ac.kr \
  --timeout=120s

kubectl --kubeconfig "$MEMBER_KUBECONFIG" \
  -n remote-cluster-provisioner-system \
  set image deployment/remote-cluster-provisioner manager="$IMG"
kubectl --kubeconfig "$MEMBER_KUBECONFIG" \
  -n remote-cluster-provisioner-system \
  rollout status deployment/remote-cluster-provisioner
```

## 스키마 계약

```yaml
spec:
  softwareConfig:
    kubernetesVersion: "v1.34.2"
    nodeSoftware:
      runtimeProfile: StatefulMigration
      nfsClient: true
      gpuMode: DRA
      migrationRuntime:
        packageURL: https://__REPLACE_PUBLIC_HTTPS_HOST__/__REPLACE_PATH__/stateful-migration-runtime_1.0.0-1_amd64.deb
        packageSHA256: __REPLACE_HEX64_PACKAGE_SHA256__
        crioCommit: __REPLACE_40_HEX_CRIO_COMMIT__
        criuCommit: __REPLACE_40_HEX_CRIU_COMMIT__
        adapterSHA256: __REPLACE_HEX64_ADAPTER_SHA256__
```

| 필드 | 값 / 검증 | 의미 |
| --- | --- | --- |
| `runtimeProfile` | `Standard` 또는 `StatefulMigration`, 기본값 `Standard` | `Standard`는 기본 CRI-O/CRIU bootstrap을 사용한다. `StatefulMigration`은 검토된 migration runtime `.deb`를 설치한다. |
| `nfsClient` | boolean, 기본값 `false` | Ubuntu 기준 `nfs-common` client만 설치한다. NFS server, CSI, StorageClass, PV provisioner를 만들지 않는다. |
| `gpuMode` | `None`, `DevicePlugin`, `DRA`, 기본값 `None` | 노드가 join된 뒤 사용할 GPU addon 모드를 선언한다. GPU checkpoint/restore 성공을 보장하지 않는다. |
| `migrationRuntime.packageURL` | public HTTPS URL, userinfo/query/fragment 금지 | operator가 직접 호스팅한 `.deb` 위치. presigned query URL이나 인증정보가 들어간 URL을 CR에 넣지 않는다. |
| `migrationRuntime.packageSHA256` | 64자리 hex | `.deb` artifact의 SHA-256 digest. 다운로드한 artifact가 신뢰한 digest와 같은지만 확인한다. |
| `migrationRuntime.crioCommit` | 40자리 lowercase hex | operator가 attestation한 검토 대상 CRI-O source commit. |
| `migrationRuntime.criuCommit` | 40자리 lowercase hex | operator가 attestation한 검토 대상 CRIU source commit. |
| `migrationRuntime.adapterSHA256` | 64자리 lowercase hex | operator가 attestation한 Stateful annotation adapter composite digest. |

`runtimeProfile: StatefulMigration`이면 `migrationRuntime`은 필수다. 반대로 `runtimeProfile`이 생략되었거나 `Standard`이면 `migrationRuntime`은 금지된다. Standard에서 무시되는 필드가 아니라 validation 오류로 다뤄야 한다.

`crioCommit`, `criuCommit`, `adapterSHA256`은 operator attestation이다. 이 값들은 package manifest와 NetConfig가 같은 reviewed intent를 가리키는지 확인하기 위한 것이며, 바이너리가 해당 source에서 재현 가능하게 빌드되었거나 annotation adapter가 기능적으로 동작한다는 증명은 아니다. `packageSHA256`도 trusted artifact integrity 확인용이다. source build, patch 적용, adapter 기능 검증은 별도 review/test 절차에서 끝낸 뒤 이 패키지에 반영해야 한다.

## `.deb` 패키지 생성과 호스팅

StatefulMigration runtime은 기본 `apt install cri-o criu`를 대체하는 임의 설치 경로가 아니다. 검토된 CRI-O fork, 검토된 CRIU fork, Stateful annotation adapter, CUDA checkpoint helper, provenance manifest를 담은 Debian package를 만들어 public HTTPS 위치에 올린 뒤 그 digest와 source commit attestation을 NetConfig에 고정한다.

source build와 수동 runtime 설치 기준은 [Stateful Migration runtime installation guide](https://github.com/GProjectdev/Stateful-Migration-Operator-with-PV/blob/main/docs/runtime-installation.md)를 따른다. 이 NodeProvisioner guide는 이미 검토된 local artifact를 `.deb`로 포장하고 새 cloud worker bootstrap에서 설치하는 단계만 다룬다.

입력 바이너리와 plugin은 다음 경로를 기준으로 준비한다.

- `/usr/local/bin/crio`
- `/usr/local/sbin/criu`
- `/usr/local/bin/cuda-checkpoint`
- `/usr/local/lib/criu/cuda_plugin.so`

빌드와 호스팅 흐름:

```bash
# 1. 검토된 source build 결과를 고정 경로에 준비한다.
test -x /usr/local/bin/crio
test -x /usr/local/sbin/criu
test -x /usr/local/bin/cuda-checkpoint
test -f /usr/local/lib/criu/cuda_plugin.so

# 2. 선택: dpkg-deb 실행 없이 manifest staging과 입력 검증만 먼저 수행한다.
python3 scripts/build-stateful-runtime-package.py \
  --crio /usr/local/bin/crio \
  --criu /usr/local/sbin/criu \
  --cuda-checkpoint /usr/local/bin/cuda-checkpoint \
  --cuda-plugin /usr/local/lib/criu/cuda_plugin.so \
  --output-dir ./dist \
  --package-version 1.0.0-1 \
  --arch amd64 \
  --k8s-minor 1.34 \
  --crio-commit "$CRIO_SHA" \
  --criu-commit "$CRIU_SHA" \
  --adapter-sha256 "$ADAPTER_SHA" \
  --confirm-reviewed-adapter \
  --dry-run \
  --staged-manifest ./dist/runtime.json

# 3. .deb를 생성한다. 출력 파일명은 stateful-migration-runtime_<version>_<arch>.deb 형식이다.
python3 scripts/build-stateful-runtime-package.py \
  --crio /usr/local/bin/crio \
  --criu /usr/local/sbin/criu \
  --cuda-checkpoint /usr/local/bin/cuda-checkpoint \
  --cuda-plugin /usr/local/lib/criu/cuda_plugin.so \
  --output-dir ./dist \
  --package-version 1.0.0-1 \
  --arch amd64 \
  --k8s-minor 1.34 \
  --crio-commit "$CRIO_SHA" \
  --criu-commit "$CRIU_SHA" \
  --adapter-sha256 "$ADAPTER_SHA" \
  --confirm-reviewed-adapter

# 4. CR에 넣을 package digest를 계산한다.
sha256sum ./dist/stateful-migration-runtime_1.0.0-1_amd64.deb

# 5. 운영자가 관리하는 public HTTPS 위치에 .deb를 업로드한다.
# 예: https://packages.example.org/stateful/stateful-migration-runtime_1.0.0-1_amd64.deb
```

`--confirm-reviewed-adapter`는 adapter patch/helper composite가 packaging 전에 review되었다는 operator attestation이다. 이 flag 자체가 기능 검증을 수행하지 않는다. `--arch`는 `amd64` 또는 `arm64`이며, builder는 입력 ELF architecture가 선택한 arch와 맞는지 확인한다.

## 패키지 호환성 경계

생성된 `.deb`는 네 개 ELF artifact(`/usr/local/bin/crio`, `/usr/local/sbin/criu`, `/usr/local/bin/cuda-checkpoint`, `/usr/local/lib/criu/cuda_plugin.so`)와 runtime manifest를 설치한다. builder는 이 ELF들의 공유 라이브러리를 자동 탐색하거나 vendor하지 않는다.

운영자는 package를 빌드한 환경과 설치할 worker의 Ubuntu release/architecture를 맞춰야 한다. `ldd`는 운영자가 신뢰한 자기 빌드 산출물에 대해서만 확인한다. 추가 distro library가 필요하면 `--depends`로 Debian dependency를 명시한다. 이때 기본 의존성인 `cri-o, criu, python3`을 잃지 않도록 함께 넣는다.

```bash
python3 scripts/build-stateful-runtime-package.py \
  --crio /usr/local/bin/crio \
  --criu /usr/local/sbin/criu \
  --cuda-checkpoint /usr/local/bin/cuda-checkpoint \
  --cuda-plugin /usr/local/lib/criu/cuda_plugin.so \
  --output-dir ./dist \
  --package-version 1.0.0-1 \
  --arch amd64 \
  --k8s-minor 1.34 \
  --crio-commit "$CRIO_SHA" \
  --criu-commit "$CRIU_SHA" \
  --adapter-sha256 "$ADAPTER_SHA" \
  --confirm-reviewed-adapter \
  --depends "cri-o, criu, python3, __REPLACE_EXTRA_DISTRO_LIB__"
```

`cuda_plugin.so`의 `dlopen` 의존성은 pre-join bootstrap에서 완전히 검증되지 않는다. GPU node는 join 후 GPU Operator/driver/toolkit 설치가 끝난 다음 실제 CUDA checkpoint/restore smoke test를 반드시 수행한다.

이 저장소 문서는 prebuilt package가 이미 공개되어 있다고 주장하지 않는다. `packageURL`에는 위 절차로 생성하고 검토한 `.deb`를 운영자가 직접 호스팅한 URL만 넣는다.

## NetConfig 작성

`NodeProvision`과 같은 namespace에는 `NodeProvisionNetConfig`가 정확히 1개만 있어야 한다. 이미 `aws-vpc-netconfig`가 있다면 `aws-vpc-netconfig-stateful`을 두 번째로 만들지 말고 기존 객체를 수정한다. 새 sample 파일을 그대로 적용할 때도 `metadata.name`을 namespace의 단일 NetConfig 이름으로 맞춘다.

```bash
cp config/samples/aws-vpc-netconfig-stateful.yaml /tmp/aws-vpc-netconfig.yaml
# metadata.name, spec.clusterName, kubernetesVersion, packageURL, digest, commit 값을 실제 값으로 수정한다.
```

반드시 바꿀 값:

- `metadata.name`: 해당 namespace의 단일 NetConfig 이름. 기존 가이드 기본값은 `aws-vpc-netconfig`다.
- `spec.clusterName`
- `spec.softwareConfig.kubernetesVersion`
- `packageURL`: public HTTPS `.deb`, query/userinfo 없음
- `packageSHA256`: `sha256sum` 결과
- `crioCommit`, `criuCommit`, `adapterSHA256`: operator가 attestation한 값이며 package manifest와 일치해야 한다.

적용 예:

```bash
kubectl --kubeconfig "$MEMBER_KUBECONFIG" apply -f /tmp/aws-vpc-netconfig.yaml
kubectl --kubeconfig "$MEMBER_KUBECONFIG" get nodeprovisionnetconfig aws-vpc-netconfig -o yaml
```

## NodeProvision 실행

AWS VPC worker는 기존 가이드의 NodeProvision sample을 사용하되, 같은 namespace에 NetConfig가 정확히 1개만 존재해야 한다. GPU DDP demo처럼 GPU runtime mode를 지정하려면 NodeProvision도 GPU worker로 명확히 선언한다.

필수 조건:

- `hardwareType: gpu`
- `nodeLabel: gpu`
- `nodeSoftware.gpuMode: DevicePlugin` 또는 `DRA`

예:

```yaml
spec:
  hardwareType: gpu
  nodeLabel: gpu
  networkMode: VPC
```

CPU worker에 `gpuMode: DevicePlugin` 또는 `DRA`를 지정하면 bootstrap validation에서 실패해야 한다.

## 설치 순서와 검증

cloud-init은 nodeSoftware 작업을 CRI-O service start와 `kubeadm join` 전에 실행한다. 이미 `/etc/kubernetes/kubelet.conf`가 있는 노드에서는 새 노드용 software bootstrap을 거부한다.

새 worker가 생성된 뒤 확인한다.

```bash
kubectl --kubeconfig "$MEMBER_KUBECONFIG" get node <WORKER_NODE_NAME> -o wide
kubectl --kubeconfig "$MEMBER_KUBECONFIG" describe node <WORKER_NODE_NAME>
```

노드 내부 확인이 필요한 경우:

```bash
ssh ubuntu@<WORKER_PUBLIC_OR_PRIVATE_IP>
crio --version
criu --version
test -x /usr/local/bin/cuda-checkpoint
test -f /usr/local/lib/criu/cuda_plugin.so
test -f /usr/local/share/stateful-migration/runtime.json
sha256sum /usr/local/share/stateful-migration/runtime.json
command -v mount.nfs
```

pre-join custom install이 확인하는 것은 package provenance, 설치된 binary, CRI-O version/config, adapter digest 범위다. 이것은 `GPUReady` 신호가 아니다. GPU driver/operator/toolkit은 노드 join 이후 cluster addon 단계에서 확인한다.

## GPU addon 경계

`gpuMode`는 노드가 어떤 GPU addon 경로를 기대하는지 기록한다.

- `None`: GPU addon 기대 없음.
- `DevicePlugin`: NVIDIA device plugin 또는 GPU Operator의 device plugin 경로를 기대한다.
- `DRA`: Kubernetes `>= 1.34.2`, CRI-O/CDI, compatible NVIDIA driver/toolkit/CDI/DRA 구성이 필요하다.

GPU addon 설치는 VM별 cloud-init이 아니라 cluster-wide 작업이다. 이 repo의 GPU addon 절차는 [`docs/gpu-addons.md`](gpu-addons.md)와 `scripts/install-gpu-addons.sh`를 따른다. 파일 mode가 `100644`일 수 있으므로 `bash`로 실행한다. DRA mode에서는 대상 GPU 노드에 `nvidia.com/dra-kubelet-plugin=true` label을 붙인 뒤 dry-run render를 먼저 확인한다.

```bash
kubectl --kubeconfig "$MEMBER_KUBECONFIG" label node <GPU_NODE_NAME> nvidia.com/dra-kubelet-plugin=true --overwrite

bash scripts/install-gpu-addons.sh \
  --kubeconfig "$MEMBER_KUBECONFIG" \
  --context <WORKLOAD_CLUSTER_CONTEXT> \
  --mode dra \
  --driver-owner gpu-operator
```

실제 설치는 render 결과와 선행 조건을 확인한 뒤 `--apply`를 붙여 실행한다. GPU diagnostics와 restore 검증은 addon 설치 이후 별도 단계다.

## 주의 사항

- 이 기능은 all-member 자동 rollout이 아니다.
- NetConfig update는 runtime rollout이 아니다.
- source와 target이 StatefulMigration checkpoint/archive를 공유하려면 같은 durable backend를 사용해야 한다.
- `nfsClient: true`는 NFS client 설치만 의미한다. 공유 NFS server와 PV/StorageClass 구성은 별도 준비한다.
- `Node Ready`만으로 StatefulMigration runtime, NFS, GPU restore 준비 완료를 주장하지 않는다.
- source commit과 adapter hash는 attestation이며 source build 증명이나 기능 검증 결과가 아니다.
- 이 문서 작업에서는 실제 AWS/GCP cluster 생성, `.deb` hosting, GPU addon apply, checkpoint/restore E2E를 수행하지 않았다.