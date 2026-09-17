# GCP / Spot VM NodeProvision 실행 가이드

이 문서는 On-Premise Kubernetes Master Node에 `remote-cluster-provisioner`를 배포하고, `NodeProvision` CR로 GCP VM 또는 GCP Spot VM을 생성한 뒤 VPN을 통해 워커 노드로 조인시키는 실험 절차를 정리한다.

## 1. 필요한 환경

### Kubernetes / On-Premise

- On-Premise Kubernetes 클러스터가 이미 구성되어 있어야 한다.
- `kubectl`이 Master Node의 kubeconfig를 바라보고 있어야 한다.
- `NodeProvision` CRD와 controller가 설치될 namespace가 있어야 한다.
- Public Cloud VM이 kube-apiserver, kubelet, VPN 서버와 통신할 수 있어야 한다.
- `NodeProvisionNetConfig`가 먼저 준비되어야 한다.
  - VPN 서버 public endpoint
  - VPN address range
  - SSH로 VPN peer를 추가/삭제할 수 있는 Secret
  - Kubernetes worker join command

### GCP

- GCP Project가 있어야 한다.
- Compute Engine API가 활성화되어 있어야 한다.
- Service Account JSON 키가 필요하다.
- Service Account에는 최소 다음 권한이 필요하다.
  - `compute.instances.create`
  - `compute.instances.get`
  - `compute.instances.delete`
  - `compute.disks.create`
  - `compute.networks.use`
  - `compute.subnetworks.use`
  - `compute.instances.setMetadata`
- VM에 별도 Service Account를 붙일 경우 `iam.serviceAccounts.actAs`도 필요할 수 있다.
- 방화벽은 최소 다음을 허용해야 한다.
  - GCP VM에서 On-Premise VPN 서버로 나가는 WireGuard UDP 포트
  - GCP VM에서 패키지 저장소, 컨테이너 레지스트리로 나가는 outbound 트래픽
  - VPN을 통해 kube-apiserver와 kubelet 통신이 가능한 경로

### Spot VM 관련

GCP Spot VM은 Compute Engine API에서 `provisioningModel=SPOT`으로 생성한다. 종료 동작은 `STOP` 또는 `DELETE`를 지정할 수 있고, 현재 구현은 `marketType: Spot`일 때 이 scheduling 값을 넣는다. Google 공식 문서 기준으로 Spot VM 생성 옵션은 `--provisioning-model=SPOT` 또는 REST API의 `scheduling.provisioningModel: "SPOT"` 형태다.

## 2. Controller 빌드 및 배포

소스 경로:

```powershell
cd "C:\Users\JeongSeungJun\Desktop\졸업준비\Codex작업\최종 주제 작업\remote-cluster-provisioner-main\remote-cluster-provisioner-main"
```

CRD manifest를 갱신한다.

```powershell
make generate manifests
```

Controller 이미지를 빌드하고 레지스트리에 push한다.

```powershell
make docker-build IMG=<registry>/remote-cluster-provisioner:gcp-spot
docker push <registry>/remote-cluster-provisioner:gcp-spot
```

CRD와 controller를 배포한다.

```powershell
kubectl apply -k config/crd
make deploy IMG=<registry>/remote-cluster-provisioner:gcp-spot
```

배포 상태를 확인한다.

```powershell
kubectl get crd | findstr nodeprovision
kubectl get pods -n remote-cluster-provisioner-system
```

## 3. NodeProvisionNetConfig 준비

기존 AWS 워커 생성 실험에서 사용하던 `NodeProvisionNetConfig`를 그대로 사용할 수 있다. 중요한 점은 GCP VM도 동일한 VPN과 join command를 사용한다는 것이다.

확인 명령:

```powershell
kubectl get nodeprovisionnetconfigs.ml.dcn.ssu.ac.kr -A
kubectl describe nodeprovisionnetconfig <name>
```

확인해야 할 상태:

- `status.clusterJoinCommand`가 비어 있지 않아야 한다.
- VPN 서버 endpoint와 public key가 설정되어 있어야 한다.
- VPN peer 추가/삭제에 사용하는 SSH Secret이 유효해야 한다.

## 4. GCP 인증 Secret 생성

Service Account JSON을 Kubernetes Secret으로 넣는다.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: gcp-nodeprovision-credentials
  namespace: default
type: Opaque
stringData:
  serviceAccountJSON: |
    {
      "type": "service_account",
      "project_id": "YOUR_PROJECT_ID",
      "private_key_id": "...",
      "private_key": "-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----\n",
      "client_email": "...",
      "client_id": "...",
      "auth_uri": "https://accounts.google.com/o/oauth2/auth",
      "token_uri": "https://oauth2.googleapis.com/token"
    }
```

적용:

```powershell
kubectl apply -f gcp-secret.yaml
```

## 5. 일반 GCP VM 생성

```yaml
apiVersion: ml.dcn.ssu.ac.kr/v1alpha1
kind: NodeProvision
metadata:
  name: gcp-worker-ondemand-1
  namespace: default
spec:
  provider: GCP
  credentialsRef:
    name: gcp-nodeprovision-credentials
  instanceType: e2-standard-4
  nodeLabel: CPU
  maxPrice: 0
  gcpConfig:
    projectId: YOUR_PROJECT_ID
    zone: asia-northeast3-a
    network: default
    imageProject: ubuntu-os-cloud
    imageFamily: ubuntu-2204-lts
    bootDiskSizeGb: 50
```

적용:

```powershell
kubectl apply -f gcp-worker-ondemand.yaml
```

## 6. GCP Spot VM 생성

```yaml
apiVersion: ml.dcn.ssu.ac.kr/v1alpha1
kind: NodeProvision
metadata:
  name: gcp-worker-spot-1
  namespace: default
spec:
  provider: GCP
  credentialsRef:
    name: gcp-nodeprovision-credentials
  instanceType: e2-standard-4
  nodeLabel: CPU
  marketType: Spot
  spotTerminationAction: STOP
  gcpConfig:
    projectId: YOUR_PROJECT_ID
    zone: asia-northeast3-a
    network: default
    imageProject: ubuntu-os-cloud
    imageFamily: ubuntu-2204-lts
    bootDiskSizeGb: 50
```

`spotTerminationAction`은 `STOP` 또는 `DELETE`를 사용한다.

적용:

```powershell
kubectl apply -f gcp-worker-spot.yaml
```

## 7. 동작 확인

`NodeProvision` 상태를 본다.

```powershell
kubectl get nodeprovisions.ml.dcn.ssu.ac.kr -w
kubectl describe nodeprovision gcp-worker-spot-1
```

노드 조인 상태를 본다.

```powershell
kubectl get nodes -o wide
kubectl get nodes -L hardware-type,ml.dcn.ssu.ac.kr/provider
```

GCP Console 또는 `gcloud`에서 VM 상태를 확인한다.

```powershell
gcloud compute instances describe gcp-worker-spot-1 --zone asia-northeast3-a
```

VM 내부 bootstrap 로그는 다음 파일을 확인한다.

```bash
sudo tail -n 200 /var/log/node-bootstrap.log
```

## 8. 삭제

`NodeProvision`을 삭제하면 finalizer가 GCP VM 삭제와 VPN peer 정리를 수행한다.

```powershell
kubectl delete nodeprovision gcp-worker-spot-1
```

삭제 후 확인:

```powershell
kubectl get nodeprovisions.ml.dcn.ssu.ac.kr
kubectl get nodes
gcloud compute instances list --zones asia-northeast3-a
```

## 9. 자주 막히는 지점

### `GCP validation failed`

`projectId`, `zone`, `instanceType` 중 하나가 비어 있을 가능성이 높다.

### `creating GCP HTTP client` 실패

Secret의 `serviceAccountJSON`이 잘못되었거나, controller Pod에서 Secret을 읽지 못하는 상태다.

### GCP API 403

Service Account 권한이 부족하거나 Compute Engine API가 비활성화된 상태다.

### VM은 생성되지만 Kubernetes Node로 붙지 않음

다음을 확인한다.

- WireGuard UDP 포트가 막혀 있지 않은지
- `NodeProvisionNetConfig.status.clusterJoinCommand`가 최신인지
- VM에서 kube-apiserver endpoint에 접근 가능한지
- `/var/log/node-bootstrap.log`에 kubeadm join 또는 kubelet 오류가 있는지

### Spot VM이 선점됨

GCP가 Spot VM을 회수하면 VM은 `spotTerminationAction`에 따라 stop 또는 delete된다. 현재 구현은 생성 시 Spot scheduling 값을 넣는 단계까지이며, 선점 이벤트를 감지해서 자동으로 새 VM을 만들거나 checkpoint/restore 정책을 조정하는 controller 로직은 별도로 구현해야 한다.

### GPU VM

현재 GCP 설정에는 accelerator type/count 필드가 없다. 따라서 실제 GCP GPU VM을 만들려면 `guestAccelerators`, GPU driver 설치, NVIDIA container runtime 설정, GPU node label/taint 처리를 추가해야 한다.

## 10. 졸업 논문 실험으로 확장할 때 필요한 다음 작업

- Spot VM 선점 위험 수집기
  - zone, instance type, 최근 interruption 이력, 가격/가용성 지표 수집
- checkpoint interval policy
  - 선점 위험과 checkpoint cost를 이용해 interval 산정
- Spot 부족 시 fallback
  - 요청 수량보다 적게 생성되면 나머지를 on-demand로 생성
- 위험 증가 시 on-demand 전환
  - low-efficiency Spot을 일부 on-demand로 교체
- 위험 감소 시 replica Spot 배치
  - restore 가능한 복제 워크로드를 추가 배치
- checkpoint storage 전략
  - GCP Filestore/NFS, object storage, on-prem NFS, node-local disk를 비교
  - checkpoint 크기, egress 비용, restore latency, multi-cloud 이동성을 함께 측정
