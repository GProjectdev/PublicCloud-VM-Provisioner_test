# AWS Seoul g5.xlarge Spot Worker 가이드

이 문서는 On-Premise Kubernetes의 `remote-cluster-provisioner`를 이용해 AWS `ap-northeast-2c`에 NVIDIA A10G GPU 1개를 제공하는 `g5.xlarge` Spot 인스턴스를 생성하고 Worker Node로 등록하는 절차를 설명한다.

## 1. 사전 조건

- On-Premise Kubernetes control plane과 동작 중인 `NodeProvisionNetConfig`
- AWS 서울 리전의 EC2 API 접근 권한
- `ap-northeast-2c`의 `g5.xlarge` Spot 용량 및 EC2 GPU 할당량
- AWS Worker에서 On-Premise VPN 서버로 연결 가능한 네트워크
- 클러스터에 GPU Operator를 설치할 수 있는 환경

## 2. 컨트롤러 배포

`awsConfig.availabilityZone`이 포함된 CRD와 컨트롤러 이미지를 배포한다.

```bash
make manifests
make install
make docker-build IMG=<registry>/remote-cluster-provisioner:aws-g5
docker push <registry>/remote-cluster-provisioner:aws-g5
make deploy IMG=<registry>/remote-cluster-provisioner:aws-g5
```

## 3. AWS Credential 적용

[`config/samples/aws_node_credentials.yaml`](../config/samples/aws_node_credentials.yaml)의 placeholder를 실제 값으로 교체한 후 적용한다. 컨트롤러 Pod에 IAM Role을 연결한 경우 이 Secret의 키는 생략할 수 있다.

```bash
kubectl apply -f config/samples/aws_node_credentials.yaml
```

실제 Access Key와 Secret Key를 Git에 커밋하지 않는다.

## 4. Spot Worker 생성

Spot 샘플의 핵심 값은 다음과 같다.

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
```

적용하고 상태를 확인한다.

```bash
kubectl apply -f config/samples/ml_v1alpha1_nodeprovision_aws.yaml
kubectl get nodeprovision aws-g5-spot-worker-001 -w
```

컨트롤러는 `g5.xlarge`가 지정 AZ에 제공되는지 검사하고 `ap-northeast-2c`에 속한 subnet만 선택한다. Spot 용량 부족 여부는 생성 요청 시 AWS가 최종 결정한다.

## 5. 생성 결과 확인

```bash
kubectl describe nodeprovision aws-g5-spot-worker-001
kubectl get nodes -o wide
kubectl describe node aws-g5-spot-worker-001
```

AWS CLI에서는 구매 옵션과 AZ를 확인한다.

```bash
aws ec2 describe-instances \
  --region ap-northeast-2 \
  --filters Name=tag:Name,Values=aws-g5-spot-worker-001 \
  --query 'Reservations[].Instances[].{Id:InstanceId,Type:InstanceType,AZ:Placement.AvailabilityZone,Lifecycle:InstanceLifecycle,State:State.Name}'
```

정상적인 Spot 인스턴스라면 `Lifecycle` 값이 `spot`, `AZ` 값이 `ap-northeast-2c`여야 한다.

## 6. GPU 확인

GPU Operator가 준비된 후 다음을 확인한다.

```bash
kubectl get pods -n gpu-operator
kubectl get node aws-g5-spot-worker-001 -o jsonpath='{.status.allocatable.nvidia\.com/gpu}'
```

결과는 `1`이어야 한다.

## 7. 삭제

```bash
kubectl delete nodeprovision aws-g5-spot-worker-001
```

NodeProvision finalizer가 EC2 인스턴스와 VPN peer를 정리한다. 강제로 finalizer를 제거하면 클라우드 자원이 남을 수 있으므로 사용하지 않는다.

## 8. Spot 전용 주의 사항

- Spot 인스턴스는 AWS에 의해 회수될 수 있다.
- `Terminate` 동작을 사용하므로 선점 후 인스턴스 로컬 checkpoint는 함께 사라진다.
- Checkpoint는 NFS, S3 또는 On-Premise 저장소처럼 인스턴스 외부에 보관해야 한다.
- 현재 Provisioner만으로는 종료 2분 알림 감지, FluidCR 호출, On-Demand fallback이 자동 수행되지 않는다.
- Spot 확보 실패 시 On-Demand 샘플로 새 Worker를 생성하고 checkpoint를 Restore해야 한다.

## 9. 대표 오류

- `instance type g5.xlarge is not available in ap-northeast-2c`: 해당 계정에서 인스턴스 타입이 그 AZ에 제공되지 않음
- `no subnet found`: 기본 VPC에 `ap-northeast-2c` subnet이 없으므로 subnet을 생성하거나 `subnetId`를 명시해야 함
- `InsufficientInstanceCapacity`: 현재 Spot 용량 부족
- `VcpuLimitExceeded`: EC2 GPU 계열 서비스 할당량 부족

