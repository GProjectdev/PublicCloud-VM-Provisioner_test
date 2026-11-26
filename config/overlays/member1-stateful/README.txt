Member1 AWS/WireGuard GPU workers with StatefulMigration

This opt-in deployment overlay enables the existing identity-checked kubelet
serving CSR approver and the Member1 serving CA ConfigMap. The base deploy
ClusterRole includes approval rights restricted to kubernetes.io/kubelet-serving.
Set an immutable image digest before applying this overlay to an environment.
Verify that stateful-migration-system/kubelet-serving-ca contains the actual
member serving signer CA; do not copy another cluster's CA.

The CSR approver verifies NodeProvision UID, observed EC2 ID, provisioned IPs,
subject and SANs. Do not manually approve arbitrary CSRs or skip TLS checks.

Before releasing a workload on a newly joined worker require:
1. NodeProvision Ready (not just Node Ready or GPU allocatable).
2. Node restore-from-file qualification and package SHA256 present.
3. A kubelet serving certificate with the matching node IP and trusted signer.
4. All-rank Checkpoint/Restore functional verification for the intended workload.

2026-11-26: the pre-existing Member1 deployment lacked both approver opt-in and
approval RBAC. Existing workers masked this configuration gap. A new worker
exposed it before two-rank cross-member validation. Live correction used the
same restricted approval rules and flag; controller image was unchanged.
