/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ml

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
)

var _ = Describe("NodeProvision Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		nodeprovision := &mlv1alpha1.NodeProvision{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind NodeProvision")
			err := k8sClient.Get(ctx, typeNamespacedName, nodeprovision)
			if err != nil && errors.IsNotFound(err) {
				resource := &mlv1alpha1.NodeProvision{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					// TODO(user): Specify other spec details if needed.
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &mlv1alpha1.NodeProvision{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance NodeProvision")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &NodeProvisionReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
var _ = Describe("NodeProvision status preservation", func() {
	newSpotStatus := func() *mlv1alpha1.NodeProvisionSpotStatus {
		noticeTime := time.Date(2026, 9, 26, 4, 0, 0, 0, time.UTC)
		interruptionTime := noticeTime.Add(2 * time.Minute)
		heartbeatTime := noticeTime.Add(10 * time.Second)
		return &mlv1alpha1.NodeProvisionSpotStatus{
			AtRisk:            true,
			SignalType:        "aws-spot-interruption",
			EventID:           "evt-123",
			NoticeTime:        noticeTime.Format(time.RFC3339),
			InterruptionTime:  interruptionTime.Format(time.RFC3339),
			Action:            "terminate",
			InstanceID:        "i-0123456789abcdef0",
			LastHeartbeatTime: heartbeatTime.Format(time.RFC3339),
		}
	}

	createNodeProvision := func(ctx context.Context, key types.NamespacedName) *mlv1alpha1.NodeProvision {
		np := &mlv1alpha1.NodeProvision{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		}
		Expect(k8sClient.Create(ctx, np)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, np)
		})
		return np
	}

	It("preserves existing SpotWatcher status on a fresh Provisioner status write", func() {
		ctx := context.Background()
		key := types.NamespacedName{Name: "spot-preserve", Namespace: "default"}
		createNodeProvision(ctx, key)

		spotWatcherView := &mlv1alpha1.NodeProvision{}
		Expect(k8sClient.Get(ctx, key, spotWatcherView)).To(Succeed())
		spotWatcherView.Status.Spot = newSpotStatus()
		Expect(k8sClient.Status().Update(ctx, spotWatcherView)).To(Succeed())

		provisionerView := &mlv1alpha1.NodeProvision{}
		Expect(k8sClient.Get(ctx, key, provisionerView)).To(Succeed())
		reconciler := &NodeProvisionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		provisionerView.Status.Phase = mlv1alpha1.NodeProvisionPhaseReady
		provisionerView.Status.Message = "Node successfully joined cluster"
		provisionerView.Status.Progress = 100
		Expect(reconciler.updateNodeProvisionStatus(ctx, provisionerView)).To(Succeed())

		updated := &mlv1alpha1.NodeProvision{}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Status.Phase).To(Equal(mlv1alpha1.NodeProvisionPhaseReady))
		Expect(updated.Status.Message).To(Equal("Node successfully joined cluster"))
		Expect(updated.Status.Progress).To(Equal(100))
		Expect(updated.Status.Spot).NotTo(BeNil())
		Expect(*updated.Status.Spot).To(Equal(*spotWatcherView.Status.Spot))
	})

	It("does not roll back concurrent Provisioner-owned status", func() {
		ctx := context.Background()
		key := types.NamespacedName{Name: "spot-owned-conflict", Namespace: "default"}
		createNodeProvision(ctx, key)

		staleProvisionerView := &mlv1alpha1.NodeProvision{}
		Expect(k8sClient.Get(ctx, key, staleProvisionerView)).To(Succeed())

		advanced := &mlv1alpha1.NodeProvision{}
		Expect(k8sClient.Get(ctx, key, advanced)).To(Succeed())
		advanced.Status.Phase = mlv1alpha1.NodeProvisionPhaseCreatingInstance
		advanced.Status.Message = "Creating EC2 instance"
		advanced.Status.Progress = 25
		Expect(k8sClient.Status().Update(ctx, advanced)).To(Succeed())

		reconciler := &NodeProvisionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		staleProvisionerView.Status.Phase = mlv1alpha1.NodeProvisionPhaseReady
		staleProvisionerView.Status.Message = "stale ready write"
		staleProvisionerView.Status.Progress = 100
		err := reconciler.updateNodeProvisionStatus(ctx, staleProvisionerView)
		Expect(errors.IsConflict(err)).To(BeTrue())

		updated := &mlv1alpha1.NodeProvision{}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Status.Phase).To(Equal(mlv1alpha1.NodeProvisionPhaseCreatingInstance))
		Expect(updated.Status.Message).To(Equal("Creating EC2 instance"))
		Expect(updated.Status.Progress).To(Equal(25))
	})

	It("rejects stale status writes for a recreated NodeProvision", func() {
		ctx := context.Background()
		key := types.NamespacedName{Name: "spot-recreated", Namespace: "default"}
		oldNP := createNodeProvision(ctx, key)

		staleProvisionerView := &mlv1alpha1.NodeProvision{}
		Expect(k8sClient.Get(ctx, key, staleProvisionerView)).To(Succeed())
		Expect(k8sClient.Delete(ctx, oldNP)).To(Succeed())
		Eventually(func() bool {
			current := &mlv1alpha1.NodeProvision{}
			return errors.IsNotFound(k8sClient.Get(ctx, key, current))
		}, time.Second*10, time.Millisecond*100).Should(BeTrue())

		newNP := &mlv1alpha1.NodeProvision{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
		Expect(k8sClient.Create(ctx, newNP)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, newNP)
		})

		reconciler := &NodeProvisionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		staleProvisionerView.Status.Phase = mlv1alpha1.NodeProvisionPhaseReady
		err := reconciler.updateNodeProvisionStatus(ctx, staleProvisionerView)
		Expect(errors.IsConflict(err)).To(BeTrue())

		updated := &mlv1alpha1.NodeProvision{}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.UID).NotTo(Equal(staleProvisionerView.UID))
		Expect(updated.Status.Phase).To(BeEmpty())
	})
})
