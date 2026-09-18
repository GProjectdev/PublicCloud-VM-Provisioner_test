# Remote Cluster Provisioner 배포 전 준비 가이드

이 문서는 On-Premise Kubernetes에 Remote Cluster Provisioner를 배포하기 전에 준비해야 하는 Kubernetes, AWS, WireGuard, GPU Operator 및 Secret 구성을 설명한다. 목표 환경은 AWS 서울 리전 `ap-northeast-2`, 가용 영역 `ap-northeast-2c`, NVIDIA A10G GPU 1개를 제공하는 `g5.xlarge`이다.

## 1. 완료 기준

다음 조건을 모두 만족하면 Controller 배포를 시작할 수 있다.

- On-Premise Kubernetes control plane이 `Ready`이다.
- 배포 서버에서 `kubectl`, `make`, Go, Buildah, Skopeo, Helm을 실행할 수 있다.
- Controller 이미지를 저장할 Registry가 있고 Kubernetes Node에서 pull할 수 있다.
- WireGuard VPN Server에 SSH로 접속할 수 있고 UDP 51820이 열려 있다.
- AWS Worker가 VPN을 통해 Kubernetes API Server의 TCP 6443에 접근할 수 있다.
- `ap-northeast-2c`에 VPC, Public Subnet, Internet Gateway, Route Table 및 전용 Security Group이 준비되어 있다.
- AWS의 G 계열 On-Demand 및 Spot vCPU quota가 `g5.xlarge`를 실행할 만큼 충분하다.
- AWS Credential Secret과 VPN SSH Secret을 준비했다.
- 유효한 `NodeProvisionNetConfig`를 작성했다.
- GPU Operator를 설치할 준비가 되어 있다.

## 2. 배포 서버 도구 준비

저장소의 `go.mod`는 Go 1.24.5를 지정한다. 다음 도구를 준비한다.

```bash
git --version
go version
make --version
buildah version
buildah info
skopeo --version
kubectl version --client
helm version
```

Ubuntu 계열 배포 서버에서는 다음과 같이 설치할 수 있다. 다른 Linux 배포판에서는 해당 배포판의 패키지 관리자를 사용한다.

```bash
sudo apt-get update
sudo apt-get install -y buildah skopeo
```

`buildah info`가 실패하면 rootless user namespace 설정과 `/etc/subuid`, `/etc/subgid` 등록 상태를 먼저 확인한다. 이후 명령은 모두 일반 사용자로 실행하거나 모두 `sudo`로 실행해야 한다. 두 방식을 섞으면 이미지 저장소와 Registry 인증 정보가 서로 달라진다.

Kubernetes 연결을 확인한다.

```bash
kubectl cluster-info
kubectl get nodes -o wide
kubectl get pods -A
kubectl auth can-i create customresourcedefinitions.apiextensions.k8s.io
kubectl auth can-i create clusterroles.rbac.authorization.k8s.io
```

Controller 배포에는 CRD, ClusterRole, ClusterRoleBinding 및 Deployment 생성 권한이 필요하다.

## 3. Kubernetes Control Plane 확인

모든 기존 Node가 `Ready`인지 확인한다.

```bash
kubectl get nodes
kubectl get --raw='/readyz?verbose'
```

API Server가 기본 TCP 6443을 사용하는지 확인한다.

```bash
sudo ss -lntp | grep 6443
```

Remote Cluster Provisioner는 Bootstrap Token과 CA hash를 이용해 `kubeadm join` 명령을 만든다. Control Plane이 kubeadm 기반이어야 하며 Controller가 Bootstrap Token을 조회·생성할 수 있어야 한다.

클러스터에서 사용할 Kubernetes 버전을 기록한다.

```bash
kubectl version
kubeadm version
kubelet --version
```

이 버전을 이후 `NodeProvisionNetConfig.spec.softwareConfig.kubernetesVersion`에 입력한다.

## 4. AWS Service Quota 확인

AWS Console에서 Region을 `Asia Pacific (Seoul)`로 선택하고 다음 quota를 확인한다.

- `Running On-Demand G and VT instances`
- `All G and VT Spot Instance Requests`

`g5.xlarge`는 4 vCPU를 사용하므로 각 방식으로 한 대를 실행하려면 해당 구매 옵션의 quota가 최소 4 vCPU여야 한다. 신규 계정은 G 계열 quota가 0일 수 있으므로 실험 전에 증가 요청을 완료한다.

AWS CLI를 사용할 경우 현재 quota를 조회한다.

```bash
aws service-quotas list-service-quotas \
  --service-code ec2 \
  --region ap-northeast-2 \
  --query "Quotas[?contains(QuotaName, 'G and VT')].[QuotaName,Value,QuotaCode]" \
  --output table
```

참고: [Amazon EC2 instance type quotas](https://docs.aws.amazon.com/ec2/latest/instancetypes/ec2-instance-quotas.html)

## 5. AWS VPC와 Subnet 준비

자동으로 기본 VPC를 수정하게 두는 것보다 실험 전용 리소스를 명시적으로 만드는 방식을 권장한다.

준비할 리소스:

```text
VPC
└─ Public Subnet: ap-northeast-2c
   ├─ Route: 0.0.0.0/0 -> Internet Gateway
   ├─ Public IPv4 사용 가능
   └─ 전용 Security Group
```

Public Subnet은 Internet Gateway로 향하는 route가 있어야 하며, 인스턴스에는 Public IPv4가 필요하다. 참고: [AWS VPC Internet Gateway](https://docs.aws.amazon.com/vpc/latest/userguide/VPC_Internet_Gateway.html)

리소스 ID를 기록한다.

```text
VPC_ID=vpc-...
SUBNET_ID=subnet-...
SECURITY_GROUP_ID=sg-...
```

Subnet이 정확한 AZ에 속하는지 확인한다.

```bash
aws ec2 describe-subnets \
  --region ap-northeast-2 \
  --subnet-ids <SUBNET_ID> \
  --query 'Subnets[0].{VpcId:VpcId,AZ:AvailabilityZone,PublicIP:MapPublicIpOnLaunch}'
```

`AZ`는 `ap-northeast-2c`여야 한다. Provisioner도 EC2 생성 시 Public IP 연결을 요청하지만, Route Table과 Internet Gateway가 없으면 패키지 및 컨테이너 이미지를 다운로드할 수 없다.

## 6. Security Group 준비

실험 전용 Security Group에 필요한 범위만 허용한다.

| 방향 | 프로토콜/포트 | 허용 대상 | 목적 |
|---|---|---|---|
| Inbound | TCP 22 | 관리자 또는 On-Premise 공인 IP | 장애 분석용 SSH |
| Outbound | UDP 51820 | VPN Server 공인 IP | WireGuard 연결 |
| Outbound | TCP 443 | 필요한 저장소 또는 인터넷 | 패키지·이미지 다운로드 |
| VPN 경유 | TCP 6443 | On-Premise Control Plane | Kubernetes API |
| VPN 경유 | TCP 10250 | Control Plane과 Worker | Kubelet API |

기본 Security Group 자동 설정을 사용하면 코드가 TCP 22와 UDP 51820을 `0.0.0.0/0`에 허용할 수 있다. 따라서 준비한 `subnetId`와 `securityGroupIds`를 NodeProvision에 명시한다.

## 7. WireGuard VPN Server 준비

VPN Server에는 다음 조건이 필요하다.

- 고정 Public IP 또는 안정적인 DNS
- SSH 접속 계정
- WireGuard 설치
- UDP 51820 인바운드 허용
- IP forwarding 활성화
- VPN CIDR이 VPC 및 On-Premise CIDR과 중복되지 않음
- VPN Client에서 Kubernetes API Server로 가는 route 존재

예시 VPN CIDR:

```text
10.9.0.0/24
```

상태를 확인한다.

```bash
ssh -i <VPN_SSH_KEY> ubuntu@<VPN_SERVER_PUBLIC_IP>
sudo systemctl status wg-quick@wg0
sudo wg show
sudo sysctl net.ipv4.ip_forward
```

`net.ipv4.ip_forward` 결과는 `1`이어야 한다.

방화벽과 Cloud Security Group에서 UDP 51820이 열려 있는지도 확인한다.

## 8. VPN SSH Secret 생성

실제 Private Key가 들어간 파일은 Git에 저장하지 않는다.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: vpn-server-secret
  namespace: default
type: Opaque
stringData:
  id_rsa: |
    -----BEGIN OPENSSH PRIVATE KEY-----
    <VPN_SERVER_PRIVATE_KEY>
    -----END OPENSSH PRIVATE KEY-----
```

적용 후 키 존재 여부만 확인한다.

```bash
kubectl apply -f vpn-server-secret.local.yaml
kubectl get secret vpn-server-secret -n default
```

Secret 내용을 터미널이나 로그에 출력하지 않는다.

## 9. AWS IAM과 Credential 준비

준비한 네트워크 리소스를 명시적으로 사용하는 경우 Controller에 필요한 주요 EC2 작업은 다음과 같다.

```text
ec2:RunInstances
ec2:TerminateInstances
ec2:DescribeInstances
ec2:DescribeImages
ec2:DescribeInstanceTypeOfferings
ec2:DescribeVpcs
ec2:DescribeSubnets
ec2:DescribeSecurityGroups
ec2:CreateTags
ec2:DescribeKeyPairs
ec2:ImportKeyPair
ec2:DeleteKeyPair
```

`RunInstances`는 AMI뿐 아니라 Volume, Network Interface, Subnet, Security Group 및 Key Pair 리소스 사용 권한도 필요하다. 참고: [AWS EC2 IAM policy examples](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/iam-policies-ec2-console.html)

정적 Credential을 사용하는 경우 로컬 사본을 만든다.

```bash
cp config/samples/aws_node_credentials.yaml aws-node-credentials.local.yaml
```

`REPLACE_ME`를 실제 값으로 교체하고 적용한다.

```bash
kubectl apply -f aws-node-credentials.local.yaml
kubectl get secret aws-node-credentials -n default
```

`*.local.yaml`을 Git에 추가하지 않는다. 가능하면 장기 Access Key 대신 Controller Pod에 연결된 IAM Role 또는 단기 Credential을 사용한다.

## 10. NodeProvisionNetConfig 작성

`config/samples/ml_v1alpha1_nodeprovisionnetconfig.yaml`은 아직 Kubebuilder 스캐폴딩 파일이므로 사용하지 않는다. 다음 최소 템플릿으로 별도 파일을 만든다.

```yaml
apiVersion: ml.dcn.ssu.ac.kr/v1alpha1
kind: NodeProvisionNetConfig
metadata:
  name: my-cluster-netconfig
  namespace: default
spec:
  clusterName: my-cluster
  vpnRange: "10.9.0.0/24"
  vpnServerPublicConfig:
    publicIP: "<VPN_SERVER_PUBLIC_IP>"
    sshPort: "22"
    sshUsername: ubuntu
    vpnPort: "51820"
    vpnSshCredentialsRef:
      name: vpn-server-secret
      namespace: default
      key: id_rsa
  softwareConfig:
    kubernetesVersion: "<KUBERNETES_VERSION>"
```

현재 API 타입에는 `nvidiaDriverVersion`, `nvidiaContainerToolkitVersion`, `k8sDevicePluginVersion` 필드가 없으므로 넣지 않는다. NVIDIA 구성은 GPU Operator가 담당한다.

파일을 적용하는 시점은 CRD 설치 이후이다.

## 11. NodeProvision 샘플에 AWS 리소스 지정

Spot 또는 On-Demand 샘플의 `awsConfig`에 준비한 리소스를 입력한다.

```yaml
awsConfig:
  availabilityZone: ap-northeast-2c
  vpcId: <VPC_ID>
  subnetId: <SUBNET_ID>
  securityGroupIds:
    - <SECURITY_GROUP_ID>
  rootVolumeSizeGB: 100
```

Spot은 다음 설정을 사용한다.

```yaml
marketType: Spot
spotTerminationAction: Terminate
```

On-Demand는 다음 설정을 사용한다.

```yaml
marketType: OnDemand
```

## 12. GPU Operator 준비

Remote Cluster Provisioner는 GPU Node Label만 설정하고 NVIDIA Driver, Container Toolkit, CDI 및 Device Plugin 설치는 GPU Operator에 위임한다.

GPU Operator 설치 전 다음을 확인한다.

- `kubectl`과 Helm 사용 가능
- GPU Worker OS가 Ubuntu 22.04로 통일됨
- Container Runtime이 CRI-O
- 기존 Node Feature Discovery 설치 여부
- Pod Security Admission 사용 시 GPU Operator namespace의 privileged 허용

GPU Operator 설치 방법과 사용할 Chart 버전은 배포 시점의 공식 문서를 기준으로 결정한다. 참고: [NVIDIA GPU Operator 설치 문서](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html)

GPU Worker가 생성된 뒤 다음 결과가 나와야 한다.

```bash
kubectl get pods -n gpu-operator
kubectl get node <GPU_NODE_NAME> \
  -o jsonpath='{.status.allocatable.nvidia\.com/gpu}'
```

`g5.xlarge`의 예상 GPU 개수는 `1`이다.

## 13. Container Registry 준비

Controller 이미지를 저장할 Registry를 준비한다.

```bash
export IMG=<REGISTRY>/remote-cluster-provisioner:aws-g5
buildah login <REGISTRY>
buildah bud --format docker --arch amd64 -t ${IMG} .
buildah push ${IMG} docker://${IMG}
skopeo inspect docker://${IMG}
```

GitHub Container Registry를 사용한다면 `<REGISTRY>`는 `ghcr.io`이며, 비밀번호 대신 package write 권한이 있는 Personal Access Token을 사용한다.

```bash
echo "${GHCR_TOKEN}" | buildah login ghcr.io -u <GITHUB_USER> --password-stdin
```

On-Premise Node에서 pull 가능한지 확인한다.

```bash
sudo crictl pull <REGISTRY>/remote-cluster-provisioner:aws-g5
```

Private Registry이면 Controller Deployment용 `imagePullSecret`도 준비해야 한다.

## 14. 비용 및 정리 보호 장치

AWS Budget과 비용 알림을 먼저 만든다. 다음 Tag도 권장한다.

```yaml
tags:
  project: graduation-research
  owner: <OWNER>
  workload: gpu-training
  capacity: spot
```

실험 종료 명령을 미리 기록한다.

```bash
kubectl delete nodeprovision <NAME>
aws ec2 describe-instances \
  --region ap-northeast-2 \
  --filters Name=tag:project,Values=graduation-research \
  --query 'Reservations[].Instances[].{Id:InstanceId,State:State.Name}'
```

NodeProvision 삭제 후에도 EC2가 `terminated`인지 AWS Console 또는 CLI에서 확인한다.

## 15. 배포 직전 Preflight

아래 항목이 모두 성공해야 한다.

```bash
# Kubernetes
kubectl get nodes
kubectl get pods -A

# 필수 Secret
kubectl get secret aws-node-credentials vpn-server-secret -n default

# VPN Server SSH
ssh -i <VPN_SSH_KEY> ubuntu@<VPN_SERVER_PUBLIC_IP> 'sudo wg show'

# AWS 자격 증명
aws sts get-caller-identity

# g5.xlarge AZ 제공 여부
aws ec2 describe-instance-type-offerings \
  --region ap-northeast-2 \
  --location-type availability-zone \
  --filters Name=location,Values=ap-northeast-2c Name=instance-type,Values=g5.xlarge

# Subnet AZ
aws ec2 describe-subnets \
  --region ap-northeast-2 \
  --subnet-ids <SUBNET_ID> \
  --query 'Subnets[0].AvailabilityZone'

# Controller 이미지
skopeo inspect docker://<REGISTRY>/remote-cluster-provisioner:aws-g5
```

## 16. Kubernetes에 Remote Cluster Provisioner 설치

사전 준비가 끝났으면 다음 구성요소를 On-Premise Kubernetes에 설치해야 한다.

```text
CustomResourceDefinition
├─ nodeprovisions.ml.dcn.ssu.ac.kr
├─ nodeprovisionnetconfigs.ml.dcn.ssu.ac.kr
└─ remoteclusters.infra.dcn.ssu.ac.kr

Controller Runtime
├─ Namespace: remote-cluster-provisioner-system
├─ ServiceAccount
├─ ClusterRole / ClusterRoleBinding
├─ Leader-election Role / RoleBinding
└─ Deployment: remote-cluster-provisioner-controller-manager
```

### 16.1 저장소 받기

외부 배포 서버에서 임시 GitHub 저장소를 clone한다.

```bash
git clone https://github.com/GProjectdev/PublicCloud-VM-Provisioner_test.git
cd PublicCloud-VM-Provisioner_test
git checkout main
```

현재 커밋을 기록한다.

```bash
git rev-parse HEAD
git status --short
```

`git status --short` 결과는 비어 있어야 한다.

### 16.2 Controller 이미지 빌드 및 Push

Controller Pod가 이미지를 pull할 수 있는 Registry 주소를 정한다.

```bash
export IMG=<REGISTRY>/remote-cluster-provisioner:aws-g5
```

예를 들어 GHCR을 사용한다면 다음과 같은 형태다.

```bash
export IMG=ghcr.io/gprojectdev/publiccloud-vm-provisioner:aws-g5
```

로그인한 뒤 이미지를 빌드하고 push한다. Makefile의 타깃 이름은 `docker-build`, `docker-push`이지만 `CONTAINER_TOOL=buildah`를 지정하면 실제 실행 도구는 Buildah가 된다.

```bash
buildah login <REGISTRY>
make docker-build CONTAINER_TOOL=buildah IMG=${IMG}
make docker-push CONTAINER_TOOL=buildah IMG=${IMG}
skopeo inspect docker://${IMG}
```

빌드 서버와 On-Premise Kubernetes Node가 모두 `amd64`라면 위 단일 아키텍처 방식이 가장 단순하다. 아키텍처를 명시해서 직접 빌드하려면 다음 명령을 사용할 수 있다.

```bash
buildah bud --format docker --arch amd64 -t ${IMG} .
buildah push ${IMG} docker://${IMG}
```

여러 CPU 아키텍처를 동시에 지원해야 하면 `make docker-buildx`를 사용하지 않는다. Buildah에는 `buildx` 하위 명령이 없으므로 Buildah manifest 기능을 사용한다.

```bash
buildah build \
  --platform linux/amd64,linux/arm64 \
  --manifest ${IMG}-manifest .
buildah manifest push --all ${IMG}-manifest docker://${IMG}
```

다른 아키텍처의 `RUN` 명령을 현재 호스트에서 실행하려면 QEMU/binfmt 설정이 별도로 필요하다. 이 프로젝트의 첫 배포에서는 Kubernetes Node 아키텍처에 맞춘 단일 아키텍처 이미지를 권장한다.

Private Registry를 사용하면 Controller Deployment에 `imagePullSecrets`가 필요하다. 현재 기본 `config/default`에는 `imagePullSecrets`가 없으므로, 처음 검증할 때는 클러스터에서 pull 가능한 Registry 또는 공개 이미지를 사용하는 것이 단순하다.

### 16.3 배포 대상 Kubernetes Context 확인

다른 클러스터에 잘못 설치하지 않도록 반드시 확인한다.

```bash
kubectl config current-context
kubectl cluster-info
kubectl get nodes
```

출력된 Context와 Node가 On-Premise Kubernetes인지 확인한 후 진행한다.

### 16.4 CRD 설치

다음 명령은 `config/crd`의 CustomResourceDefinition을 현재 kubeconfig가 가리키는 클러스터에 설치한다.

```bash
make install
```

설치 결과를 확인한다.

```bash
kubectl get crd nodeprovisions.ml.dcn.ssu.ac.kr
kubectl get crd nodeprovisionnetconfigs.ml.dcn.ssu.ac.kr
kubectl get crd remoteclusters.infra.dcn.ssu.ac.kr
kubectl api-resources | grep -E 'NodeProvision|RemoteCluster'
```

세 CRD가 모두 표시되어야 한다.

### 16.5 Controller와 RBAC 배포

빌드하고 push한 이미지 주소를 사용한다.

```bash
make deploy IMG=${IMG}
```

`make deploy`는 다음 리소스를 함께 적용한다.

- CRD
- Namespace
- ServiceAccount
- ClusterRole과 ClusterRoleBinding
- Leader-election Role과 RoleBinding
- Controller Deployment
- Metrics Service

배포 상태를 확인한다.

```bash
kubectl get namespace remote-cluster-provisioner-system
kubectl get deployment -n remote-cluster-provisioner-system
kubectl get pods -n remote-cluster-provisioner-system -o wide
kubectl rollout status \
  deployment/remote-cluster-provisioner-controller-manager \
  -n remote-cluster-provisioner-system \
  --timeout=180s
```

Controller 로그도 확인한다.

```bash
kubectl logs \
  deployment/remote-cluster-provisioner-controller-manager \
  -n remote-cluster-provisioner-system \
  --tail=200
```

정상 조건은 다음과 같다.

```text
Deployment AVAILABLE=1
Controller Pod STATUS=Running
READY=1/1
CrashLoopBackOff 또는 ImagePullBackOff 없음
로그에 지속적으로 반복되는 RBAC 오류 없음
```

### 16.6 설치용 단일 YAML을 사용하는 방법

Makefile은 CRD와 Controller 리소스를 하나로 합친 설치 파일도 생성할 수 있다.

```bash
make build-installer IMG=${IMG}
kubectl apply -f dist/install.yaml
```

`dist/install.yaml`을 다른 On-Premise 서버로 옮겨 배포할 때 사용할 수 있다. 이 경우에도 `${IMG}` 이미지는 대상 Kubernetes Node에서 pull 가능해야 한다.

### 16.7 Secret 적용

Controller가 `Running`인 것을 확인한 후 AWS 및 VPN Secret을 적용한다.

```bash
kubectl apply -f aws-node-credentials.local.yaml
kubectl apply -f vpn-server-secret.local.yaml

kubectl get secret \
  aws-node-credentials vpn-server-secret \
  -n default
```

Secret의 실제 값을 `kubectl get ... -o yaml`로 출력하거나 로그에 남기지 않는다.

### 16.8 NodeProvisionNetConfig 적용

10절에서 작성한 파일을 적용한다.

```bash
kubectl apply -f nodeprovision-netconfig.local.yaml
kubectl get nodeprovisionnetconfig -n default
kubectl describe nodeprovisionnetconfig my-cluster-netconfig -n default
```

Controller 로그에서 NetConfig 조회 또는 권한 오류가 없는지 다시 확인한다.

```bash
kubectl logs \
  deployment/remote-cluster-provisioner-controller-manager \
  -n remote-cluster-provisioner-system \
  --tail=200
```

### 16.9 설치 완료 판정

다음 명령이 모두 성공하면 NodeProvision을 생성할 준비가 끝난 것이다.

```bash
kubectl get crd nodeprovisions.ml.dcn.ssu.ac.kr
kubectl get pods -n remote-cluster-provisioner-system
kubectl get secret aws-node-credentials vpn-server-secret -n default
kubectl get nodeprovisionnetconfig my-cluster-netconfig -n default
kubectl auth can-i create nodeprovisions.ml.dcn.ssu.ac.kr -n default
```

이 단계까지는 AWS EC2 인스턴스를 생성하지 않으므로 EC2 사용 요금이 발생하지 않는다. 이후 Spot 또는 On-Demand `NodeProvision`을 적용하는 순간 실제 인스턴스 생성 요청이 시작된다.

### 16.10 `deploy/` 디렉터리 사용 시 주의

`deploy/deployment.yaml`에는 다음과 같은 이미지 placeholder가 남아 있다.

```text
ghcr.io/<ORG>/remote-cluster-provisioner:latest
```

따라서 `kubectl apply -k deploy/`를 그대로 실행하면 이미지를 pull하지 못한다. `make deploy IMG=${IMG}` 경로를 사용하거나, `deploy/kustomization.yaml`의 `images` 설정을 실제 Registry로 수정한 후 적용해야 한다.

## 17. 권장 최초 실험 순서

1. CRD와 Controller를 배포한다.
2. `NodeProvisionNetConfig`를 적용한다.
3. Spot보다 먼저 On-Demand `g5.xlarge` 한 대를 생성한다.
4. VPN handshake, Node `Ready`, CRI-O 및 `kubeadm join`을 확인한다.
5. GPU Operator가 `nvidia.com/gpu=1`을 등록하는지 확인한다.
6. 테스트 CUDA Pod를 실행한다.
7. NodeProvision을 삭제하고 EC2와 VPN Peer가 정리되는지 확인한다.
8. 같은 절차를 Spot Worker로 반복한다.

On-Demand E2E 검증이 끝나기 전에는 Spot 선점과 용량 부족 문제까지 함께 다루지 않는 것이 원인 분리에 유리하다.

## 18. 알려진 주의점

- 현재 코드는 실제 AWS/Kubernetes 환경에서 이 저장소 버전 전체가 E2E 검증된 상태는 아니다.
- `config/samples/ml_v1alpha1_nodeprovisionnetconfig.yaml`은 사용할 수 있는 완성 샘플이 아니다.
- `ml_v1alpha1_nodeprovision_cnlab_runtime.yaml` 일부 NVIDIA 버전 필드는 현재 API 타입과 맞지 않는다.
- Spot 용량이 없으면 instance type offering 조회가 성공해도 생성은 실패할 수 있다.
- Controller는 Spot 종료 알림 감지, checkpoint, On-Demand fallback 또는 restore를 수행하지 않는다.
- GPU Operator가 준비되기 전에는 Kubernetes Node가 `Ready`여도 `nvidia.com/gpu`가 나타나지 않을 수 있다.
