package runtime

import (
	"strings"
	"testing"
)

func TestInstallDisabledByDefault(t *testing.T) {
	if steps := InstallSteps(Config{}); len(steps) != 0 {
		t.Fatalf("InstallSteps returned %d steps for disabled runtime", len(steps))
	}
	if script := InstallScript(Config{}); script != "" {
		t.Fatalf("InstallScript returned content for disabled runtime: %q", script)
	}
}

func TestInstallEnabledUsesDefaults(t *testing.T) {
	cfg := Config{Enabled: true}
	steps := InstallSteps(cfg)
	if len(steps) == 0 {
		t.Fatal("InstallSteps returned no steps for enabled runtime")
	}

	script := InstallScript(cfg)
	if !strings.Contains(script, DefaultRegistry+"/"+DefaultRepository+":"+DefaultVersion) {
		t.Fatalf("InstallScript does not contain the default artifact reference")
	}
}
