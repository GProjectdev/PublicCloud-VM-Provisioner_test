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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type NodeProvisionPhase string
type CloudProvider string
type InstanceMarketType string

const (
	NodeProvisionPhasePending            NodeProvisionPhase = "Pending"
	NodeProvisionPhaseValidating         NodeProvisionPhase = "Validating"
	NodeProvisionPhaseCreatingInstance   NodeProvisionPhase = "CreatingInstance"
	NodeProvisionPhaseWaitingForInstance NodeProvisionPhase = "WaitingForInstance"
	NodeProvisionPhaseConfiguringVPN     NodeProvisionPhase = "ConfiguringVPN"
	NodeProvisionPhaseProvisioning       NodeProvisionPhase = "Provisioning"
	NodeProvisionPhaseBootstrapping      NodeProvisionPhase = "Bootstrapping"
	NodeProvisionPhaseRegisteringNode    NodeProvisionPhase = "RegisteringNode"
	NodeProvisionPhaseJoining            NodeProvisionPhase = "Joining"
	NodeProvisionPhaseVerifyingHealth    NodeProvisionPhase = "VerifyingHealth"
	NodeProvisionPhaseReady              NodeProvisionPhase = "Ready"
	NodeProvisionPhaseFailed             NodeProvisionPhase = "Failed"
	NodeProvisionPhaseDeleting           NodeProvisionPhase = "Deleting"
	NodeProvisionPhasePrePullingImages   NodeProvisionPhase = "PrePullingImages"

	CloudProviderAWS    CloudProvider = "AWS"
	CloudProviderGCP    CloudProvider = "GCP"
	CloudProviderAzure  CloudProvider = "Azure"
	CloudProviderOnPrem CloudProvider = "OnPrem"

	InstanceMarketTypeOnDemand InstanceMarketType = "OnDemand"
	InstanceMarketTypeSpot     InstanceMarketType = "Spot"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// NodeProvisionSpec defines the desired state of NodeProvision
// CredentialsRef references the Secret containing provider credentials.
type CredentialsRef struct {
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	// Key is the data key within the secret that holds the credential.
	// When omitted the controller tries well-known names: privateKey, id_rsa,
	// ssh-privatekey, password, key.
	Key string `json:"key,omitempty"`
}

// AWSConfig holds AWS-specific parameters for EC2 node provisioning.
type AWSConfig struct {
	// AvailabilityZone constrains instance and subnet selection to one AWS AZ.
	// For example, "ap-northeast-2c".
	// +optional
	AvailabilityZone string `json:"availabilityZone,omitempty"`
	// VPC ID where the instance will be launched.
	VPCID string `json:"vpcId,omitempty"`
	// Subnet ID for the instance's primary network interface.
	SubnetID string `json:"subnetId,omitempty"`
	// Security group IDs to attach to the instance.
	// +optional
	SecurityGroupIDs []string `json:"securityGroupIds,omitempty"`
	// AMI ID to use for the instance.
	AMI string `json:"ami,omitempty"`
	// EC2 key pair name for SSH access (optional when using cloud-init only).
	// +optional
	KeyPairName string `json:"keyPairName,omitempty"`
	// IAM instance profile name or ARN for the instance.
	// +optional
	IAMInstanceProfile string `json:"iamInstanceProfile,omitempty"`
	// Additional tags to apply to created AWS resources.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`
	// RootVolumeSizeGB overrides the root EBS volume size in GB.
	// Defaults to 50 GB when unset. The Ubuntu 22.04 AMI default (8 GB) is
	// too small for a Kubernetes node running CRI-O and container images.
	// +optional
	RootVolumeSizeGB int32 `json:"rootVolumeSizeGB,omitempty"`
}

// GCPConfig holds GCP-specific parameters for Compute Engine node provisioning.
type GCPConfig struct {
	// ProjectID is the GCP project that owns the VM.
	ProjectID string `json:"projectId,omitempty"`
	// Zone is the Compute Engine zone, for example "us-central1-a".
	Zone string `json:"zone,omitempty"`
	// Network is the VPC network self-link or short/global network path.
	// Defaults to "global/networks/default".
	// +optional
	Network string `json:"network,omitempty"`
	// Subnetwork is an optional subnetwork self-link or regional path.
	// +optional
	Subnetwork string `json:"subnetwork,omitempty"`
	// ImageProject contains the source image project. Defaults to "ubuntu-os-cloud".
	// +optional
	ImageProject string `json:"imageProject,omitempty"`
	// ImageFamily contains the source image family. Defaults to "ubuntu-2204-lts".
	// +optional
	ImageFamily string `json:"imageFamily,omitempty"`
	// Image overrides ImageFamily. Accepts a full self-link/project path or an
	// image name within ImageProject.
	// +optional
	Image string `json:"image,omitempty"`
	// ServiceAccountEmail attaches a service account to the VM when set.
	// +optional
	ServiceAccountEmail string `json:"serviceAccountEmail,omitempty"`
	// Scopes are OAuth scopes for the attached service account.
	// +optional
	Scopes []string `json:"scopes,omitempty"`
	// Tags are network tags applied to the VM.
	// +optional
	Tags []string `json:"tags,omitempty"`
	// RootDiskSizeGB overrides the boot persistent disk size in GB.
	// Defaults to 50 GB when unset.
	// +optional
	RootDiskSizeGB int64 `json:"rootDiskSizeGB,omitempty"`
}

// NodeProvisionSpec defines the desired state of NodeProvision.
type NodeProvisionSpec struct {
	Provider CloudProvider `json:"provider,omitempty"`

	// HardwareType classifies the node for image pre-pull targeting.
	// "gpu" — node has GPUs; pulls images with nodeTarget "gpu" and "all".
	// "cpu" (or empty) — pulls only images with nodeTarget "all".
	// +optional
	HardwareType string `json:"hardwareType,omitempty"`

	Role string `json:"role,omitempty"`

	NodeLabel string `json:"nodeLabel,omitempty"`

	Region string `json:"region,omitempty"`

	InstanceType string `json:"instanceType,omitempty"`

	// MarketType selects regular on-demand capacity or provider spot/preemptible
	// capacity. Empty defaults to OnDemand.
	// +optional
	MarketType InstanceMarketType `json:"marketType,omitempty"`

	// SpotTerminationAction controls provider behavior when Spot capacity is
	// reclaimed. GCP accepts STOP or DELETE; AWS accepts Stop or Terminate.
	// Empty defaults to provider-safe behavior.
	// +optional
	SpotTerminationAction string `json:"spotTerminationAction,omitempty"`

	InstanceID string `json:"instanceId,omitempty"`

	Hostname string `json:"hostname,omitempty"`

	IPAddress string `json:"ipAddress,omitempty"`
	SSHPort   int    `json:"sshPort,omitempty"`

	SSHUsernameOverride string `json:"sshUsernameOverride,omitempty"`

	CredentialsRef CredentialsRef `json:"credentialsRef,omitempty"`

	// AWSConfig holds provider-specific parameters for AWS EC2 provisioning.
	// +optional
	AWSConfig *AWSConfig `json:"awsConfig,omitempty"`

	// GCPConfig holds provider-specific parameters for GCP Compute Engine provisioning.
	// +optional
	GCPConfig *GCPConfig `json:"gcpConfig,omitempty"`
}

// NodeProvisionStatus defines the observed state of NodeProvision.
type NodeProvisionStatus struct {
	// Current lifecycle phase.
	Phase NodeProvisionPhase `json:"phase,omitempty"`

	// Human-readable status message.
	Message string `json:"message,omitempty"`

	// Timestamp when provisioning started.
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// Timestamp when provisioning completed.
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Provider-generated instance ID (e.g. i-xxxxxxxxxxxxxxxxx for AWS).
	InstanceID string `json:"instanceId,omitempty"`

	// Assigned hostname.
	Hostname string `json:"hostname,omitempty"`

	// IPAddress is the VPN (WireGuard) IP assigned to the node.
	IPAddress string `json:"ipAddress,omitempty"`

	// PublicIP is the cloud-provider public IP of the instance.
	// +optional
	PublicIP string `json:"publicIp,omitempty"`

	// PrivateIP is the cloud-provider private IP of the instance.
	// +optional
	PrivateIP string `json:"privateIp,omitempty"`

	// VpnIP is the WireGuard VPN IP allocated for this node.
	// +optional
	VpnIP string `json:"vpnIp,omitempty"`

	// Progress is a 0–100 percentage of provisioning completion.
	// +optional
	Progress int `json:"progress,omitempty"`

	// LastUpdated is the timestamp of the most recent status update.
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`

	// Kubernetes node name after join.
	NodeName string `json:"nodeName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NodeProvision is the Schema for the nodeprovisions API
type NodeProvision struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of NodeProvision
	// +required
	Spec NodeProvisionSpec `json:"spec"`

	// status defines the observed state of NodeProvision
	// +optional
	Status NodeProvisionStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NodeProvisionList contains a list of NodeProvision
type NodeProvisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NodeProvision `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeProvision{}, &NodeProvisionList{})
}
