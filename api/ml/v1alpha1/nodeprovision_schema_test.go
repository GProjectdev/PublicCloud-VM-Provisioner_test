package v1alpha1

import (
	"context"
	"os"
	"strings"
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
func TestKarmadaNodeProvisionRICAWSOnlySafety(t *testing.T) {
	data, err := os.ReadFile("../../../config/karmada/nodeprovision-aws-status.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ric := string(data)
	required := []string{
		`if statusItems[i].clusterName == "aws" then`,
		"awsCount = awsCount + 1",
		"if awsCount ~= 1 or selectedStatus == nil then",
		"desiredObj.status = {}",
		`desiredObj.status.observedCluster = "aws"`,
	}
	for _, pattern := range required {
		if !strings.Contains(ric, pattern) {
			t.Fatalf("Karmada NodeProvision RIC missing safety pattern %q", pattern)
		}
	}
	forbidden := []string{
		"if selectedStatus == nil then",
		"status.clusters",
		"desiredObj.status.clusters",
	}
	for _, pattern := range forbidden {
		if strings.Contains(ric, pattern) {
			t.Fatalf("Karmada NodeProvision RIC contains forbidden fallback/shape pattern %q", pattern)
		}
	}

	crdData, err := os.ReadFile("../../../config/crd/bases/ml.dcn.ssu.ac.kr_nodeprovisions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(crdData), "observedCluster:") {
		t.Fatal("NodeProvision CRD is missing top-level status.observedCluster provenance field")
	}
}
