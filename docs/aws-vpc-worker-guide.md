# AWS VPC Worker NodeProvision 실전 가이드

이 문서는 `NodeProvision.spec.networkMode: VPC`로 AWS EC2 Worker를 만들고, VPN 없이 AWS VPC 네트워크로 Kubernetes 클러스터에 조인시키는 절차를 정리한다. 기존 기본 동작은 WireGuard 방식이다. `networkMode`를 생략하면 기존 WireGuard 경로를 유지하고, VPC Worker를 만들 때만 `networkMode: VPC`를 명시한다.

## 1. 적용 범위

- Controller는 AWS member cluster에 배포한다.
- `kubectl`은 Karmada kubeconfig가 아니라 AWS member cluster kubeconfig를 명시적으로 사용한다.
- Worker EC2는 지정한 VPC/Subnet/Security Group 안에 생성되고, VPC user-data는 IMDSv2에서 primary private IP를 읽어 Kubernetes Node IP로 사용한다. IMDSv2 requirement는 AWS의 새 인스턴스 metadata option 설정 방식과 맞춘다: https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/configuring-IMDS-new-instances.html
- VPC 모드 Worker에는 VPN Secret, VPN peer, WireGuard client 설정이 필요 없다.
- `NodeProvisionNetConfig`는 여전히 필요하며, Controller는 `NodeProvision` CR과 같은 namespace에서 정확히 1개를 선택한다. `spec.softwareConfig.kubernetesVersion`은 master와 같은 버전으로 맞춘다.
- GPU Operator는 별도 설치/운영 대상이다. 이 가이드는 GPU Operator를 실제 배포하지 않는다.

## 2. 사전 조건

AWS member cluster에서 다음 조건을 먼저 만족해야 한다.

- AWS member cluster의 master/control-plane Node가 `Ready` 상태이다.
- CNI가 master와 새 Worker 사이의 양방향 Pod/Node 통신을 제공한다.
- `kube-public/cluster-info`의 Kubernetes API endpoint가 Worker subnet에서 도달 가능한 master private IP 또는 내부 endpoint를 가리킨다.
- 새 Worker subnet에서 Kubernetes 패키지 저장소, container registry, OS 패키지 mirror로 나가는 인터넷 접근이 가능하다.
- 새 Worker가 master의 Kubernetes API endpoint에 private network로 접근할 수 있다.
- Controller image를 새 코드로 빌드하고 registry에 push했다.
- AWS credentials Secret이 존재한다. IAM role/IRSA/instance profile 등 AWS SDK 기본 credential chain을 쓰더라도 현재 Controller는 Secret 참조를 요구하므로 빈 `data: {}` Secret을 만든다.

## 3. kubeconfig와 master label 확인

모든 명령은 AWS member cluster kubeconfig로 실행한다. Karmada context에서 실행하면 Controller와 CR이 다른 클러스터에 생성될 수 있다.

```bash
export KUBECONFIG=<AWS_MEMBER_CLUSTER_KUBECONFIG>
kubectl config current-context
kubectl get nodes -o wide
```

master/control-plane Node에 명확한 label을 붙인다. 이미 label이 있으면 덮어써도 된다.

```bash
kubectl label node <MASTER_NODE_NAME> node-role.kubernetes.io/control-plane= --overwrite
kubectl label node <MASTER_NODE_NAME> ml.dcn.ssu.ac.kr/master=true --overwrite
```

## 4. Controller 배포

이 저장소에서 AWS member cluster에 적용할 management-node 배포 overlay는 `deploy/`이다. 이 overlay는 다음 이름으로 렌더링된다.

- Namespace: `remote-cluster-provisioner-system`
- ServiceAccount: `remote-cluster-provisioner`
- Deployment: `remote-cluster-provisioner`
- Container: `manager`
- 기본 image 자리: `ghcr.io/<ORG>/remote-cluster-provisioner:latest`

`config/default` 또는 `make deploy` 경로는 여기서 사용하지 않는다. Kubebuilder 기본 base는 master-only cluster의 control-plane/master `NoSchedule` taint를 견디는 toleration 구성이 아니므로, single master 위에 Controller를 올리는 검토/실험에서는 `deploy/` overlay를 렌더링해 적용한다.

새 image를 빌드하고 push한다.

```bash
export IMG=<REGISTRY>/remote-cluster-provisioner:<TAG>
make docker-build IMG="$IMG"
docker push "$IMG"
```

먼저 CRD를 AWS member cluster에 설치하고, manager가 watch하는 CRD 3개가 `Established`가 될 때까지 기다린다.

```bash
export AWS_KUBECONFIG=<AWS_MEMBER_CLUSTER_KUBECONFIG>

kubectl --kubeconfig "$AWS_KUBECONFIG" apply -k config/crd

kubectl --kubeconfig "$AWS_KUBECONFIG" wait \
  --for=condition=Established \
  crd/remoteclusters.infra.dcn.ssu.ac.kr \
  crd/nodeprovisions.ml.dcn.ssu.ac.kr \
  crd/nodeprovisionnetconfigs.ml.dcn.ssu.ac.kr \
  --timeout=120s
```

`deploy/`를 임시 디렉터리에 복사해 image만 바꾼 뒤 렌더링한다. 저장소의 `deploy/` 파일은 수정하지 않는다.

```bash
RENDER_DIR="$(mktemp -d)"
cp -R deploy "$RENDER_DIR/deploy"

(
  cd "$RENDER_DIR/deploy"
  kustomize edit set image 'ghcr.io/<ORG>/remote-cluster-provisioner'="$IMG"
)

kustomize build "$RENDER_DIR/deploy" \
  > "$RENDER_DIR/remote-cluster-provisioner-deploy.yaml"
```

적용 전 렌더링 결과가 의도한 namespace, deployment, image를 가리키는지 확인한다.

```bash
grep -E 'name: remote-cluster-provisioner-system|name: remote-cluster-provisioner$|image: ' \
  "$RENDER_DIR/remote-cluster-provisioner-deploy.yaml"
```

AWS member cluster kubeconfig로만 적용한다. Karmada kubeconfig를 사용하지 않는다.

```bash
kubectl --kubeconfig "$AWS_KUBECONFIG" apply \
  -f "$RENDER_DIR/remote-cluster-provisioner-deploy.yaml"

kubectl --kubeconfig "$AWS_KUBECONFIG" \
  -n remote-cluster-provisioner-system \
  rollout status deployment/remote-cluster-provisioner

kubectl --kubeconfig "$AWS_KUBECONFIG" \
  -n remote-cluster-provisioner-system \
  get deploy,pods -o wide
```

## 5. AWS credentials Secret

정적 access key를 쓰는 경우 값은 로컬 파일 또는 외부 Secret 관리 도구에서 주입한다. 실제 credential 값을 Git에 저장하지 않는다.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: aws-node-credentials
  namespace: default
type: Opaque
stringData:
  awsAccessKeyId: "<AWS_ACCESS_KEY_ID>"
  awsSecretAccessKey: "<AWS_SECRET_ACCESS_KEY>"
  # awsSessionToken: "<AWS_SESSION_TOKEN>"
```

현재 Controller의 `ResolveAWSCredentials` 구현은 Secret data key로 `awsAccessKeyId`, `awsSecretAccessKey`, 선택적 `awsSessionToken`을 읽는다. `AWS_ACCESS_KEY_ID` 같은 대문자 환경변수식 key 이름은 이 Secret resolver가 읽지 않는다.

AWS SDK credential chain을 쓰는 경우에도 현재 `NodeProvision.spec.credentialsRef`가 필요하므로 빈 Secret을 둔다.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: aws-node-credentials
  namespace: default
type: Opaque
data: {}
```

VPC 모드 validation은 명시한 subnet과 security group이 같은 VPC에 속하는지 확인한다. 따라서 IAM policy에는 기존 EC2 생성/삭제 권한 외에 최소한 다음 조회 권한이 필요하다.

- `ec2:DescribeSubnets`
- `ec2:DescribeSecurityGroups`

## 6. VPC 네트워크 요구사항

VPC 모드에서는 Worker가 VPN에 붙지 않는다. 따라서 VPC와 CNI가 다음 경로를 직접 만족해야 한다.

| 방향 | 필요 조건 |
| --- | --- |
| Worker -> master | TCP 6443 Kubernetes API 접근 |
| master -> Worker | kubelet/API health 확인에 필요한 Node private IP 접근. Kubernetes 기본 포트 요구사항은 공식 ports/protocols 표를 기준으로 확인한다: https://kubernetes.io/docs/reference/networking/ports-and-protocols/ |
| Worker <-> Pod/Service CIDR | 사용 중인 CNI 정책에 맞는 양방향 통신 |
| Worker -> internet | kubeadm, kubelet, CRI-O, OS package, container image pull 접근 |

Security Group은 최소한 master와 Worker 사이의 Kubernetes control-plane/kubelet/CNI 트래픽을 허용해야 한다. 운영 환경에서는 `0.0.0.0/0` inbound 대신 master/worker Security Group 또는 관리 CIDR로 제한한다.

Worker bootstrap은 `kube-public/cluster-info`에서 API server URL과 CA를 읽어 join command를 구성한다. `cluster-info`가 public endpoint 또는 Karmada/관리-plane endpoint를 가리키면 VPC Worker가 private network로 master에 붙지 못할 수 있다. 적용 전에 endpoint가 Worker subnet에서 접근 가능한 master private IP 또는 내부 load balancer인지 확인한다.

Karmada Push 방식으로 workload를 내려보내는 경우에도 기본 조인은 AWS member cluster control plane API를 기준으로 생각한다. On-Premise master에서 Worker `10250`으로 직접 라우팅되는 경로가 항상 필수인 것은 아니다. 다만 AWS member cluster의 master/control-plane은 Worker kubelet `10250`에 도달해야 하며, Karmada control plane이 member API server를 통해 logs/exec/attach/metrics 같은 kubelet 경유 기능을 사용할 수 있어야 한다.

`NodeProvision`에는 다음 값을 반드시 명시한다.

- `spec.networkMode: VPC`
- `spec.awsConfig.vpcId`
- `spec.awsConfig.subnetId`
- `spec.awsConfig.securityGroupIds`

`spec.awsConfig.associatePublicIP`는 선택값이다. 값을 생략하면 기존 AWS 생성 경로의 기본값인 public IP 연결 동작을 따른다. Public subnet에서 Worker가 직접 인터넷 egress를 해야 하면 `true`로 둔다. Private subnet에서 NAT Gateway 또는 NAT Instance로 egress를 제공하면 `false`로 둔다.

## 7. NetConfig 적용

VPC 모드에서도 `NodeProvisionNetConfig`는 필요하다. 단, VPN server 설정과 status는 샘플에 넣지 않는다. Controller는 모든 namespace를 검색하지 않고 `NodeProvision` CR namespace 안의 NetConfig만 본다. 해당 namespace에 NetConfig가 0개이거나 2개 이상이면 하나를 결정할 수 없으므로, VPC Worker를 만들 namespace마다 NetConfig를 정확히 1개만 둔다. `kubernetesVersion`은 master 버전과 맞춘다.

```bash
kubectl version
kubectl apply -f config/samples/aws-vpc-netconfig.yaml
kubectl get nodeprovisionnetconfig aws-vpc-netconfig -o yaml
```

샘플의 `spec.softwareConfig.kubernetesVersion`을 실제 master 버전으로 수정한다.

## 8. VPC Worker 생성

샘플을 실제 VPC 값으로 수정한 뒤 적용한다.

```bash
kubectl apply -f config/samples/aws-vpc-worker.yaml
kubectl get nodeprovision aws-vpc-worker-001 -w
```

`NodeProvision` CR의 `Ready`는 Kubernetes Node가 실제 `Ready`가 된 뒤에만 완료로 본다. EC2가 `running`이어도 kubelet이 조인하지 못했거나 NodeReady가 아니면 아직 성공이 아니다.

```bash
kubectl describe nodeprovision aws-vpc-worker-001
kubectl get nodes -o wide
kubectl get node <WORKER_NODE_NAME> -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}{"\n"}'
```

AWS private IP가 Kubernetes Node의 InternalIP로 잡혔는지 확인한다.

```bash
kubectl get node <WORKER_NODE_NAME> -o wide
```

## 9. GPU 확인

GPU instance type을 사용해도 GPU Operator는 별도로 설치해야 한다. Node가 `Ready`가 된 것과 `nvidia.com/gpu` 리소스가 보이는 것은 다른 단계이다.

```bash
kubectl get nodes -L nvidia.com/gpu.present
kubectl describe node <WORKER_NODE_NAME> | grep -A5 nvidia.com/gpu
```

GPU Operator가 아직 없으면 위 GPU 리소스가 보이지 않는 것이 정상이다.

## 10. 삭제 전 주의사항

`NodeProvision`을 삭제하면 finalizer가 연결된 EC2 인스턴스를 삭제한다. 실행 중인 workload, checkpoint, 로그, 데이터 정리가 끝나기 전에는 삭제하지 않는다.

```bash
kubectl delete nodeprovision aws-vpc-worker-001
```

삭제 후에는 Kubernetes Node와 EC2 인스턴스가 함께 정리되었는지 확인한다.

```bash
kubectl get nodes -o wide
aws ec2 describe-instances --region ap-northeast-2 --filters Name=tag:Name,Values=aws-vpc-worker-001
```

## 11. 문제 확인 순서

- `kubectl config current-context`가 AWS member cluster인지 확인한다.
- Controller Pod가 새 image로 배포되었는지 확인한다.
- `aws-node-credentials` Secret이 `NodeProvision` namespace에 있는지 확인한다.
- `NodeProvision.spec.networkMode`가 `VPC`인지 확인한다.
- `NodeProvision`과 같은 namespace에 `NodeProvisionNetConfig`가 정확히 1개만 있는지 확인한다.
- `awsConfig.vpcId`, `subnetId`, `securityGroupIds`가 모두 실제 AWS 리소스인지 확인한다. VPC 모드에서는 이 3개 값을 자동 탐색하지 않는다.
- Worker subnet에서 인터넷 package access가 되는지 확인한다.
- master와 Worker 사이 CNI/Node 통신이 양방향인지 확인한다.
- `kubectl get nodes -o wide`에서 Worker private IP가 보이는지 확인한다.

## 12. 코드 검증 범위

VPC 부팅 스크립트, 기존 WireGuard 경로 유지, EC2 네트워크 옵션, VPN 정리 생략,
NodeReady 대기, namespace별 NetConfig 선택, 생성된 CRD/CEL 및 삭제 순서를 단위 테스트한다.

```bash
TEST_BASH="$(command -v bash)" go test ./api/ml/v1alpha1 ./provider/aws ./internal/controller/ml \
  -run '^Test(VPC|Startup)' -count=1
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/aws-vpc-manager ./cmd
```

실제 AWS EC2 생성/삭제, master Join, GPU Operator 동작, Karmada workload 배포 및
전체 envtest/E2E는 실행하지 않았다. 샘플의 VPC/Subnet/SG ID, Kubernetes 버전과
image는 실제 환경 값으로 바꿔야 한다. `networkMode`는 생성 후 변경할 수 없다.
VPC CR 삭제 시 EC2 종료를 확인한 뒤 Kubernetes Node를 정리한다.
