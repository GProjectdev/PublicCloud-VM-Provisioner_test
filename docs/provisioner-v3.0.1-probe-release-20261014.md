# Provisioner CRI-O probe fix (2026-10-14)

## Scope
Only the Provisioner image changes. No CRD, System controller, or trainer image update is required.
CRI-O config renders scalar values with comment prefixes. The probe now accepts exact unique assignments in the real section, including commented values. Missing, ambiguous, false, wrong-section, and wrong-runtime values still fail.
This checks configured values and existing runtime prerequisites; it is not an end-to-end restore test.

## 1. Finish the previous run
Stop and scoped-cleanup costb07, preserve evidence, and verify its EC2 instances terminate.
Do not delete shared CIFAR-10 data or SSH secrets belonging to other runs.

## 2. Update source on MGMT
Run inside the existing Provisioner checkout. Preserve local modifications; do not reset them.
```bash
cd /root/hybridspot-validation/provisioner
git status --short
git fetch origin refs/heads/restore-automation-20260928:refs/remotes/origin/restore-automation-20260928
git merge --ff-only origin/restore-automation-20260928
git rev-parse HEAD
go test -p 2 -mod=readonly ./...
go vet -p 2 ./...
```

## 3. Build and push
Use a new tag to avoid reusing the cached v3.0 image. Run each stage only after the previous command succeeds.
```bash
export IMAGE=docker.io/jeongseungjun/my-publiccloudvm-provisioner:v3.0.1
buildah login docker.io
buildah bud --arch amd64 --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 -f Dockerfile -t "$IMAGE" .
buildah push "$IMAGE" "docker://$IMAGE"
```

## 4. Replace the AWS-cluster controller from MGMT
```bash
export IMAGE=docker.io/jeongseungjun/my-publiccloudvm-provisioner:v3.0.1
kubectl --kubeconfig="${AWS_KUBECONFIG:?}" -n remote-cluster-provisioner-system get deployment remote-cluster-provisioner -o yaml > provisioner-before-v3.0.1.yaml
kubectl --kubeconfig="${AWS_KUBECONFIG:?}" -n remote-cluster-provisioner-system set image deployment/remote-cluster-provisioner "manager=${IMAGE:?}"
kubectl --kubeconfig="${AWS_KUBECONFIG:?}" -n remote-cluster-provisioner-system rollout status deployment/remote-cluster-provisioner --timeout=180s
kubectl --kubeconfig="${AWS_KUBECONFIG:?}" -n remote-cluster-provisioner-system get pods -o json | jq '.items[] | {name:.metadata.name, images:[.status.containerStatuses[]? | {image,imageID}]}'
```
Record the imageID. Do not overwrite this tag for later fixes.

## 5. Run a fresh B measurement
Use costb08 only if unused. Keep the existing 43000-step config and other A/B comparison settings.
```bash
cd /root/hybridspot-validation/System
python3 evaluation/cost.py render --config evaluation/config.cost.local.json --run-id costb08 --arm B --out evidence/cost/costb08
jq '{run_id,phase}' evidence/cost/costb08/run.json
python3 evaluation/cost.py prepare --evidence evidence/cost/costb08
python3 evaluation/cost.py start --evidence evidence/cost/costb08
```
Require rendered phase and matching run_id before prepare; start only after successful prepare.
In a second terminal:
```bash
kubectl --kubeconfig="${AWS_KUBECONFIG:?}" -n fluidcr-realign-121040 get nodeprovisions -o custom-columns='NAME:.metadata.name,PHASE:.status.phase,MESSAGE:.status.message'
tail -n 20 -F /root/hybridspot-validation/System/evidence/cost/costb08/runner-events.jsonl
```
Both initial NodeProvisions must reach Ready, not merely Kubernetes Nodes.
Later, replacement nodes must also pass verification. Risk consumption is not proof of replacement completion.
Preserve any new probe error; never bypass certification or manually stamp restore capability.
The final live acceptance criteria are successful replacement/restore and all_ranks_completed.

