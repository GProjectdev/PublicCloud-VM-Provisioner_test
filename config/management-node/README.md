# Remote Cluster Provisioner management-node deployment

This overlay installs the controller on a node labeled
`workload-role=management`.

```bash
kubectl label node <MANAGEMENT_NODE_NAME> workload-role=management --overwrite

kubectl kustomize config/management-node > /tmp/remote-cluster-provisioner.yaml
kubectl apply --server-side --dry-run=server \
  -f /tmp/remote-cluster-provisioner.yaml
kubectl apply --server-side -f /tmp/remote-cluster-provisioner.yaml

kubectl rollout status deployment/remote-cluster-provisioner-controller-manager \
  -n remote-cluster-provisioner-system
kubectl get pods -n remote-cluster-provisioner-system -o wide
```

Change the image in `kustomization.yaml` if a different Registry, repository,
or tag is used. Apply the AWS and VPN Secrets separately; this overlay does not
contain credentials.
