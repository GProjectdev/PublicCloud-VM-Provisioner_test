package ml

import (
	ml "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepullDiscoversPackagedAndLocalHostTools(t *testing.T) {
	for _, location := range []string{"bin", "local/bin", "missing"} {
		t.Run(location, func(t *testing.T) {
			root := t.TempDir()
			if location != "missing" {
				path := filepath.Join(root, location, "crictl")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'CALLED %s\\n' \"$*\"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.ReplaceAll(buildPrepullScript([]ml.ImagePrepull{{Image: "example.org/trainer:v1"}}), "/host/usr", root)
			out, err := exec.Command("/bin/bash", "-c", script).CombinedOutput()
			if location == "missing" {
				if err == nil || !strings.Contains(string(out), "host crictl not found") {
					t.Fatalf("missing tool accepted: %s %v", out, err)
				}
				return
			}
			if err != nil || !strings.Contains(string(out), "CALLED --runtime-endpoint unix:///var/run/crio/crio.sock pull example.org/trainer:v1") {
				t.Fatalf("host tool not called: %s %v", out, err)
			}
		})
	}
}
