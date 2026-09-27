package controller

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Node software API contract", func() {
	newConfig := func(software map[string]interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "ml.dcn.ssu.ac.kr/v1alpha1", "kind": "NodeProvisionNetConfig",
			"metadata": map[string]interface{}{"generateName": "software-", "namespace": "default"},
			"spec":     map[string]interface{}{"softwareConfig": map[string]interface{}{"nodeSoftware": software}},
		}}
	}
	packageConfig := func() map[string]interface{} {
		return map[string]interface{}{
			"packageURL": "https://example.test/runtime.deb", "packageSHA256": strings.Repeat("a", 64),
			"crioCommit": strings.Repeat("b", 40), "criuCommit": strings.Repeat("c", 40), "adapterSHA256": strings.Repeat("d", 64),
		}
	}
	It("defaults NFS-only requests without discarding the option", func() {
		obj := newConfig(map[string]interface{}{"nfsClient": true})
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, obj)).To(Succeed()) })
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
		profile, _, err := unstructured.NestedString(obj.Object, "spec", "softwareConfig", "nodeSoftware", "runtimeProfile")
		Expect(err).NotTo(HaveOccurred())
		Expect(profile).To(Equal("Standard"))
		nfs, found, err := unstructured.NestedBool(obj.Object, "spec", "softwareConfig", "nodeSoftware", "nfsClient")
		Expect(err).NotTo(HaveOccurred())
		Expect(found && nfs).To(BeTrue())
	})
	It("accepts a complete migration runtime request", func() {
		obj := newConfig(map[string]interface{}{"runtimeProfile": "StatefulMigration", "migrationRuntime": packageConfig()})
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
	})
	It("rejects incomplete or contradictory migration profiles", func() {
		badDigest := packageConfig()
		badDigest["packageSHA256"] = "not-a-digest"
		for i, software := range []map[string]interface{}{
			{"runtimeProfile": "StatefulMigration"},
			{"runtimeProfile": "Standard", "migrationRuntime": packageConfig()},
			{"runtimeProfile": "StatefulMigration", "migrationRuntime": map[string]interface{}{}},
			{"runtimeProfile": "StatefulMigration", "migrationRuntime": badDigest},
		} {
			err := k8sClient.Create(ctx, newConfig(software))
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "case %d: %v", i, err)
		}
	})
})
