# Controller bootstrap 수정 배포

CRI-O의 명시적 설정 파일이 없으면 기존 drop-in을 유지하면서 기본 파일을 생성한다.
CRIU는 지원되지 않는 plugin-dir 대신 libdir를 기록하며 기존 설정의 다른 항목을 보존한다.
이 변경은 새 Worker user-data에 적용된다. 기존 EC2의 cloud-init을 자동 교체하지 않는다.

기존 NodeProvision은 삭제하지 않는다. 삭제는 EC2 종료로 이어진다.
status.phase가 Ready이면 controller reconcile은 추가 작업 없이 반환한다.
수동 복구한 Worker가 Ready여도 CR 상태가 아직 진행 중이면 controller 로그로 완료 여부를 확인한다.

MGMT에서 실행한다. AWS_KUBECONFIG는 새 AWS member kubeconfig 경로다.

```bash
cd /root/hybridspot-validation/provisioner
git pull --ff-only origin In-aws-create-WorkerNode
export IMG=docker.io/jeongseungjun/my-publiccloudvm-provisioner:aws_v1.3
buildah login docker.io
buildah build --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 -t "$IMG" .
buildah push "$IMG" "docker://$IMG"
```

빌드와 push가 성공한 후에만 다음을 실행한다. 기존 이미지 이름을 먼저 기록해 둔다.

```bash
kubectl --kubeconfig="$AWS_KUBECONFIG" -n remote-cluster-provisioner-system \
  get deployment remote-cluster-provisioner -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}{"\n"}'
kubectl --kubeconfig="$AWS_KUBECONFIG" -n remote-cluster-provisioner-system \
  set image deployment/remote-cluster-provisioner manager="$IMG"
kubectl --kubeconfig="$AWS_KUBECONFIG" -n remote-cluster-provisioner-system \
  rollout status deployment/remote-cluster-provisioner --timeout=180s
kubectl --kubeconfig="$AWS_KUBECONFIG" -n fluidcr-demo get nodeprovision aws-vpc-worker-001 \
  -o jsonpath='{.status.phase}{"\n"}{.status.instanceId}{"\n"}'
kubectl --kubeconfig="$AWS_KUBECONFIG" get nodes -o wide
```

새 Worker는 다른 이름으로 생성해 검증한다. 기존 NodeProvision/Secret/NetConfig/CRD를 재생성할 필요는 없다.
이번 설정 수정에는 runtime .deb 재빌드가 필요하지 않다.
