package v1alpha1

import (
	"context"
	"os"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"sigs.k8s.io/yaml"
)

func TestVPCGeneratedCRDSchema(t *testing.T) {
	data, err := os.ReadFile("../../../config/crd/bases/ml.dcn.ssu.ac.kr_nodeprovisions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var external apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &external); err != nil {
		t.Fatal(err)
	}
	var internal apiextensions.CustomResourceDefinition
	if err := apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(&external, &internal, nil); err != nil {
		t.Fatal(err)
	}
	internal.Status.StoredVersions = []string{"v1alpha1"}
	if errors := validation.ValidateCustomResourceDefinition(context.Background(), &internal); len(errors) != 0 {
		t.Fatalf("invalid generated CRD (including CEL rules): %v", errors)
	}
}
