# v6.0 architecture release

Cloud Node Provisioner maps to existing `remote-cluster-provisioner`.
RemoteCluster, NodeProvision and NodeProvisionNetConfig remain the wire APIs.

Release overlays expose presentation names through annotations. Existing names,
selectors, RBAC, CRDs, mode flags, leader-election identities and reconciliation
behavior are preserved.

Run from this repository on a Linux Buildah host:

```bash
bash scripts/build-v6.0.sh
kubectl kustomize release/v6.0/provisioner > /tmp/my-publiccloudvm-provisioner-provisioner-v6.0.yaml
```

Local image: `docker.io/jeongseungjun/my-publiccloudvm-provisioner:v6.0`.
Build scripts do not push or deploy. Review and commit source before producing
publishable images so revision labels identify reviewed content. These files
do not imply that v6.0 tags have been published. Publication and cluster changes
belong to a separate release step: record digests and back up live manifests
first, retain existing secrets/certificates/kubeconfigs/storage, and use the
prior manifests and image digests for rollback.
