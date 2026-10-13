# Provisioner v3.0 SSH / Key pair 수정 배포 (2026-10-13)

## 변경 범위
- SSH 포트 생략(0)은 22로 처리하고 범위 밖 값은 거부합니다.
- AWS 기본값과 자동 Key pair를 실행 메모리에서 결정합니다. Karmada 소유 spec을 수정해 다음 reconcile까지 유지되기를 기다리지 않습니다.
- 상태 갱신 충돌로 객체를 다시 읽더라도 검증된 생성 설정의 복사본으로 EC2를 생성합니다.
- 키 이름이 없는 생성 요청은 컨트롤러에서 차단합니다.
- 기존 키 Secret 재사용과 Restore 검증은 유지합니다. 검증을 우회하지 않습니다.
- 이번 로그만으로 실제 운영 환경에서 누가 필드를 제거했는지는 확정하지 않았습니다. 확인된 포트 문제와 코드상 설정 유실 경로를 수정합니다.

## 1. 중단 및 준비
기존 평가를 stop/cleanup하고 해당 EC2가 Terminated인지 확인합니다. evidence는 보관합니다.
이미 키 없이 생성된 인스턴스는 이미지 업데이트만으로 복구되지 않습니다. 수정 후 새 run-id로 생성하세요.
namespace는 fluidcr-realign-121040을 유지합니다. 다른 실험의 Secret/키는 삭제하지 마세요.

## 2. 소스 받기 (MGMT)
기존 디렉터리가 있으면 clone 대신 해당 저장소에서 git status 후 git pull --ff-only를 실행합니다.
```bash
cd /root/hybridspot-validation
git clone --branch restore-automation-20260928 https://github.com/GProjectdev/PublicCloud-VM-Provisioner_test.git provisioner-v3.0
cd provisioner-v3.0
git rev-parse HEAD
go test -p 2 -mod=readonly ./...
go vet -p 2 ./...
```

## 3. Buildah 빌드 및 Push
```bash
set -euo pipefail
export IMAGE=docker.io/jeongseungjun/my-publiccloudvm-provisioner:v3.0
buildah login docker.io
buildah bud --arch amd64 --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 -f Dockerfile -t "$IMAGE" .
buildah push "$IMAGE" "docker://$IMAGE"
```
Push 성공 후에만 다음 단계로 진행합니다. 이 릴리스에서는 System 컨트롤러와 학습 이미지를 재빌드하지 않습니다. CRD 스키마 변경도 없습니다.

## 4. AWS 클러스터 Deployment 교체
```bash
export IMAGE=docker.io/jeongseungjun/my-publiccloudvm-provisioner:v3.0
kubectl --kubeconfig="${AWS_KUBECONFIG:?}" -n remote-cluster-provisioner-system get deployment remote-cluster-provisioner -o yaml > provisioner-before-v3.0.yaml
kubectl --kubeconfig="${AWS_KUBECONFIG:?}" -n remote-cluster-provisioner-system set image deployment/remote-cluster-provisioner "manager=${IMAGE:?}"
kubectl --kubeconfig="${AWS_KUBECONFIG:?}" -n remote-cluster-provisioner-system rollout status deployment/remote-cluster-provisioner --timeout=180s
kubectl --kubeconfig="${AWS_KUBECONFIG:?}" -n remote-cluster-provisioner-system get pods -o json | jq '.items[] | {name:.metadata.name, images:[.status.containerStatuses[]? | {image,imageID}]}'
```
v3.0 태그는 같은 내용으로 유지하고 실행 imageID를 기록하세요. 수정본을 다시 만들 때는 새 태그를 사용합니다.

## 5. 새 노드 검증
새 B 실험을 고유 run-id로 생성합니다. 1시간을 기다리기 전에 다음을 확인합니다.
```bash
kubectl --kubeconfig="${AWS_KUBECONFIG:?}" -n fluidcr-realign-121040 get nodeprovisions -o custom-columns='NAME:.metadata.name,INSTANCE:.status.instanceId,PHASE:.status.phase,MESSAGE:.status.message'
```
- 해당 노드의 <이름>-ssh-key Secret 존재 (개인키를 출력하지 않습니다).
- 위 instance ID를 EC2 콘솔에서 찾아 Key pair name 지정 확인.
- 포트 0/SSH 인증 오류 없이 NodeProvision Ready 확인.
- Kubernetes Node Ready만으로 성공 판정하지 않습니다.
- spec.keyPairName과 sshPort는 생략 상태일 수 있습니다. v3.0은 실행 시 해결하므로 null 자체를 실패로 판단하지 않습니다.
- 위험률 변경 후 대체 노드도 같은 검증을 통과하고 Restore 및 기존 노드 정리까지 완료해야 합니다.
- 실패하면 해당 run은 장애 자료로 분리하고 정상 비용 비교에서 제외합니다.

## 검증 한계
로컬 테스트는 실제 AWS API, Karmada 동시 갱신, GPU Restore를 재현하지 않습니다. 위 새 노드 확인이 실환경 완료 조건입니다.

