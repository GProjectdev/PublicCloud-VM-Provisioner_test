package ml

import (
	"context"
	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"strings"
	"testing"
)

func TestReadyNodeHonorsExplicitRestoreCertification(t *testing.T) {
	for _, name := range []string{"missing", "certified", "changed-package", "opt-out"} {
		t.Run(name, func(t *testing.T) {
			s := runtime.NewScheme()
			_ = corev1.AddToScheme(s)
			_ = api.AddToScheme(s)
			np := &api.NodeProvision{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"}, Spec: api.NodeProvisionSpec{Provider: api.CloudProviderAWS}, Status: api.NodeProvisionStatus{NodeName: "worker"}}
			cfg := &api.NodeProvisionNetConfig{ObjectMeta: metav1.ObjectMeta{Name: "net", Namespace: "default"}}
			cfg.Spec.SoftwareConfig.NodeSoftware = &api.NodeSoftwareConfig{RuntimeProfile: "StatefulMigration", MigrationRuntime: &api.MigrationRuntimeConfig{CertifyRestore: name != "opt-out", PackageSHA256: strings.Repeat("a", 64)}}
			n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
			if name == "certified" || name == "changed-package" {
				n.Labels = map[string]string{"migration.dcnlab.com/restore-from-file": "true"}
				n.Annotations = map[string]string{"migration.dcnlab.com/restore-package-sha256": strings.Repeat("a", 64)}
			}
			if name == "changed-package" {
				n.Annotations["migration.dcnlab.com/restore-package-sha256"] = strings.Repeat("b", 64)
			}
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(np, cfg, n).Build()
			r := &NodeProvisionReconciler{Client: c, Scheme: s}
			pending, err := r.restoreCertificationPending(context.Background(), np)
			if err != nil {
				t.Fatal(err)
			}
			if pending != (name == "missing" || name == "changed-package") {
				t.Fatalf("pending=%v", pending)
			}
		})
	}
}
