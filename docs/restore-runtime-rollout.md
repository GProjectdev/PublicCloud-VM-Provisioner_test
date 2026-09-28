# 새 Worker의 GPU Restore 배포 가이드

## 범위와 완료 기준

이 변경은 수동으로 설치했던 GPU file-restore 런타임을 새 AWS Worker bootstrap에 연결한다.
기존 Ready 노드의 런타임을 자동 교체하지 않는다. 관리자가 지정한 패키지와 환경의 자격 검증은
개별 Pod가 실제로 CRIU 복원되었다는 증명과 다르다.

현재 확인된 실험 증거는 단일 GPU, 동일 노드에서 CRI-O의 `Restored container` 로그까지다.
다른 노드 복원, DDP 통신 재연결, 선점 후 학습 진도 증가까지 완료된 것은 아니다.

지원하는 자동 교체 경로는 명시적으로 활성화한 같은 AWS 클러스터의 Spot <-> OnDemand,
비 rank-0 대상 교체다. rank-0 교체, 예고 없이 이미 사라진
노드의 일반 복구는 이 경로로 지원하지 않는다. 관련 안전 조건을 제거해서 우회하지 않는다.
선점 알림을 처리하더라도 남은 시간 안에 체크포인트와 fencing이 완료된다는 보장은 없다.

## 1. 패키지 생성 (MGMT Linux)

아래 경로는 이번 실험 디렉터리 기준이다. 최신 수정 파일을 Linux 작업 사본에 먼저 반영한다.
이 문서는 Git push, 이미지 push, 패키지 업로드가 이미 완료됐다고 가정하지 않는다.

```bash
export ROOT=/root/hybridspot-validation
export CRIO_SRC="$ROOT/runtime-src/custom-crio"
cd "$CRIO_SRC"
git status --short
git fetch origin
git checkout --detach 6d082c56b0212769f58a6acd80ce2879c3d998f0
go test -mod=vendor -v server/container_restore_annotation.go server/container_restore_annotation_test.go server/container_restore_cdi.go server/container_restore_cdi_test.go
go test -mod=vendor -v server/container_restore_mount.go server/container_restore_mount_test.go
go test -mod=vendor -v internal/lib/sandbox/containerenv.go internal/lib/sandbox/containerenv_test.go
make -j2 BUILDTAGS="containers_image_openpgp containers_image_ostree_stub seccomp selinux" binaries
./bin/crio --version
```

작업 사본에 변경이 있으면 보존한 후 진행한다. 새 baseline에는 annotation restore,
CDI, checkpoint inventory 기반 TCP 옵션, 재시작 후 .containerenv, serviceaccount alias
수정이 포함되어 있다. 정적 crun에 LD_LIBRARY_PATH를 주는 임시 우회는 필요하지 않다.

CRIU/CUDA helper/plugin은 실제 시험에 사용한 검토된 ELF를 준비한다.
아래 네 입력 파일이 MGMT에 실제로 존재해야 한다. Worker에만 있는 파일은 먼저 전송한다.
빌드 호스트와 Worker의 배포판/아키텍처, 동적 라이브러리 의존성을 맞춘다.

```bash
export CRIU_BIN=/usr/local/sbin/criu
export CUDA_CHECKPOINT=/usr/local/bin/cuda-checkpoint
export CUDA_PLUGIN=/usr/local/lib/criu/cuda_plugin.so
export CRIU_SHA=cff99dbcc2ea3e54574bc2812d8b14abe2d17c94
export CRIO_SHA="$(git -C "$CRIO_SRC" rev-parse HEAD)"
# 검토 대상 tracked restore 소스 묶음의 digest. 동일 값을 NetConfig에 사용한다.
export ADAPTER_SHA="$(git -C "$CRIO_SRC" archive HEAD server internal/oci internal/lib | sha256sum | awk '{print $1}')"
cd "$ROOT/provisioner"
python3 scripts/build-stateful-runtime-package.py \
  --crio "$CRIO_SRC/bin/crio" --crio-source "$CRIO_SRC" \
  --criu "$CRIU_BIN" --cuda-checkpoint "$CUDA_CHECKPOINT" \
  --cuda-plugin "$CUDA_PLUGIN" --output-dir dist \
  --package-version 1.1.0-1 --arch amd64 --k8s-minor 1.37 \
  --crio-commit "$CRIO_SHA" --criu-commit "$CRIU_SHA" \
  --adapter-sha256 "$ADAPTER_SHA" --confirm-reviewed-adapter
sha256sum dist/stateful-migration-runtime_1.1.0-1_amd64.deb
dpkg-deb --fsys-tarfile dist/stateful-migration-runtime_1.1.0-1_amd64.deb |
  tar -xOf - ./usr/local/share/stateful-migration/runtime.json
```

`--crio-source`는 clean tree, baseline ancestry, bin/crio 경로와 바이너리의 GitCommit을
검사하고 manifest에 `restoreProfile: gpu-file-v1`을 넣는다. 재현 빌드나 GPU E2E 증명은 아니다.
이 옵션 없이 만든 이전 패키지는 설치는 가능하지만 새 restore certification을 통과하지 않는다.
생성한 .deb를 운영자 소유 HTTPS 저장소에 올린다. URL에 인증정보나 presigned query를 넣지 않는다.
저장소에 있던 1.0.0-1 파일을 새 패키지인 것처럼 재사용하지 않는다.

## 2. 이미지와 CRD 적용

고유 태그를 사용한다. 환경에 맞게 다음 이미지 변수 두 개를 지정한다.
Buildah login은 기존 레지스트리 인증을 사용한다.

```bash
: "${PROVISIONER_IMAGE:?새 Provisioner 이미지 전체 이름 필요}"
: "${STATEFUL_IMAGE:?새 Stateful 이미지 전체 이름 필요}"
cd "$ROOT/provisioner"
buildah bud -f Dockerfile -t "$PROVISIONER_IMAGE" .
buildah push "$PROVISIONER_IMAGE"
kubectl --kubeconfig="$AWS_KUBECONFIG" apply -f config/crd/bases/ml.dcn.ssu.ac.kr_nodeprovisionnetconfigs.yaml
kubectl --kubeconfig="$AWS_KUBECONFIG" apply -f config/crd/bases/ml.dcn.ssu.ac.kr_nodeprovisions.yaml

cd "$ROOT/Stateful-Migration-System"
buildah bud -f Dockerfile -t "$STATEFUL_IMAGE" .
buildah push "$STATEFUL_IMAGE"
```

Stateful 저장소 경로는 실제 checkout 위치로 바꾼다.
기존 배포의 컨테이너 이름을 조회한 뒤 해당 이름으로 set image 한다.
이번 수정으로 반드시 바뀌는 배포는 Provisioner와 stateful-checkpoint다.
다른 Stateful 모드도 동일 릴리스 이미지로 통일할 수 있으나 클러스터 위치를 혼동하지 않는다.

```bash
kubectl --kubeconfig="$AWS_KUBECONFIG" -n remote-cluster-provisioner-system get deploy remote-cluster-provisioner -o jsonpath='{.spec.template.spec.containers[*].name}'
kubectl --kubeconfig="$AWS_KUBECONFIG" -n stateful-migration-system get deploy stateful-checkpoint -o jsonpath='{.spec.template.spec.containers[*].name}'
# 아래 manager는 조회한 실제 이름으로 대체
kubectl --kubeconfig="$AWS_KUBECONFIG" -n remote-cluster-provisioner-system set image deploy/remote-cluster-provisioner manager="$PROVISIONER_IMAGE"
kubectl --kubeconfig="$AWS_KUBECONFIG" -n stateful-migration-system set image deploy/stateful-checkpoint manager="$STATEFUL_IMAGE"
kubectl --kubeconfig="$AWS_KUBECONFIG" -n remote-cluster-provisioner-system rollout status deploy/remote-cluster-provisioner --timeout=180s
kubectl --kubeconfig="$AWS_KUBECONFIG" -n stateful-migration-system rollout status deploy/stateful-checkpoint --timeout=180s
```

Karmada에도 NetConfig CRD를 배포/전파하고 있다면 같은 CRD를 갱신한다.
System 정책/복구 변경은 System의 `docs/restore-automation-validation.md`를 따른다.
NodeProvision을 Karmada에서 관리한다면 NodeProvision CRD도 Karmada에 먼저 갱신한다.
`spec.fence`는 실제 인스턴스 종료 요청이며, 이미지 배포 확인용으로 설정하지 않는다.

## 3. NetConfig와 새 Worker

[1.37 샘플](../config/samples/aws-vpc-netconfig-stateful-1.37.yaml)을 기준으로 기존 네트워크 설정은 유지한다.
Kubernetes patch version은 실제 클러스터와 맞추고 다음을 지정한다.

- runtimeProfile: StatefulMigration
- nfsClient: true
- gpuMode: DevicePlugin (이번 검증 경로)
- migrationRuntime: 새 packageURL/packageSHA256/crioCommit/criuCommit/adapterSHA256
- certifyRestore: false (최초 자격 검증 전 기본값)

네임스페이스당 NetConfig 선택 규칙을 유지한다. 별도 테스트 NodeProvision 하나로 시작한다.
GPU Operator/NFD/DevicePlugin, NFS 및 artifact store PVC, checkpoint/member webhook,
kubelet serving CSR 승인과 CA 신뢰 구성이 먼저 준비되어야 한다.
bootstrap 전에 임의 NoSchedule taint를 추가하면 GPU DaemonSet이 막힐 수 있다.

Provisioner는 artifact DaemonSet용 `migration.dcnlab.com/artifact-node=true`를 설정한다.
GPU allocatable, serving TLS 등 기존 readiness 조건은 계속 적용한다.
패키지 설치 시 바이너리 해시와 GitCommit을 검증하고 CRIU의 default/crun/runc 설정에
CUDA plugin libdir를 넣는다. 이 단계에서만으로 Restore 성공을 주장하지 않는다.

## 4. 자격 검증 후 자동 capability 부여

최초 격리 시험에서 다음 증거를 보존한다.

1. 원본 checkpoint export와 SHA-256 일치.
2. source PodUID fencing과 새 PodUID/RestorePlan UID 연결.
3. 현재 컨테이너 ID에 대한 CRI-O `Restored container` 기록.
4. 복원된 애플리케이션의 checkpointID 일치 및 소유권에 맞는 resume.
5. 시간이 지난 뒤 globalStep 증가와 GPU 연산 정상.
6. 새 노드 복원과 지원 대상 partial-rank 교체에서도 위 조건 충족.

같은 노드 단일 rank 시험만으로 다른 노드/DDP 환경을 인증하지 않는다.
이 환경을 검증한 후 NetConfig의 `migrationRuntime.certifyRestore: true`를 명시한다.
그러면 **새로 join하는** 노드에서 Provisioner가 SSH로 다음을 읽기 전용 검사한다.

- 패키지 설치 digest, manifest profile/commit/adapter, 네 바이너리 hash
- 실행 중인 CRI-O executable hash와 config 인자
- 유효 crun 경로, CRIU 활성화, crun의 CRIU 및 tcp-close 지원
- CUDA plugin 설정

성공 시 restore-from-file=true 라벨과 restore-package-sha256 annotation을 부여한다.
실패하면 VerifyingHealth에 남고 원인을 status.message에 기록한다.
기존 Ready 노드에는 이 설정 변경만으로 재설치/재인증이 실행되지 않는다.

이 라벨은 관리자 승인 환경의 시작 시점 capability다. 지속적인 drift 감시나 Pod별
restore attestation이 아니다. GPU driver/crun/OS를 바꾸면 재검증하고 기존 자격을 재사용하지 않는다.

## 5. 정책 교체 E2E

정리 과정에서 중지한 policy-manager, checkpoint-coordinator, spot-recovery-controller는
구성/워크로드가 준비되기 전 재시작하지 않는다. 백업한 replica 수로 마지막에 복구한다.

지원 대상 TrainingPolicy에서 replacement.enabled를 활성화하면 기존 컨트롤러는
UID-bound SpotReplacement, 별도 OnDemand NodeProvision, checkpoint/fence/restore 단계를 진행한다.
관찰 순서는 다음과 같다.

```bash
kubectl --kubeconfig="$KARMADA_KUBECONFIG" -n fluidcr-demo get trainingpolicies,spotreplacements
kubectl --kubeconfig="$AWS_KUBECONFIG" -n fluidcr-demo get nodeprovisions,fluidcrmigrations,restoreplans
kubectl --kubeconfig="$KARMADA_KUBECONFIG" -n fluidcr-demo get restorerequests,trainingruntimes
kubectl --kubeconfig="$AWS_KUBECONFIG" get nodes -L migration.dcnlab.com/restore-from-file
```

Pod Running만 확인하고 원본 EC2를 수동 삭제하지 않는다. 원본 fencing, UID 연결,
RestoreRequest 검증과 학습 진행을 확인한다. runtimeUnavailable/rank0/누락된 checkpoint 등
차단 이유가 발생하면 해당 조건을 해결한다. finalizer나 검증 상태를 수동 덮어쓰지 않는다.

이번 finalizer 수정은 workload가 이미 삭제되었어도 기록된 checkpoint PodUID만 정리하며,
동일 이름의 새 Pod는 resume하지 않는다. API 오류는 finalizer를 유지한다.

## 검증 한계

로컬 Go/Python 테스트와 스크립트 문법 검증은 실제 Linux 패키지 설치와 AWS GPU E2E를 대체하지 않는다.
이 변경을 적용한 .deb 빌드, 레지스트리 업로드, 새 Worker 생성 및 교체 실험은 위 순서로 수행해야 한다.
일반적인 갑작스러운 선점 복구와 역방향 정책 교체까지 모두 자동화된 것으로 간주하지 않는다.
