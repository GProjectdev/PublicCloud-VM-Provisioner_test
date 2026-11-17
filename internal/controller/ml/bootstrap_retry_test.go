package ml

import (
	"context"
	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	provider "dcn.ssu.ac.kr/infra/provider/aws"
	"errors"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"strings"
	"testing"
	"time"
)

func TestInterruptedBootstrapRetrySafety(t *testing.T) {
	for _, tc := range []string{"terminal", "running", "describe-error", "ever-ready", "stale-event", "pod-present", "foreign-node", "retry-limit", "resume-journal", "no-watcher"} {
		t.Run(tc, func(t *testing.T) {
			s := runtime.NewScheme()
			_ = core.AddToScheme(s)
			_ = api.AddToScheme(s)
			np := &api.NodeProvision{ObjectMeta: meta.ObjectMeta{Name: "worker", Namespace: "test", UID: "owner", Annotations: map[string]string{}}, Spec: api.NodeProvisionSpec{Provider: api.CloudProviderAWS, MarketType: api.InstanceMarketTypeSpot, NetworkMode: "VPC"}, Status: api.NodeProvisionStatus{InstanceID: "i-dead", PrivateIP: "10.0.0.5", Phase: api.NodeProvisionPhaseVerifyingHealth, Spot: &api.NodeProvisionSpotStatus{AtRisk: true, InstanceID: "i-dead"}}}
			objects := []client.Object{np}
			switch tc {
			case "ever-ready":
				now := meta.NewTime(time.Now())
				np.Status.CompletionTime = &now
			case "no-watcher":
				np.Status.Spot = nil
			case "stale-event":
				np.Status.Spot.InstanceID = "i-other"
			case "retry-limit":
				np.Annotations[provider.BootstrapHistoryAnnotation] = "i-1,i-2,i-3"
			case "resume-journal":
				np.Annotations[provider.BootstrapHistoryAnnotation] = "i-dead"
			case "pod-present", "foreign-node":
				n := &core.Node{ObjectMeta: meta.ObjectMeta{Name: "worker", Labels: map[string]string{nodeProvisionUIDLabel: "owner"}}, Spec: core.NodeSpec{ProviderID: "aws:///az/i-dead"}}
				if tc == "foreign-node" {
					n.Labels[nodeProvisionUIDLabel] = "foreign"
				}
				objects = append(objects, n)
				if tc == "pod-present" {
					objects = append(objects, &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "trainer", Namespace: "test"}, Spec: core.PodSpec{NodeName: "worker"}})
				}
			}
			c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(np).WithObjects(objects...).Build()
			calls := 0
			r := &NodeProvisionReconciler{Client: c, Scheme: s, BootstrapInstanceTerminated: func(context.Context, *api.NodeProvision) (bool, error) {
				calls++
				if tc == "describe-error" {
					return false, errors.New("denied")
				}
				return tc != "running" && tc != "stale-event", nil
			}}
			handled, _, e := r.reconcileInterruptedBootstrap(context.Background(), np)
			fresh := &api.NodeProvision{}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(np), fresh); err != nil {
				t.Fatal(err)
			}
			switch tc {
			case "ever-ready":
				if handled || calls != 0 {
					t.Fatal("eligible outside initial matching bootstrap")
				}
			case "terminal", "resume-journal", "no-watcher":
				if !handled || e != nil || fresh.Status.InstanceID != "" || fresh.Annotations[provider.BootstrapHistoryAnnotation] != "i-dead" {
					t.Fatalf("retry state invalid: %v %+v", e, fresh)
				}
				if !strings.Contains(fresh.Status.Message, "positively confirmed") {
					t.Fatal("missing retry reason")
				}
			default:
				if fresh.Status.InstanceID != "i-dead" {
					t.Fatal("lost instance identity without safety proof")
				}
				if tc != "running" && tc != "stale-event" && e == nil {
					t.Fatal("unsafe state accepted")
				}
			}
		})
	}
}
