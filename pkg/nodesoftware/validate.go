package nodesoftware

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
)

const (
	RuntimeProfileStandard          = "Standard"
	RuntimeProfileStatefulMigration = "StatefulMigration"

	GPUModeNone         = "None"
	GPUModeDevicePlugin = "DevicePlugin"
	GPUModeDRA          = "DRA"

	RuntimeManifestPath = "/usr/local/share/stateful-migration/runtime.json"
	CRIOBinaryPath      = "/usr/local/bin/crio"
	CRIUBinaryPath      = "/usr/local/sbin/criu"
	CUDACheckpointPath  = "/usr/local/bin/cuda-checkpoint"
	CUDAPluginPath      = "/usr/local/lib/criu/cuda_plugin.so"
)

var (
	hex64RE = regexp.MustCompile(`^[A-Fa-f0-9]{64}$`)
	hex40RE = regexp.MustCompile(`^[A-Fa-f0-9]{40}$`)
)

// Validate enforces the node software contract before rendering bootstrap.
// GPU node suitability is intentionally left to the caller.
func Validate(config *api.NodeSoftwareConfig, kubernetesVersion string) error {
	if config == nil {
		return nil
	}

	switch runtimeProfile(config.RuntimeProfile) {
	case RuntimeProfileStandard:
		if config.MigrationRuntime != nil {
			return fmt.Errorf("nodeSoftware.migrationRuntime is forbidden when runtimeProfile is %s", RuntimeProfileStandard)
		}
	case RuntimeProfileStatefulMigration:
		if _, _, _, err := parseKubernetesVersion(kubernetesVersion); err != nil {
			return fmt.Errorf("nodeSoftware.runtimeProfile StatefulMigration requires a stable Kubernetes version: %w", err)
		}
		if err := validateMigrationRuntime(config.MigrationRuntime); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported nodeSoftware.runtimeProfile %q", config.RuntimeProfile)
	}

	switch gpuMode(config.GPUMode) {
	case GPUModeNone, GPUModeDevicePlugin:
	case GPUModeDRA:
		if err := requireKubernetesAtLeast(kubernetesVersion, 1, 34, 2); err != nil {
			return fmt.Errorf("nodeSoftware.gpuMode DRA requires Kubernetes >= 1.34.2: %w", err)
		}
	default:
		return fmt.Errorf("unsupported nodeSoftware.gpuMode %q", config.GPUMode)
	}

	return nil
}

func validateMigrationRuntime(cfg *api.MigrationRuntimeConfig) error {
	if cfg == nil {
		return fmt.Errorf("nodeSoftware.migrationRuntime is required when runtimeProfile is %s", RuntimeProfileStatefulMigration)
	}
	if err := validatePackageURL(cfg.PackageURL); err != nil {
		return fmt.Errorf("nodeSoftware.migrationRuntime.packageURL: %w", err)
	}
	if !hex64RE.MatchString(cfg.PackageSHA256) {
		return fmt.Errorf("nodeSoftware.migrationRuntime.packageSHA256 must be a 64-character hex SHA256")
	}
	if !hex40RE.MatchString(cfg.CRIOCommit) {
		return fmt.Errorf("nodeSoftware.migrationRuntime.crioCommit must be a 40-character hex commit")
	}
	if !hex40RE.MatchString(cfg.CRIUCommit) {
		return fmt.Errorf("nodeSoftware.migrationRuntime.criuCommit must be a 40-character hex commit")
	}
	if !hex64RE.MatchString(cfg.AdapterSHA256) {
		return fmt.Errorf("nodeSoftware.migrationRuntime.adapterSHA256 must be a 64-character hex SHA256")
	}
	return nil
}

func validatePackageURL(raw string) error {
	if strings.Contains(raw, "#") {
		return fmt.Errorf("must not include a fragment separator")
	}
	if strings.ContainsAny(raw, " \t\r\n'\"`<>") {
		return fmt.Errorf("must not include raw whitespace, quotes, backticks, or angle brackets")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" {
		return fmt.Errorf("must use https")
	}
	if u.Host == "" {
		return fmt.Errorf("must include a host")
	}
	if u.User != nil {
		return fmt.Errorf("must not include credentials")
	}
	if u.ForceQuery || u.RawQuery != "" {
		return fmt.Errorf("must not include a query string")
	}
	if u.Fragment != "" {
		return fmt.Errorf("must not include a fragment")
	}
	if u.EscapedPath() != u.Path {
		return fmt.Errorf("path must be URL-escaped")
	}
	return nil
}

func runtimeProfile(profile string) string {
	if profile == "" {
		return RuntimeProfileStandard
	}
	return profile
}

func gpuMode(mode string) string {
	if mode == "" {
		return GPUModeNone
	}
	return mode
}

func requireKubernetesAtLeast(version string, major, minor, patch int) error {
	gotMajor, gotMinor, gotPatch, err := parseKubernetesVersion(version)
	if err != nil {
		return err
	}
	if gotMajor != major {
		return fmt.Errorf("version %s has major %d, want %d", version, gotMajor, major)
	}
	if gotMinor < minor || (gotMinor == minor && gotPatch < patch) {
		return fmt.Errorf("version %s is below %d.%d.%d", version, major, minor, patch)
	}
	return nil
}

func kubernetesMinor(version string) string {
	major, minor, _, err := parseKubernetesVersion(version)
	if err != nil {
		return version
	}
	return fmt.Sprintf("%d.%d", major, minor)
}

func parseKubernetesVersion(version string) (int, int, int, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v")
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("version %q must be stable major.minor.patch", version)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid major version %q", parts[0])
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid minor version %q", parts[1])
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid patch version %q", parts[2])
	}
	return major, minor, patch, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
