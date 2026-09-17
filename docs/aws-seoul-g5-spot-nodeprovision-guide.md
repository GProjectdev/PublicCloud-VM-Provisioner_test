# AWS Seoul A10G Worker 가이드 안내

실행 방식별 가이드를 다음과 같이 분리했다.

- Spot Worker: [`aws-seoul-g5-spot-guide.md`](aws-seoul-g5-spot-guide.md)
- On-Demand Worker: [`aws-seoul-g5-ondemand-guide.md`](aws-seoul-g5-ondemand-guide.md)

아래 내용은 기존 통합 가이드이며, 최신 실행 절차는 위의 방식별 문서를 기준으로 한다.

이 가이드는 On-Premise Kubernetes의 `remote-cluster-provisioner`가 AWS 서울 리전의 `ap-northeast-2c`에서 NVIDIA A10G GPU를 제공하는 `g5.xlarge` Spot 인스턴스를 만들고 Worker Node로 조인시키는 절차를 설명한다.

## 1. 사전 조건

- On-Premise Kubernetes control plane과 동작 중인 `NodeProvisionNetConfig`
- AWS 계정 및 `ap-northeast-2`의 EC2 서비스 할당량
- `ap-northeast-2c`에서 사용할 수 있는 `g5.xlarge` On-Demand/Spot 용량
- On-Premise VPN 서버로 향하는 네트워크 경로
- 컨트롤러 이미지 빌드 및 배포 환경

AWS 계정에 최소한 다음 작업 권한이 필요하다.

- EC2 인스턴스 조회, 생성, 태그, 종료
- VPC, Subnet, Security Group 조회
- 기본 VPC가 없을 때 이를 자동 생성하려면 기본 VPC 생성 권한
- EC2 Key Pair 조회 및 생성
- 최신 Ubuntu AMI 조회

AWS 계정별 Availability Zone 문자는 같은 물리 Zone을 의미하지 않을 수 있다. 이 시스템은 사용자 계정에서 보이는 `ap-northeast-2c` 이름을 그대로 사용한다.

## 2. CRD와 컨트롤러 배포

변경된 CRD에 `awsConfig.availabilityZone` 필드가 있으므로 CRD를 먼저 갱신한다.

```bash
make manifests
make install
make docker-build IMG=<registry>/remote-cluster-provisioner:aws-g5-spot
docker push <registry>/remote-cluster-provisioner:aws-g5-spot
make deploy IMG=<registry>/remote-cluster-provisioner:aws-g5-spot
```

이미 배포된 클러스터에서는 사용 중인 배포 절차에 맞게 `config/crd/bases/ml.dcn.ssu.ac.kr_nodeprovisions.yaml`과 새 컨트롤러 이미지를 적용한다.

## 3. AWS 자격 증명

정적 Access Key를 사용하는 경우 다음 Secret을 만든다. Git 저장소에는 실제 값을 저장하지 않는다.

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
```

컨트롤러 Pod에 IAM Role이 연결되어 있다면 두 키를 생략할 수 있다.

## 4. Spot GPU Worker 생성

저장소의 `config/samples/ml_v1alpha1_nodeprovision_aws.yaml`을 사용한다. 핵심 설정은 다음과 같다.

```yaml
spec:
  provider: AWS
  nodeLabel: gpu
  hardwareType: gpu
  region: ap-northeast-2
  instanceType: g5.xlarge
  marketType: Spot
  spotTerminationAction: Terminate
  awsConfig:
    availabilityZone: ap-northeast-2c
    rootVolumeSizeGB: 100
```

적용한다.

```bash
kubectl apply -f config/samples/ml_v1alpha1_nodeprovision_aws.yaml
kubectl get nodeprovision aws-g5-spot-worker-001 -w
```

컨트롤러는 `g5.xlarge`가 `ap-northeast-2c`에 제공되는지 확인하고, 해당 AZ의 subnet만 선택한다. 기본 VPC에 그 AZ의 subnet이 없으면 생성은 중단되고 명시적인 오류를 반환한다.

## 5. 상태 확인

```bash
kubectl describe nodeprovision aws-g5-spot-worker-001
kubectl get nodes -o wide
kubectl get node aws-g5-spot-worker-001 --show-labels
```

AWS에서도 확인할 수 있다.

```bash
aws ec2 describe-instances \
  --region ap-northeast-2 \
  --filters Name=tag:Name,Values=aws-g5-spot-worker-001 \
  --query 'Reservations[].Instances[].{Id:InstanceId,Type:InstanceType,AZ:Placement.AvailabilityZone,Lifecycle:InstanceLifecycle,State:State.Name}'
```

노드가 Ready가 된 뒤 GPU Operator가 드라이버와 NVIDIA Container Toolkit을 준비한다. 다음 명령으로 GPU 노출을 확인한다.

```bash
kubectl get pods -n gpu-operator
kubectl describe node aws-g5-spot-worker-001 | grep -A5 nvidia.com/gpu
```

## 6. On-Demand로 전환

동일 사양을 On-Demand로 만들려면 새 `NodeProvision` 이름을 사용하고 `marketType`을 `OnDemand`로 변경한다. 실행 중인 Spot 인스턴스의 구매 옵션은 직접 변경할 수 없으므로 새 인스턴스를 생성하고 워크로드를 Restore한 후 기존 Spot 노드를 제거해야 한다.

## 7. 주의 사항

- `g5.xlarge`의 GPU는 A10이 아니라 AWS 상품명 기준 NVIDIA A10G 1개다.
- Spot 용량이 부족하면 생성 요청이 실패할 수 있다. 이것은 인스턴스 타입 제공 여부 검사와 별개다.
- 현재 Provisioner는 Spot 부족 시 On-Demand 자동 fallback을 수행하지 않는다. 이후 정책 컨트롤러가 새 On-Demand `NodeProvision`을 생성하도록 연결해야 한다.
- Spot 종료 2분 알림 감지와 FluidCR checkpoint 호출도 별도 관리 컨트롤러의 책임이다.
- SSH와 WireGuard를 `0.0.0.0/0`에 허용하는 기본 Security Group 자동 설정은 실험 환경용이다. 운영 환경에서는 On-Premise 공인 IP와 VPN 포트로 CIDR을 제한한다.
