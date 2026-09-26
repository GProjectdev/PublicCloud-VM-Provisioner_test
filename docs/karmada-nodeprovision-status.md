# Karmada NodeProvision Status Reflection

This repo treats `NodeProvision` as a management-plane API object that is propagated by Karmada to exactly one member cluster named `aws`. The AWS member cluster runs this Provisioner controller and writes the lifecycle status. A separate SpotWatcher, implemented in `System/internal/member/spot.go`, owns only `NodeProvision.status.spot`. It writes that subtree with optimistic merge-patch retry on conflict; Provisioner copies the latest `spot` subtree onto fresh `NodeProvision` status writes and keeps normal optimistic-lock conflicts for stale Provisioner-owned status.

Apply the Karmada config from the Karmada control plane:

```bash
kubectl --kubeconfig "$KARMADA_KUBECONFIG" apply -f config/karmada/nodeprovision-aws-status.yaml
```

The file contains:

- `ResourceInterpreterCustomization/nodeprovision-status`: reflects member `status` and aggregates it back to the management template only when exactly one status item comes from member cluster `aws`. It clears top-level `status` when `aws` is absent or duplicated; it never falls back to another cluster.
- `PropagationPolicy/nodeprovision-aws-only`: keeps `NodeProvision` propagation fixed to the `aws` member cluster.

Do not propagate `NodeProvisionNetConfig` or referenced Secrets through this policy. They are member-local inputs and must exist in the AWS member cluster namespace where the materialized `NodeProvision` runs. This keeps bootstrap commands, VPN state, image-pull credentials, and cloud credentials local to the executing cluster.

`status.spot` contract:

```yaml
status:
  spot:
    atRisk: true
    signalType: aws-spot-interruption
    eventID: evt-123
    noticeTime: "2026-09-26T04:00:00Z"
    interruptionTime: "2026-09-26T04:02:00Z"
    action: terminate
    instanceID: i-0123456789abcdef0
    lastHeartbeatTime: "2026-09-26T04:00:10Z"
```

Aggregated management-plane status includes top-level `status.observedCluster: aws` when and only when the status came from the fixed `aws` member. Fresh Provisioner status writes preserve `status.spot` from the latest API object. If the `NodeProvision` resourceVersion is stale because any status writer advanced the object, Provisioner keeps the normal status-update conflict instead of retrying over Provisioner-owned lifecycle fields. SpotWatcher should update only the `spot` subtree and should not rewrite fields such as `phase`, `message`, `instanceId`, or IP fields.

SpotWatcher member deployment contract:

- Run the DaemonSet only on AWS member nodes with `nodeSelector: {ml.dcn.ssu.ac.kr/provider: AWS}`.
- Grant `get`, `list`, and `watch` on core `nodes` so SpotWatcher can read the owner labels `ml.dcn.ssu.ac.kr/node-provision`, `ml.dcn.ssu.ac.kr/node-provision-namespace`, and `ml.dcn.ssu.ac.kr/node-provision-uid`.
- Grant `get` on resource `nodeprovisions` in API group `ml.dcn.ssu.ac.kr` so SpotWatcher can read `status.instanceId` and verify it against IMDS instance identity before publishing risk.
- Grant `patch` on resource `nodeprovisions/status` in API group `ml.dcn.ssu.ac.kr` so SpotWatcher can merge-patch only `status.spot`.

'

## Reader Contract


- Read the exact top-level `NodeProvision.status` object.
- Do not read or expect `status.clusters`; this contract intentionally does not introduce a per-cluster status array.
- Treat `status.observedCluster: aws` as the provenance marker for aggregated management-plane status.
- Treat missing/empty `status` or missing `status.observedCluster` as "no valid unique aws member status". This can happen when Karmada has no `aws` status item or duplicate `aws` status items.
- Continue reading `status.spot` at the same top-level path: `NodeProvision.status.spot`.
'
## NodeProvision Generation Contract


Safe `NodeProvision` generation requirements:

- Do not set `spec.nodeLabel` to arbitrary policy metadata such as `training.dcnlab.com/policy=<policyName>`.
- Use `spec.nodeLabel` only for the worker category currently expected by this Provisioner path, especially `gpu` or `cpu` style scheduling/hardware category values.
- Set `spec.hardwareType` explicitly when capacity selection knows the hardware class. Use `gpu` for GPU capacity and `cpu` for CPU capacity. Do not leave it empty for generated policy-driven capacity unless the policy truly has no hardware requirement.
- Carry policy identity outside `spec.nodeLabel`. Preferred integration options are metadata labels/annotations on the `NodeProvision`, for example `training.dcnlab.com/policy: <policyName>`, while keeping `spec.nodeLabel` as the hardware category.
- For GPU policy capacity, generate AWS fields that point to a GPU-ready AMI/runtime path. The selected AMI and bootstrap path must be compatible with NVIDIA driver/GPU Operator expectations and the repo's GPU image pre-pull path.
- Capacity policy should expose both `hardwareType` and optional `nodeLabel` inputs so callers can request GPU/CPU capacity without corrupting the Provisioner-owned label semantics.
- SpotWatcher will rely on Provisioner-owned node owner labels plus `status.instanceId`; generated specs must still allow Provisioner to stamp node owner labels after join.
