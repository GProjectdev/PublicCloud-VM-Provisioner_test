package ml

import (
	"context"
	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	provider "dcn.ssu.ac.kr/infra/provider/aws"
	"fmt"
	core "k8s.io/api/core/v1"
	errors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"time"
)

func (r *NodeProvisionReconciler) reconcileInterruptedBootstrap(ctx context.Context, np *api.NodeProvision) (bool, ctrl.Result, error) {
	result := ctrl.Result{RequeueAfter: 15 * time.Second}
	if np.Spec.Provider != api.CloudProviderAWS || np.Spec.NetworkMode != "VPC" || np.Status.CompletionTime != nil || np.Status.InstanceID == "" || np.Spec.MarketType != api.InstanceMarketTypeSpot {
		return false, ctrl.Result{}, nil
	}
	history := np.Annotations[provider.BootstrapHistoryAnnotation]
	previous := []string{}
	if history != "" {
		previous = strings.Split(history, ",")
	}
	recorded := len(previous) > 0 && previous[len(previous)-1] == np.Status.InstanceID
	if len(previous) >= 3 && !recorded {
		return true, result, fmt.Errorf("bootstrap retry limit reached; operator review required")
	}
	probe := r.BootstrapInstanceTerminated
	if probe == nil {
		probe = func(ctx context.Context, n *api.NodeProvision) (bool, error) {
			s, e := r.getSecret(ctx, n)
			if e != nil {
				return false, e
			}
			c, e := r.resolveAWSCreds(ctx, n.Spec.Region, s)
			if e != nil {
				return false, e
			}
			return provider.BootstrapInstanceTerminated(ctx, n, c)
		}
	}
	dead, e := probe(ctx, np)
	if e != nil {
		return true, result, e
	}
	if !dead {
		return false, ctrl.Result{}, nil
	}
	name := np.Status.NodeName
	if name == "" {
		name = np.Name
	}
	node := &core.Node{}
	e = r.Get(ctx, client.ObjectKey{Name: name}, node)
	if e != nil && !errors.IsNotFound(e) {
		return true, result, e
	}
	if e == nil {
		if node.Labels[nodeProvisionUIDLabel] != string(np.UID) || !strings.HasSuffix(node.Spec.ProviderID, "/"+np.Status.InstanceID) {
			return true, result, fmt.Errorf("bootstrap node owner/provider identity mismatch")
		}
		pods := &core.PodList{}
		if e = r.List(ctx, pods); e != nil {
			return true, result, e
		}
		for _, p := range pods.Items {
			if p.Spec.NodeName != name {
				continue
			}
			daemon := false
			for _, o := range p.OwnerReferences {
				if o.APIVersion == "apps/v1" && o.Kind == "DaemonSet" && o.Controller != nil && *o.Controller {
					daemon = true
				}
			}
			if !daemon {
				return true, result, fmt.Errorf("bootstrap node has non-DaemonSet Pod %s/%s; restore review required", p.Namespace, p.Name)
			}
		}
		// Provider termination is positively confirmed; use normal controller-owned
		// Node cleanup. No external finalizer is removed, and no Pod is force-deleted.
		done, e := r.deleteProvisionedNode(ctx, np)
		if e != nil || !done {
			return true, result, e
		}
	}
	if !recorded {
		patch := client.MergeFrom(np.DeepCopy())
		if np.Annotations == nil {
			np.Annotations = map[string]string{}
		}
		previous = append(previous, np.Status.InstanceID)
		np.Annotations[provider.BootstrapHistoryAnnotation] = strings.Join(previous, ",")
		if e = r.Patch(ctx, np, patch); e != nil {
			return true, result, e
		}
	}
	// History is durable before clearing the terminal instance ID. A new launch
	// uses a new idempotency token; a restarted controller resumes the same retry.
	now := meta.Now()
	np.Status = api.NodeProvisionStatus{StartTime: &now, LastUpdated: &now, Message: "Retrying initial bootstrap after positively confirmed Spot termination"}
	if e = r.updateNodeProvisionStatus(ctx, np); e != nil {
		return true, result, e
	}
	return true, ctrl.Result{RequeueAfter: time.Second}, nil
}
