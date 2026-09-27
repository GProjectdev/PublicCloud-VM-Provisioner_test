# AWS Kubernetes 1.37 재설치 후 NodeProvisioner 복구

MGMT Ubuntu 터미널에서 아래 순서로 실행한다. AWS control plane/CNI는 준비됐고 Worker는 없으며 runtime .deb는 생성된 상태를 기준으로 한다. Controller는 NetConfig의 버전을 읽으므로 버전을 코드에 고정하지 않는다.

## 1. 소스와 새 AWS 연결 확인

```bash
cd /root/hybridspot-validation/provisioner
git switch In-aws-create-WorkerNode
git pull --ff-only origin In-aws-create-WorkerNode
sudo apt-get update
sudo apt-get install -y curl jq
export RENDER=/root/hybridspot-validation/rendered
mkdir -p "$RENDER"
kubectl config get-contexts
export AWS_CONTEXT='실제_새_AWS_context_이름'
kubectl --context="$AWS_CONTEXT" get nodes -o wide
export K8S_VERSION="$(kubectl --context="$AWS_CONTEXT" version -o json | jq -r '.serverVersion.gitVersion')"
printf '%s\n' "$K8S_VERSION"
kubectl --context="$AWS_CONTEXT" -n kube-public get configmap cluster-info -o jsonpath='{.data.kubeconfig}'
kubectl --context="$AWS_CONTEXT" label node 실제_CONTROL_PLANE_NODE_NAME ml.dcn.ssu.ac.kr/master=true --overwrite
```

통합 kubeconfig의 AWS context를 새 CA/인증서/endpoint로 갱신해야 한다. 서버 버전이 v1.37.x이며 control plane이 Ready인지 확인한다. kubeadm으로 만든 self-managed 클러스터를 기준으로 한다. cluster-info의 server는 Worker subnet에서 접근 가능한 새 AWS API 주소여야 한다. 이전 join token은 재사용하지 않는다. 재설치 전 EC2가 남았다면 AWS 콘솔에서 확인한다. 소실된 CR의 finalizer는 이전 EC2를 정리할 수 없다.

## 2. .deb 확인 및 호스팅

```bash
export DEB="$PWD/dist/stateful-migration-runtime_1.0.0-1_amd64.deb"
test -f "$DEB"
export PACKAGE_SHA="$(sha256sum "$DEB" | awk '{print $1}')"
EXTRACT_DIR="$(mktemp -d)"
dpkg-deb -x "$DEB" "$EXTRACT_DIR"
export MANIFEST="$EXTRACT_DIR/usr/local/share/stateful-migration/runtime.json"
jq . "$MANIFEST"
jq -e '.kubernetesMinor == "1.37"' "$MANIFEST"
dpkg-deb -f "$DEB" Architecture Depends
```

Architecture는 amd64여야 한다. commit/hash는 이 실제 manifest에서 가져온다. 패키징 성공만으로 adapter 적용이나 GPU 복원까지 검증되지는 않는다.

공개 GitHub 저장소의 Releases에서 릴리스를 만들고 .deb를 asset으로 첨부하거나 public HTTPS 서버에 업로드한다. 파일 다운로드 URL을 아래에 지정한다. GitHub blob 페이지 주소, 인증정보/query가 포함된 URL은 사용하지 않는다.

```bash
export PACKAGE_URL='https://실제_다운로드_주소/stateful-migration-runtime_1.0.0-1_amd64.deb'
curl -fL --proto '=https' --proto-redir '=https' "$PACKAGE_URL" -o "$RENDER/runtime-download.deb"
printf '%s  %s\n' "$PACKAGE_SHA" "$RENDER/runtime-download.deb" | sha256sum -c -
curl -fL https://pkgs.k8s.io/core:/stable:/v1.37/deb/Release -o "$RENDER/kubernetes-release"
curl -fL https://download.opensuse.org/repositories/isv:/cri-o:/stable:/v1.37/deb/Release -o "$RENDER/crio-release"
```

실패하면 Worker 생성 전에 해결한다. 현재 bootstrap은 기본 cri-o/criu를 설치한 후 custom .deb를 설치하므로 1.37 패키지 저장소도 필요하다. Worker subnet에서도 위 주소와 OS mirror/registry로 egress가 가능해야 한다.

## 3. Buildah 이미지 빌드와 신규 설치

아래 태그는 이 단계에서 직접 빌드해 게시하는 태그다.

```bash
export IMG=docker.io/jeongseungjun/hybrid-spot-vm-system:node-provisioner_k137-v1
buildah login docker.io
buildah build --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 -t "$IMG" .
buildah push "$IMG" "docker://$IMG"
kubectl --context="$AWS_CONTEXT" apply -k config/crd
kubectl --context="$AWS_CONTEXT" wait --for=condition=Established --timeout=120s \
  crd/remoteclusters.infra.dcn.ssu.ac.kr \
  crd/nodeprovisions.ml.dcn.ssu.ac.kr \
  crd/nodeprovisionnetconfigs.ml.dcn.ssu.ac.kr
DEPLOY_DIR="$(mktemp -d)"
cp -R deploy "$DEPLOY_DIR/deploy"
cat >> "$DEPLOY_DIR/deploy/kustomization.yaml" <<EOF
images:
  - name: ghcr.io/<ORG>/remote-cluster-provisioner
    newName: ${IMG%:*}
    newTag: ${IMG##*:}
EOF
kubectl kustomize "$DEPLOY_DIR/deploy" > "$RENDER/nodeprovisioner-install.yaml"
kubectl --context="$AWS_CONTEXT" apply -f "$RENDER/nodeprovisioner-install.yaml"
kubectl --context="$AWS_CONTEXT" -n remote-cluster-provisioner-system rollout status deployment/remote-cluster-provisioner --timeout=180s
```

deploy/는 control-plane taint를 허용하여 Worker가 없어도 배치할 수 있다. 비공개 이미지면 imagePullSecret을 Deployment에 연결한다. 새 클러스터에서는 set image만으로 Deployment가 생성되지 않는다.

## 4. Secret과 NetConfig

```bash
kubectl --context="$AWS_CONTEXT" create namespace fluidcr-demo --dry-run=client -o yaml |
  kubectl --context="$AWS_CONTEXT" apply -f -
```

/secure/aws-node-credentials.yaml을 다음 형식으로 준비한다. 실제 키는 Git에 넣지 않는다.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: aws-node-credentials
  namespace: fluidcr-demo
type: Opaque
stringData:
  awsAccessKeyId: "실제_ACCESS_KEY"
  awsSecretAccessKey: "실제_SECRET_KEY"
  # 임시 자격증명이면 awsSessionToken도 지정
```

```bash
kubectl --context="$AWS_CONTEXT" apply -f /secure/aws-node-credentials.yaml
export CRIO_SHA="$(jq -r .crioCommit "$MANIFEST")"
export CRIU_SHA="$(jq -r .criuCommit "$MANIFEST")"
export ADAPTER_SHA="$(jq -r .adapterSHA256 "$MANIFEST")"
cp config/samples/aws-vpc-netconfig-stateful-1.37.yaml "$RENDER/netconfig.yaml"
printf 'version=%s\nURL=%s\npackageSHA256=%s\ncrioCommit=%s\ncriuCommit=%s\nadapterSHA256=%s\n' \
  "$K8S_VERSION" "$PACKAGE_URL" "$PACKAGE_SHA" "$CRIO_SHA" "$CRIU_SHA" "$ADAPTER_SHA"
nano "$RENDER/netconfig.yaml"
```

출력한 실제 값으로 kubernetesVersion과 migrationRuntime의 5개 필드를 수정한다. namespace는 fluidcr-demo, clusterName은 aws다. 같은 namespace의 NetConfig는 정확히 1개만 둔다.

```bash
kubectl --context="$AWS_CONTEXT" apply --dry-run=server -f "$RENDER/netconfig.yaml"
kubectl --context="$AWS_CONTEXT" apply -f "$RENDER/netconfig.yaml"
kubectl --context="$AWS_CONTEXT" -n fluidcr-demo get nodeprovisionnetconfigs
```

## 5. GPU Worker 생성

```bash
cp config/samples/aws-vpc-worker.yaml "$RENDER/aws-worker.yaml"
nano "$RENDER/aws-worker.yaml"
```

- metadata.namespace와 spec.credentialsRef.namespace를 둘 다 fluidcr-demo로 변경한다.
- region, awsConfig.availabilityZone/vpcId/subnetId/securityGroupIds를 새 AWS 환경에 맞춘다.
- awsConfig.ami에 빌드 환경과 같은 Ubuntu release의 amd64 AMI를 명시한다. 생략하면 Ubuntu 22.04를 탐색한다.
- GPU instanceType을 사용하고 첫 검증은 샘플의 marketType: OnDemand로 수행한다.
- networkMode: VPC, hardwareType: gpu, nodeLabel: gpu를 유지한다.
- public subnet이면 associatePublicIP: true, private subnet이면 NAT 경유 egress를 준비한다.

```bash
kubectl --context="$AWS_CONTEXT" apply --dry-run=server -f "$RENDER/aws-worker.yaml"
kubectl --context="$AWS_CONTEXT" apply -f "$RENDER/aws-worker.yaml"
kubectl --context="$AWS_CONTEXT" -n fluidcr-demo get nodeprovisions -w
# Ctrl+C로 감시 종료 후
kubectl --context="$AWS_CONTEXT" get nodes -o wide
```

첫 Worker 확인 후 2대가 필요하면 파일을 복사하고 metadata.name과 awsConfig.tags.Name을 aws-vpc-worker-002로 바꾼다. Secret/NetConfig는 공유한다. NodeProvision 삭제는 EC2 삭제를 실행하므로 실패 시 먼저 로그를 확인한다.

## 6. 런타임과 GPU 검증

Worker에서 실행한다.

```bash
/usr/local/bin/crio --version
/usr/local/sbin/criu --version
command -v mount.nfs
cat /usr/local/share/stateful-migration/runtime.json
sudo systemctl status crio --no-pager
sudo journalctl -u crio -n 100 --no-pager
```

gpuMode는 의도만 기록하며 driver/toolkit/DRA는 별도 설치다. MGMT에서 Helm을 준비하고 [GPU addon 가이드](gpu-addons.md)의 전제 조건을 확인한다.

```bash
kubectl --context="$AWS_CONTEXT" label node 실제_GPU_WORKER명 nvidia.com/dra-kubelet-plugin=true --overwrite
bash scripts/install-gpu-addons.sh --kubeconfig "$HOME/.kube/config" \
  --context "$AWS_CONTEXT" --mode dra --driver-owner gpu-operator
# render 성공 후 같은 명령에 --apply를 붙여 설치
```

모든 GPU Worker에 라벨을 붙이고 실제 kubeconfig 경로를 사용한다. driver/toolkit/DRA Pod와 GPU workload 할당을 확인한 뒤 checkpoint/restore를 검증한다. NFS server/PV/StorageClass와 Stateful Migration Member controller도 별도 재설치가 필요하다.

Karmada의 aws 등록은 이전 CA/자격증명을 가리킬 수 있다. 새 AWS kubeconfig로 등록을 갱신하고 Karmada context에서 kubectl get clusters로 Ready를 확인한다. placement는 사용자가 변경한다.

실패 시 MGMT에서 Controller 로그/NodeProvision events를, Worker에서 /var/log/node-bootstrap.log, /var/log/cloud-init-output.log와 journalctl -u crio -u kubelet을 확인한다. 로컬 검증과 실제 AWS join/GPU 복원 검증은 별도다.
