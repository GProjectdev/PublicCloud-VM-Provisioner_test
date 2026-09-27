package main

import (
	"testing"

	mlcontroller "dcn.ssu.ac.kr/infra/internal/controller/ml"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNewKubeletServingCSRApproverWiresOptInFlag(t *testing.T) {
	scheme := runtime.NewScheme()
	ca := mlcontroller.KubeletServingCAConfig{
		SourceKind:      "Secret",
		SourceNamespace: "kube-system",
		SourceName:      "custom-kubelet-serving-ca",
		SourceKey:       "bundle.pem",
	}

	approver := newKubeletServingCSRApprover(fake.NewClientBuilder().WithScheme(scheme).Build(), scheme, ca, true)
	if !approver.EnableKubeletServingCSRApproval {
		t.Fatal("expected enable-kubelet-serving-csr-approval flag to be wired into approver")
	}
	if approver.KubeletServingCA != ca {
		t.Fatalf("approver CA config mismatch: got %#v want %#v", approver.KubeletServingCA, ca)
	}
}
