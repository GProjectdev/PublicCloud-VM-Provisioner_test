// Package gcp implements GCP Compute Engine node provisioning for the
// NodeProvision controller.
package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	pkgruntime "dcn.ssu.ac.kr/infra/pkg/runtime"
	sshhelper "dcn.ssu.ac.kr/infra/pkg/ssh"
	awsbootstrap "dcn.ssu.ac.kr/infra/provider/aws"
	onprem "dcn.ssu.ac.kr/infra/provider/onprem"
	corev1 "k8s.io/api/core/v1"
)

const (
	computeBaseURL        = "https://compute.googleapis.com/compute/v1"
	computeCloudScope     = "https://www.googleapis.com/auth/cloud-platform"
	defaultNetwork        = "global/networks/default"
	defaultImageProject   = "ubuntu-os-cloud"
	defaultImageFamily    = "ubuntu-2204-lts"
	defaultRootDiskSizeGB = 50
)

// GCPCredentials holds a service-account JSON key resolved from a Kubernetes Secret.
// If ServiceAccountJSON is empty, the GCP client uses Application Default Credentials.
type GCPCredentials struct {
	ServiceAccountJSON []byte
}

// ProvisionResult contains the Compute Engine instance data returned after launch.
type ProvisionResult struct {
	InstanceID string
	PrivateIP  string
	PublicIP   string
	VpnIP      string
	PublicKey  string
}

// ResolveGCPCredentials extracts a service-account JSON key from a secret.
func ResolveGCPCredentials(secret *corev1.Secret) GCPCredentials {
	for _, key := range []string{"serviceAccountJSON", "service-account.json", "credentials.json", "key.json"} {
		if v, ok := secret.Data[key]; ok && len(v) > 0 {
			return GCPCredentials{ServiceAccountJSON: v}
		}
	}
	return GCPCredentials{}
}

// ValidateGCPConfig checks that all required GCP parameters are present.
func ValidateGCPConfig(spec mlv1alpha1.NodeProvisionSpec) error {
	if spec.InstanceType == "" {
		return fmt.Errorf("spec.instanceType is required for GCP provider")
	}
	if spec.GCPConfig == nil {
		return fmt.Errorf("spec.gcpConfig is required for GCP provider")
	}
	if spec.GCPConfig.ProjectID == "" {
		return fmt.Errorf("spec.gcpConfig.projectId is required")
	}
	if zone(spec) == "" {
		return fmt.Errorf("spec.gcpConfig.zone is required for GCP provider")
	}
	action := strings.ToUpper(strings.TrimSpace(spec.SpotTerminationAction))
	if action != "" && action != "STOP" && action != "DELETE" {
		return fmt.Errorf("spec.spotTerminationAction must be STOP or DELETE for GCP")
	}
	return nil
}

// ProvisionGCPNode performs the full GCP provisioning workflow:
//  1. Allocate a VPN IP from the NodeProvisionNetConfig range.
//  2. Generate a WireGuard keypair.
//  3. Register the peer on the VPN server.
//  4. Build a startup script that installs packages, configures VPN, and joins the cluster.
//  5. Launch the Compute Engine instance with that startup script.
func ProvisionGCPNode(
	ctx context.Context,
	nodeProvision *mlv1alpha1.NodeProvision,
	creds GCPCredentials,
	vpnServerClient *sshhelper.Client,
	netNodeConfig *mlv1alpha1.NodeProvisionNetConfig,
	runtimeCfg pkgruntime.Config,
) (*ProvisionResult, error) {
	name := nodeProvision.Name
	log.Printf("[INFO] NodeProvision/%s: GCP validation successful", name)

	vpnIP, err := onprem.AllocateVPNIP(
		vpnServerClient,
		*netNodeConfig.Spec.VPNRange,
		netNodeConfig.Status.UsedIPAddresses,
	)
	if err != nil {
		return nil, fmt.Errorf("allocating VPN IP: %w", err)
	}
	log.Printf("[INFO] NodeProvision/%s: Assigned VPN IP %s", name, vpnIP)

	privateKey, publicKey, err := onprem.GenerateWireGuardKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generating WireGuard keypair: %w", err)
	}

	wgConfig, err := onprem.BuildClientWGConfig(
		vpnServerClient,
		vpnIP,
		*netNodeConfig.Spec.VPNRange,
		netNodeConfig.Spec.VPNServerPublicConfig.PublicIP,
		parsePort(netNodeConfig.Spec.VPNServerPublicConfig.VPNPort, 51820),
		privateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("building WireGuard client config: %w", err)
	}

	if err := onprem.RegisterVPNPeer(vpnServerClient, publicKey, vpnIP); err != nil {
		return nil, fmt.Errorf("registering VPN peer: %w", err)
	}
	log.Printf("[INFO] NodeProvision/%s: VPN peer registered (vpnIP=%s)", name, vpnIP)

	clean := strings.TrimPrefix(netNodeConfig.Spec.SoftwareConfig.KubernetesVersion, "v")
	parts := strings.Split(clean, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid kubernetes version: %s", netNodeConfig.Spec.SoftwareConfig.KubernetesVersion)
	}
	crioVersion := fmt.Sprintf("%s.%s", parts[0], parts[1])

	labels := []string{}
	if nodeProvision.Spec.NodeLabel != "" {
		labels = append(labels, fmt.Sprintf("hardware-type=%s", nodeProvision.Spec.NodeLabel))
	}

	startupScript, err := awsbootstrap.BuildStartupScript(awsbootstrap.CloudInitParams{
		WGConfig:               wgConfig,
		VpnIP:                  vpnIP,
		JoinCommand:            netNodeConfig.Status.ClusterJoinCommand,
		KubernetesVersion:      clean,
		KubernetesMinorVersion: crioVersion,
		NodeName:               name,
		Labels:                 labels,
		SSHUsername:            nodeProvision.Spec.SSHUsernameOverride,
		IsGPUNode:              strings.EqualFold(nodeProvision.Spec.NodeLabel, "gpu"),
		RuntimeRegistryUser:    runtimeCfg.Username,
		RuntimeRegistryToken:   runtimeCfg.Token,
		RuntimeRegistry:        runtimeCfg.Registry,
		RuntimeRepository:      runtimeCfg.Repository,
		RuntimeVersion:         runtimeCfg.Version,
		RuntimeOrasVersion:     runtimeCfg.OrasVersion,
	})
	if err != nil {
		return &ProvisionResult{VpnIP: vpnIP, PublicKey: publicKey}, fmt.Errorf("building startup script: %w", err)
	}

	client, err := newHTTPClient(ctx, creds)
	if err != nil {
		return &ProvisionResult{VpnIP: vpnIP, PublicKey: publicKey}, fmt.Errorf("creating GCP HTTP client: %w", err)
	}

	endpoint := fmt.Sprintf("%s/projects/%s/zones/%s/instances",
		computeBaseURL,
		url.PathEscape(nodeProvision.Spec.GCPConfig.ProjectID),
		url.PathEscape(zone(nodeProvision.Spec)),
	)
	log.Printf("[INFO] NodeProvision/%s: Creating GCP instance (type=%s zone=%s)",
		name, nodeProvision.Spec.InstanceType, zone(nodeProvision.Spec))
	if err := doJSON(ctx, client, http.MethodPost, endpoint, buildInstance(nodeProvision, startupScript), nil); err != nil {
		return &ProvisionResult{VpnIP: vpnIP, PublicKey: publicKey}, fmt.Errorf("launching GCP instance: %w", err)
	}

	log.Printf("[INFO] NodeProvision/%s: GCP instance %s creation requested", name, name)
	return &ProvisionResult{
		InstanceID: name,
		VpnIP:      vpnIP,
		PublicKey:  publicKey,
	}, nil
}

// WaitForInstanceRunning polls Compute Engine until the instance reaches RUNNING.
func WaitForInstanceRunning(
	ctx context.Context,
	nodeProvision *mlv1alpha1.NodeProvision,
	creds GCPCredentials,
	instanceID string,
) (privateIP, publicIP string, err error) {
	client, err := newHTTPClient(ctx, creds)
	if err != nil {
		return "", "", fmt.Errorf("creating GCP HTTP client: %w", err)
	}
	endpoint := fmt.Sprintf("%s/projects/%s/zones/%s/instances/%s",
		computeBaseURL,
		url.PathEscape(nodeProvision.Spec.GCPConfig.ProjectID),
		url.PathEscape(zone(nodeProvision.Spec)),
		url.PathEscape(instanceID),
	)

	var inst instanceResponse
	if err := doJSON(ctx, client, http.MethodGet, endpoint, nil, &inst); err != nil {
		return "", "", fmt.Errorf("getting instance %s: %w", instanceID, err)
	}
	if inst.Status != "RUNNING" {
		return "", "", nil
	}

	for _, ni := range inst.NetworkInterfaces {
		if privateIP == "" {
			privateIP = ni.NetworkIP
		}
		for _, ac := range ni.AccessConfigs {
			if publicIP == "" {
				publicIP = ac.NatIP
			}
		}
	}
	log.Printf("[INFO] NodeProvision/%s: GCP instance running privateIP=%s publicIP=%s",
		nodeProvision.Name, privateIP, publicIP)
	return privateIP, publicIP, nil
}

// TerminateInstance deletes a Compute Engine VM as part of deprovisioning.
func TerminateInstance(
	ctx context.Context,
	nodeProvision *mlv1alpha1.NodeProvision,
	creds GCPCredentials,
	instanceID string,
) error {
	client, err := newHTTPClient(ctx, creds)
	if err != nil {
		return fmt.Errorf("creating GCP HTTP client: %w", err)
	}
	endpoint := fmt.Sprintf("%s/projects/%s/zones/%s/instances/%s",
		computeBaseURL,
		url.PathEscape(nodeProvision.Spec.GCPConfig.ProjectID),
		url.PathEscape(zone(nodeProvision.Spec)),
		url.PathEscape(instanceID),
	)
	log.Printf("[INFO] NodeProvision/%s: Deleting GCP instance %s", nodeProvision.Name, instanceID)
	if err := doJSON(ctx, client, http.MethodDelete, endpoint, nil, nil); err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			log.Printf("[INFO] NodeProvision/%s: GCP instance %s already gone", nodeProvision.Name, instanceID)
			return nil
		}
		return fmt.Errorf("deleting instance %s: %w", instanceID, err)
	}
	return nil
}

var nodeLabelInstanceTypes = map[string]string{
	"cpu": "e2-standard-4",
	"gpu": "n1-standard-4",
}

// DefaultInstanceTypeForLabel returns the recommended GCP machine type for the
// given nodeLabel. Returns an empty string when no mapping is defined.
func DefaultInstanceTypeForLabel(nodeLabel string) string {
	return nodeLabelInstanceTypes[strings.ToLower(nodeLabel)]
}

func newHTTPClient(ctx context.Context, creds GCPCredentials) (*http.Client, error) {
	if len(creds.ServiceAccountJSON) > 0 {
		credentials, err := google.CredentialsFromJSON(ctx, creds.ServiceAccountJSON, computeCloudScope)
		if err != nil {
			return nil, fmt.Errorf("parsing service-account JSON: %w", err)
		}
		return oauth2.NewClient(ctx, credentials.TokenSource), nil
	}
	credentials, err := google.FindDefaultCredentials(ctx, computeCloudScope)
	if err != nil {
		return nil, fmt.Errorf("finding application default credentials: %w", err)
	}
	return oauth2.NewClient(ctx, credentials.TokenSource), nil
}

func doJSON(ctx context.Context, client *http.Client, method, endpoint string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GCP API %s %s failed: status=%d body=%s", method, endpoint, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response body: %w", err)
		}
	}
	return nil
}

func buildInstance(np *mlv1alpha1.NodeProvision, startupScript string) map[string]any {
	cfg := np.Spec.GCPConfig
	rootDiskGB := cfg.RootDiskSizeGB
	if rootDiskGB <= 0 {
		rootDiskGB = defaultRootDiskSizeGB
	}

	instance := map[string]any{
		"name":        np.Name,
		"machineType": fmt.Sprintf("zones/%s/machineTypes/%s", zone(np.Spec), np.Spec.InstanceType),
		"disks": []map[string]any{
			{
				"boot":       true,
				"autoDelete": true,
				"type":       "PERSISTENT",
				"initializeParams": map[string]any{
					"sourceImage": sourceImage(cfg),
					"diskSizeGb":  rootDiskGB,
				},
			},
		},
		"networkInterfaces": []map[string]any{
			{
				"network": network(cfg),
				"accessConfigs": []map[string]any{
					{
						"name": "External NAT",
						"type": "ONE_TO_ONE_NAT",
					},
				},
			},
		},
		"metadata": map[string]any{
			"items": []map[string]string{
				{
					"key":   "startup-script",
					"value": startupScript,
				},
			},
		},
	}

	if cfg.Subnetwork != "" {
		instance["networkInterfaces"].([]map[string]any)[0]["subnetwork"] = cfg.Subnetwork
	}
	if len(cfg.Tags) > 0 {
		instance["tags"] = map[string]any{"items": cfg.Tags}
	}
	if cfg.ServiceAccountEmail != "" {
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{computeCloudScope}
		}
		instance["serviceAccounts"] = []map[string]any{
			{
				"email":  cfg.ServiceAccountEmail,
				"scopes": scopes,
			},
		}
	}
	if strings.EqualFold(string(np.Spec.MarketType), string(mlv1alpha1.InstanceMarketTypeSpot)) {
		action := strings.ToUpper(strings.TrimSpace(np.Spec.SpotTerminationAction))
		if action == "" {
			action = "STOP"
		}
		instance["scheduling"] = map[string]any{
			"provisioningModel":        "SPOT",
			"instanceTerminationAction": action,
			"automaticRestart":         false,
			"onHostMaintenance":        "TERMINATE",
		}
	}

	return instance
}

type instanceResponse struct {
	Status            string `json:"status"`
	NetworkInterfaces []struct {
		NetworkIP     string `json:"networkIP"`
		AccessConfigs []struct {
			NatIP string `json:"natIP"`
		} `json:"accessConfigs"`
	} `json:"networkInterfaces"`
}

func sourceImage(cfg *mlv1alpha1.GCPConfig) string {
	project := cfg.ImageProject
	if project == "" {
		project = defaultImageProject
	}
	if cfg.Image != "" {
		if strings.HasPrefix(cfg.Image, "projects/") || strings.HasPrefix(cfg.Image, "https://") {
			return cfg.Image
		}
		return fmt.Sprintf("projects/%s/global/images/%s", project, cfg.Image)
	}
	family := cfg.ImageFamily
	if family == "" {
		family = defaultImageFamily
	}
	return fmt.Sprintf("projects/%s/global/images/family/%s", project, family)
}

func network(cfg *mlv1alpha1.GCPConfig) string {
	if cfg.Network == "" {
		return defaultNetwork
	}
	return cfg.Network
}

func zone(spec mlv1alpha1.NodeProvisionSpec) string {
	if spec.GCPConfig != nil && spec.GCPConfig.Zone != "" {
		return spec.GCPConfig.Zone
	}
	return spec.Region
}

func parsePort(s string, defaultPort int) int {
	if s == "" {
		return defaultPort
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return defaultPort
	}
	return n
}
