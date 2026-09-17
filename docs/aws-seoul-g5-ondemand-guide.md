# AWS Seoul g5.xlarge On-Demand Worker 가이드

이 문서는 On-Premise Kubernetes의 `remote-cluster-provisioner`를 이용해 AWS `ap-northeast-2c`에 NVIDIA A10G GPU 1개를 제공하는 `g5.xlarge` On-Demand 인스턴스를 생성하고 Worker Node로 등록하는 절차를 설명한다.

## 1. 사전 조건

- On-Premise Kubernetes control plane과 동작 중인 `NodeProvisionNetConfig`
- AWS 서울 리전의 EC2 API 접근 권한
- `ap-northeast-2c`의 `g5.xlarge` On-Demand 용량 및 EC2 GPU 할당량
- AWS Worker에서 On-Premise VPN 서버로 연결 가능한 네트워크
- 클러스터에 GPU Operator를 설치할 수 있는 환경

## 2. 컨트롤러와 Credential 준비

CRD와 컨트롤러 배포는 Spot 방식과 동일하다.

```bash
make manifests
make install
make docker-build IMG=<registry>/remote-cluster-provisioner:aws-g5
docker push <registry>/remote-cluster-provisioner:aws-g5
make deploy IMG=<registry>/remote-cluster-provisioner:aws-g5
```

Credential Secret을 적용한다.

```bash
kubectl apply -f config/samples/aws_node_credentials.yaml
```

컨트롤러 Pod에 IAM Role이 연결되어 있다면 정적 Access Key 대신 해당 Role을 사용한다.

## 3. On-Demand Worker 생성

On-Demand 샘플은 `marketType: OnDemand`를 명시한다. `spotTerminationAction`은 사용하지 않는다.

```yaml
spec:
  provider: AWS
  nodeLabel: gpu
  hardwareType: gpu
  region: ap-northeast-2
  instanceType: g5.xlarge
  marketType: OnDemand
  awsConfig:
    availabilityZone: ap-northeast-2c
```

적용하고 상태를 확인한다.

```bash
kubectl apply -f config/samples/ml_v1alpha1_nodeprovision_aws_ondemand.yaml
kubectl get nodeprovision aws-g5-ondemand-worker-001 -w
```

## 4. 생성 결과 확인

```bash
kubectl describe nodeprovision aws-g5-ondemand-worker-001
kubectl get nodes -o wide
kubectl describe node aws-g5-ondemand-worker-001
```

AWS CLI에서는 다음과 같이 확인한다.

```bash
aws ec2 describe-instances \
  --region ap-northeast-2 \
  --filters Name=tag:Name,Values=aws-g5-ondemand-worker-001 \
  --query 'Reservations[].Instances[].{Id:InstanceId,Type:InstanceType,AZ:Placement.AvailabilityZone,Lifecycle:InstanceLifecycle,State:State.Name}'
```

On-Demand 인스턴스의 `InstanceLifecycle`은 일반적으로 비어 있고, `AZ`는 `ap-northeast-2c`여야 한다.

## 5. GPU 확인

```bash
kubectl get pods -n gpu-operator
kubectl get node aws-g5-ondemand-worker-001 -o jsonpath='{.status.allocatable.nvidia\.com/gpu}'
```

결과는 `1`이어야 한다.

## 6. Spot 실패 후 On-Demand 대체 절차

Spot Worker를 즉시 삭제하기 전에 다음 순서로 교체한다.

1. FluidCR로 모든 DDP rank의 application checkpoint를 완료한다.
2. On-Demand `NodeProvision`을 생성한다.
3. 새 Worker가 `Ready`이고 `nvidia.com/gpu=1`인지 확인한다.
4. 새 Worker에서 checkpoint를 Restore한다.
5. 모든 rank의 global step과 generation이 일치하는지 확인한다.
6. 학습 재개를 확인한 후 기존 Spot `NodeProvision`을 삭제한다.

이 순서는 향후 Spot 관리 컨트롤러가 상태 머신으로 자동화해야 할 기본 전환 절차다.

## 7. 삭제

```bash
kubectl delete nodeprovision aws-g5-ondemand-worker-001
```

On-Demand 인스턴스는 사용 시간만큼 정상 요금이 계속 발생하므로 실험 종료 후 NodeProvision과 실제 EC2 종료 상태를 모두 확인한다.

## 8. 대표 오류

- `instance type g5.xlarge is not available in ap-northeast-2c`: 해당 AZ에서 타입을 제공하지 않음
- `no subnet found`: `ap-northeast-2c` subnet 부재
- `InsufficientInstanceCapacity`: 해당 AZ의 일시적인 On-Demand 용량 부족
- `VcpuLimitExceeded`: EC2 GPU 계열 서비스 할당량 부족

