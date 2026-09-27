package ml

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"

	"reflect"
	"strings"
	"time"

	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	kubeletServingCSRApprovalReason = "NodeProvisionEC2IdentityVerified"
	kubeletServingCSRRequeue        = 30 * time.Second
)

type trustedNodeProvisionIdentity struct {
	NodeName   string
	InstanceID string
	IPs        map[string]bool
	DNSNames   map[string]bool
}

// KubeletServingCSRApprover approves kubelet serving CSRs only after the
// requesting Node is bound to an AWS NodeProvision by immutable CR UID and
// controller-observed EC2 instance/IP state. Node labels and node names are
// pointers only; they are never sufficient on their own.
type KubeletServingCSRApprover struct {
	client.Client
	Scheme                          *runtime.Scheme
	KubeletServingCA                KubeletServingCAConfig
	EnableKubeletServingCSRApproval bool
}

// +kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=get;list;watch
// +kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/approval,verbs=update
// +kubebuilder:rbac:groups=certificates.k8s.io,resources=signers,resourceNames=kubernetes.io/kubelet-serving,verbs=approve
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=ml.dcn.ssu.ac.kr,resources=nodeprovisions,verbs=get;list;watch

func (r *KubeletServingCSRApprover) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if !r.EnableKubeletServingCSRApproval {
		return ctrl.Result{}, nil
	}

	csr := &certificatesv1.CertificateSigningRequest{}
	if err := r.Get(ctx, req.NamespacedName, csr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if csrApprovedOrDenied(csr) || csr.Spec.SignerName != certificatesv1.KubeletServingSignerName {
		return ctrl.Result{}, nil
	}

	ok, reason, err := r.verifyKubeletServingCSR(ctx, csr)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		log.Info("Leaving kubelet serving CSR pending until NodeProvision EC2 identity can be verified", "csr", csr.Name, "reason", reason)
		return ctrl.Result{RequeueAfter: kubeletServingCSRRequeue}, nil
	}

	now := metav1.Now()
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:           certificatesv1.CertificateApproved,
		Status:         corev1.ConditionTrue,
		Reason:         kubeletServingCSRApprovalReason,
		Message:        "kubelet serving CSR matched an AWS NodeProvision owner UID, controller-observed EC2 instance ID/IP, and restricted serving SANs",
		LastUpdateTime: now,
	})
	if err := r.SubResource("approval").Update(ctx, csr); err != nil {
		return ctrl.Result{}, fmt.Errorf("approving kubelet serving CSR %s: %w", csr.Name, err)
	}
	log.Info("Approved kubelet serving CSR", "csr", csr.Name, "node", strings.TrimPrefix(csr.Spec.Username, "system:node:"))
	return ctrl.Result{}, nil
}

func (r *KubeletServingCSRApprover) verifyKubeletServingCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest) (bool, string, error) {
	if !hasExactServingUsages(csr.Spec.Usages) {
		return false, "CSR usages are not exactly kubelet serving usages", nil
	}
	nodeName, req, reason := parseKubeletServingRequest(csr)
	if reason != "" {
		return false, reason, nil
	}

	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "Node does not exist", nil
		}
		return false, "", fmt.Errorf("getting Node %s: %w", nodeName, err)
	}
	trusted, ok, reason, err := r.trustedAWSNodeProvisionIdentity(ctx, node)
	if !ok || err != nil {
		return ok, reason, err
	}
	if ok, reason := csrSANsMatchTrustedIdentity(req, trusted); !ok {
		return false, reason, nil
	}
	return true, "", nil
}

func parseKubeletServingRequest(csr *certificatesv1.CertificateSigningRequest) (string, *x509.CertificateRequest, string) {
	if !strings.HasPrefix(csr.Spec.Username, "system:node:") {
		return "", nil, "CSR username is not a node identity"
	}
	nodeName := strings.TrimPrefix(csr.Spec.Username, "system:node:")
	if nodeName == "" || !containsString(csr.Spec.Groups, "system:nodes") {
		return "", nil, "CSR requester is not in system:nodes"
	}
	block, _ := pem.Decode(csr.Spec.Request)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return "", nil, "CSR request is not PEM encoded"
	}
	req, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", nil, "CSR request cannot be parsed"
	}
	if err := req.CheckSignature(); err != nil {
		return "", nil, "CSR signature is invalid"
	}
	if req.Subject.CommonName != csr.Spec.Username || !reflect.DeepEqual(req.Subject.Organization, []string{"system:nodes"}) {
		return "", nil, "CSR subject does not exactly match node identity"
	}
	if len(req.EmailAddresses) > 0 || len(req.URIs) > 0 {
		return "", nil, "CSR email and URI SANs are not allowed"
	}
	return nodeName, req, ""
}

func (r *KubeletServingCSRApprover) trustedAWSNodeProvisionIdentity(ctx context.Context, node *corev1.Node) (trustedNodeProvisionIdentity, bool, string, error) {
	if !node.DeletionTimestamp.IsZero() {
		return trustedNodeProvisionIdentity{}, false, "Node is deleting", nil
	}
	labels := node.GetLabels()
	if labels[nodeProvisionProviderLabel] != string(mlv1alpha1.CloudProviderAWS) {
		return trustedNodeProvisionIdentity{}, false, "Node is not labeled as an AWS NodeProvision node", nil
	}
	npName := labels[nodeProvisionNameLabel]
	npNamespace := labels[nodeProvisionNsLabel]
	npUID := labels[nodeProvisionUIDLabel]
	if npName == "" || npNamespace == "" || npUID == "" {
		return trustedNodeProvisionIdentity{}, false, "Node is missing NodeProvision owner labels", nil
	}
	np := &mlv1alpha1.NodeProvision{}
	if err := r.Get(ctx, types.NamespacedName{Name: npName, Namespace: npNamespace}, np); err != nil {
		if apierrors.IsNotFound(err) {
			return trustedNodeProvisionIdentity{}, false, "NodeProvision owner does not exist", nil
		}
		return trustedNodeProvisionIdentity{}, false, "", fmt.Errorf("getting NodeProvision %s/%s: %w", npNamespace, npName, err)
	}
	if !np.DeletionTimestamp.IsZero() {
		return trustedNodeProvisionIdentity{}, false, "NodeProvision owner is deleting", nil
	}
	if np.Status.Phase == mlv1alpha1.NodeProvisionPhaseDeleting || np.Status.Phase == mlv1alpha1.NodeProvisionPhaseFailed {
		return trustedNodeProvisionIdentity{}, false, "NodeProvision owner is not in an approvable lifecycle phase", nil
	}
	if string(np.UID) != npUID {
		return trustedNodeProvisionIdentity{}, false, "NodeProvision UID label does not match the live owner", nil
	}
	if np.Spec.Provider != mlv1alpha1.CloudProviderAWS {
		return trustedNodeProvisionIdentity{}, false, "NodeProvision owner is not AWS", nil
	}
	if np.Status.NodeName == "" || np.Status.NodeName != node.Name {
		return trustedNodeProvisionIdentity{}, false, "NodeProvision status does not reference this Node", nil
	}
	if np.Status.InstanceID == "" {
		return trustedNodeProvisionIdentity{}, false, "NodeProvision status has no controller-observed EC2 instance ID", nil
	}
	trustedIPs := trustedProvisionedIPs(np)
	if len(trustedIPs) == 0 {
		return trustedNodeProvisionIdentity{}, false, "NodeProvision status has no trusted provisioned IP", nil
	}
	if !nodeHasTrustedProvisionedAddress(node, trustedIPs) {
		return trustedNodeProvisionIdentity{}, false, "Node addresses do not include a trusted NodeProvision IP", nil
	}
	return trustedNodeProvisionIdentity{
		NodeName:   node.Name,
		InstanceID: np.Status.InstanceID,
		IPs:        trustedIPs,
		DNSNames: map[string]bool{
			node.Name: true,
		},
	}, true, "", nil
}

func csrSANsMatchTrustedIdentity(req *x509.CertificateRequest, trusted trustedNodeProvisionIdentity) (bool, string) {
	if len(req.DNSNames) == 0 && len(req.IPAddresses) == 0 {
		return false, "CSR has no serving DNS or IP SANs"
	}
	for _, dns := range req.DNSNames {
		if !trusted.DNSNames[dns] {
			return false, "CSR DNS SAN is not a trusted node DNS name"
		}
	}
	matchedTrustedIP := false
	for _, ip := range req.IPAddresses {
		if !trusted.IPs[ip.String()] {
			return false, "CSR IP SAN is not a trusted provisioned NodeProvision IP"
		}
		matchedTrustedIP = true
	}
	if !matchedTrustedIP {
		return false, "CSR must include at least one trusted provisioned NodeProvision IP SAN"
	}
	return true, ""
}

func trustedProvisionedIPs(np *mlv1alpha1.NodeProvision) map[string]bool {
	trusted := map[string]bool{}
	for _, ip := range []string{np.Status.PrivateIP, np.Status.IPAddress, np.Status.VpnIP} {
		if parsed := net.ParseIP(ip); parsed != nil {
			trusted[parsed.String()] = true
		}
	}
	return trusted
}

func nodeHasTrustedProvisionedAddress(node *corev1.Node, trusted map[string]bool) bool {
	for _, addr := range node.Status.Addresses {
		if addr.Type != corev1.NodeInternalIP && addr.Type != corev1.NodeExternalIP {
			continue
		}
		if parsed := net.ParseIP(addr.Address); parsed != nil && trusted[parsed.String()] {
			return true
		}
	}
	return false
}

func hasExactServingUsages(usages []certificatesv1.KeyUsage) bool {
	if len(usages) != 3 {
		return false
	}
	seen := map[certificatesv1.KeyUsage]bool{}
	for _, usage := range usages {
		seen[usage] = true
	}
	return seen[certificatesv1.UsageDigitalSignature] && seen[certificatesv1.UsageKeyEncipherment] && seen[certificatesv1.UsageServerAuth]
}

func csrApprovedOrDenied(csr *certificatesv1.CertificateSigningRequest) bool {
	for _, condition := range csr.Status.Conditions {
		if condition.Type == certificatesv1.CertificateApproved || condition.Type == certificatesv1.CertificateDenied {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the kubelet serving CSR approver with the Manager.
func (r *KubeletServingCSRApprover) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&certificatesv1.CertificateSigningRequest{}).
		Named("kubelet-serving-csr-approver").
		Complete(r)
}
