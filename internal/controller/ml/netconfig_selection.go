package ml

import (
	"context"
	"errors"
	"fmt"

	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const selectedNetConfigUID = "ml.dcn.ssu.ac.kr/selected-netconfig-uid"

var errNoLegacyNetConfig = errors.New("no legacy NetConfig configured")

// The same selector is used for creation, runtime qualification and VPN cleanup.
// An unrelated namespace must never redirect an explicitly selected worker.
func (r *NodeProvisionReconciler) selectNetConfig(ctx context.Context, np *api.NodeProvision) (*api.NodeProvisionNetConfig, error) {
	var result *api.NodeProvisionNetConfig
	if np.Spec.NetConfigName != "" {
		result = &api.NodeProvisionNetConfig{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: np.Namespace, Name: np.Spec.NetConfigName}, result); err != nil {
			return nil, err
		}
	} else {
		list := &api.NodeProvisionNetConfigList{}
		var opts []client.ListOption
		if np.Spec.NetworkMode == "VPC" {
			opts = append(opts, client.InNamespace(np.Namespace))
		}
		if err := r.List(ctx, list, opts...); err != nil {
			return nil, err
		}
		if len(list.Items) == 0 {
			return nil, errNoLegacyNetConfig
		}
		if len(list.Items) != 1 {
			return nil, fmt.Errorf("exactly one eligible NetConfig required; got %d; set spec.netConfigName for explicit namespace-local selection", len(list.Items))
		}
		result = &list.Items[0]
	}
	if !result.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("selected NetConfig is deleting")
	}
	if pinned := np.Annotations[selectedNetConfigUID]; pinned != "" && pinned != string(result.UID) {
		return nil, fmt.Errorf("selected NetConfig UID changed; refusing network reassignment")
	}
	return result, nil
}

func (r *NodeProvisionReconciler) pinNetConfig(ctx context.Context, np *api.NodeProvision) (bool, error) {
	if np.Spec.NetConfigName == "" || np.Annotations[selectedNetConfigUID] != "" {
		return false, nil
	}
	nc, err := r.selectNetConfig(ctx, np)
	if err != nil {
		return false, err
	}
	if nc.UID == "" {
		return false, fmt.Errorf("persisted NetConfig UID required")
	}
	before := np.DeepCopy()
	if np.Annotations == nil {
		np.Annotations = map[string]string{}
	}
	np.Annotations[selectedNetConfigUID] = string(nc.UID)
	return true, r.Patch(ctx, np, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}
