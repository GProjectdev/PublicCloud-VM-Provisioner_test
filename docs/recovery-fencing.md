# Recovery fencing contract

## Scope

The AWS VPC provisioner supports an explicit, irreversible recovery fence.
This is a building block, not an automatically wired Spot recovery workflow.
No new instance is created by fencing. The caller must retain a verified
checkpoint and provision the replacement separately.

Only an authorized recovery controller may request this operation. It must
bind the NodeProvision UID, provider instance ID and recovery-operation UID
before patching. Restrict NodeProvision patch permissions accordingly.

## API

Apply the updated NodeProvision CRD to Karmada and the AWS member before
deploying the updated provisioner. Existing NodeProvision objects without a
fence retain their normal lifecycle.

The request shape is:

```yaml
spec:
  fence:
    operationUID: <immutable-recovery-operation-uid>
    instanceID: <exact-status.instanceId>
```

This request TERMINATES the instance; it is not a dry run, cordon, drain or
pause. Once set, the CRD freezes the provisioning spec, including provider,
region and credentials reference, and rejects changing or removing the fence.
Credential Secret contents can still be rotated. The controller also
refuses normal provisioning when a fence receipt exists.

1. Validate AWS, VPC, nonempty operation and exact status instance ID.
2. Persist status.fence.phase=Fencing before calling EC2.
3. Describe the exact instance; terminate it if not already shutting down.
4. Wait for a positive EC2 terminated state before publishing Fenced.
5. Keep the NodeProvision and its receipt; normal CR deletion remains the
   separate finalizer-driven cleanup path.

Neither Node NotReady, Pod disappearance, a successful TerminateInstances
submission nor EC2 InvalidInstanceID.NotFound counts as terminal proof.
NotFound may mean incorrect region/credentials. If EC2 has already purged the
terminated instance record, the controller intentionally cannot attest it.

Consumers must require matching operationUID, instanceID, NodeProvision UID,
observedGeneration and the unique AWS status provenance. Do not accept
phase=Fenced from an unrelated or recreated object.

## Verification and limits

Unit tests cover persisted intent, pending termination, provider errors,
terminal evidence, identity rejection and refusal to provision after fencing.
No real instance was terminated during local tests.

The automatic System recovery consumer and member-side use of this receipt
are not connected yet. Do not manually set SourceFenced merely because this
API exists. On-prem and non-AWS fencing are unsupported.
