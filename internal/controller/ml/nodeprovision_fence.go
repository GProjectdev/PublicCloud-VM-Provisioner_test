package ml

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	awsprovision "dcn.ssu.ac.kr/infra/provider/aws"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *NodeProvisionReconciler) reconcileFence(ctx context.Context, np *mlv1alpha1.NodeProvision) (ctrl.Result, error) {
	request := np.Spec.Fence
	// A removed or changed request must never restart normal provisioning.
	if request == nil {
		return ctrl.Result{}, fmt.Errorf("fenced NodeProvision cannot resume provisioning")
	}
	if prior := np.Status.Fence; prior != nil &&
		(prior.OperationUID != request.OperationUID || prior.InstanceID != request.InstanceID) {
		return ctrl.Result{}, fmt.Errorf("fence request conflicts with persisted operation")
	}
	if np.Spec.Provider != mlv1alpha1.CloudProviderAWS || np.Spec.NetworkMode != "VPC" ||
		strings.TrimSpace(request.OperationUID) == "" || request.InstanceID == "" ||
		request.InstanceID != np.Status.InstanceID || np.UID == "" {
		return r.recordFence(ctx, np, "Rejected", "requires AWS VPC and exact provisioned instance identity")
	}
	if prior := np.Status.Fence; prior != nil && prior.Phase == "Fenced" {
		if prior.ObservedGeneration != np.Generation {
			return r.recordFence(ctx, np, "Fenced", prior.Message)
		}
		return ctrl.Result{}, nil
	}
	if np.Status.Fence == nil || np.Status.Fence.Phase != "Fencing" {
		result, err := r.recordFence(ctx, np, "Fencing", "termination intent persisted; awaiting provider confirmation")
		result.RequeueAfter = time.Second
		return result, err
	}
	terminate := r.FenceInstance
	if terminate == nil {
		terminate = r.fenceAWSInstance
	}
	if err := terminate(ctx, np); err != nil {
		if errors.Is(err, awsprovision.ErrInstanceTerminationPending) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("provider fence not confirmed: %w", err)
	}
	return r.recordFence(ctx, np, "Fenced", "provider confirmed instance termination")
}

func (r *NodeProvisionReconciler) recordFence(ctx context.Context, np *mlv1alpha1.NodeProvision, phase, message string) (ctrl.Result, error) {
	np.Status.Fence = &mlv1alpha1.NodeProvisionFenceStatus{
		OperationUID: np.Spec.Fence.OperationUID, InstanceID: np.Spec.Fence.InstanceID,
		ObservedGeneration: np.Generation, Phase: phase, Message: message, ObservedAt: metav1.Now(),
	}
	// Ready would incorrectly advertise this node as available capacity.
	np.Status.Phase = mlv1alpha1.NodeProvisionPhaseFailed
	np.Status.Message = "recovery fence: " + message
	return ctrl.Result{}, r.updateNodeProvisionStatus(ctx, np)
}

func (r *NodeProvisionReconciler) fenceAWSInstance(ctx context.Context, np *mlv1alpha1.NodeProvision) error {
	secret, err := r.getSecret(ctx, np)
	if err != nil {
		secret, err = r.getControllerCredsSecret(ctx, np)
		if err != nil {
			return fmt.Errorf("no credentials for instance fence: %w", err)
		}
	}
	creds, err := r.resolveAWSCreds(ctx, np.Spec.Region, secret)
	if err != nil {
		creds = awsprovision.ResolveAWSCredentials(secret)
	}
	err = awsprovision.FenceInstance(ctx, np, creds, np.Spec.Fence.InstanceID)
	if awsprovision.IsAWSAuthFailure(err) {
		if r.CredMgr != nil {
			r.CredMgr.Evict(secret.Namespace, secret.Name)
		}
		creds = awsprovision.StaticCredsNoSession(awsprovision.ResolveAWSCredentials(secret))
		return awsprovision.FenceInstance(ctx, np, creds, np.Spec.Fence.InstanceID)
	}
	return err
}
