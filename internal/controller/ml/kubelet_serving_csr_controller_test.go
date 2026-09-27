package ml

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"net/url"
	"testing"

	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestKubeletServingCSRUsageCombinations(t *testing.T) {
	for _, tt := range []struct {
		name   string
		usages []certificatesv1.KeyUsage
		want   bool
	}{
		{"two usages", []certificatesv1.KeyUsage{"digital signature", "server auth"}, true},
		{"three usages", []certificatesv1.KeyUsage{"digital signature", "key encipherment", "server auth"}, true},
		{"reordered", []certificatesv1.KeyUsage{"server auth", "digital signature"}, true},
		{"empty", nil, false},
		{"missing signature", []certificatesv1.KeyUsage{"key encipherment", "server auth"}, false},
		{"missing server auth", []certificatesv1.KeyUsage{"digital signature", "key encipherment"}, false},
		{"client auth", []certificatesv1.KeyUsage{"digital signature", "server auth", "client auth"}, false},
		{"unknown", []certificatesv1.KeyUsage{"digital signature", "server auth", "unknown"}, false},
		{"duplicate", []certificatesv1.KeyUsage{"digital signature", "server auth", "server auth"}, false},
		{"extra usage", []certificatesv1.KeyUsage{"digital signature", "key encipherment", "server auth", "client auth"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			np := trustedCSRTestNodeProvision()
			node := trustedCSRTestNode("worker-a", "", "10.50.1.99")
			csr := newServingCSR(t, "csr-usage", "worker-a", []string{"worker-a"}, []net.IP{net.ParseIP("10.50.1.99")})
			csr.Spec.Usages = tt.usages
			scheme := newCSRTestScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(np, node).Build()
			r := &KubeletServingCSRApprover{Client: c, Scheme: scheme}
			ok, reason, err := r.verifyKubeletServingCSR(context.Background(), csr)
			if err != nil || ok != tt.want {
				t.Fatalf("ok=%v want=%v reason=%s err=%v", ok, tt.want, reason, err)
			}
			if tt.want {
				np.Status.InstanceID = ""
				if err := c.Update(context.Background(), np); err != nil {
					t.Fatal(err)
				}
				ok, _, err = r.verifyKubeletServingCSR(context.Background(), csr)
				if err != nil || ok {
					t.Fatalf("missing EC2 identity: ok=%v err=%v", ok, err)
				}
			}
		})
	}
}

func TestKubeletServingCSRApproverRequiresTrustedEC2Mapping(t *testing.T) {
	for _, tt := range []struct {
		name       string
		providerID string
		instanceID string
		privateIP  string
		nodeIP     string
		wantOK     bool
	}{
		{name: "trusted mapping without provider id", providerID: "", instanceID: "i-0123456789abcdef0", privateIP: "10.50.1.99", nodeIP: "10.50.1.99", wantOK: true},
		{name: "provider id mismatch ignored", providerID: "aws:///i-deadbeef", instanceID: "i-0123456789abcdef0", privateIP: "10.50.1.99", nodeIP: "10.50.1.99", wantOK: true},
		{name: "missing controller observed instance id", providerID: "aws:///i-0123456789abcdef0", instanceID: "", privateIP: "10.50.1.99", nodeIP: "10.50.1.99", wantOK: false},
		{name: "node lacks trusted provisioned ip", providerID: "aws:///i-0123456789abcdef0", instanceID: "i-0123456789abcdef0", privateIP: "10.50.1.99", nodeIP: "10.50.1.100", wantOK: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newCSRTestScheme(t)
			np := &mlv1alpha1.NodeProvision{
				ObjectMeta: metav1.ObjectMeta{Name: "worker-a", Namespace: "default", UID: types.UID("np-uid")},
				Spec:       mlv1alpha1.NodeProvisionSpec{Provider: mlv1alpha1.CloudProviderAWS, NetworkMode: "VPC"},
				Status: mlv1alpha1.NodeProvisionStatus{
					Phase:      mlv1alpha1.NodeProvisionPhaseReady,
					NodeName:   "worker-a",
					InstanceID: tt.instanceID,
					PrivateIP:  tt.privateIP,
					IPAddress:  tt.privateIP,
				},
			}
			node := trustedCSRTestNode("worker-a", tt.providerID, tt.nodeIP)
			csr := newServingCSR(t, "csr-a", "worker-a", []string{"worker-a"}, []net.IP{net.ParseIP(tt.privateIP)})
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(np).WithObjects(np, node, csr).Build()
			r := &KubeletServingCSRApprover{Client: c, Scheme: scheme}
			got, reason, err := r.verifyKubeletServingCSR(context.Background(), csr)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.wantOK {
				t.Fatalf("ok=%v, want %v, reason=%s", got, tt.wantOK, reason)
			}
		})
	}
}

func TestKubeletServingCSRApproverRejectsUntrustedSANAndSubject(t *testing.T) {
	spiffeURI, err := url.Parse("spiffe://example.test/worker-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		mutate func(*x509.CertificateRequest)
	}{
		{name: "dns only without trusted ip", mutate: func(req *x509.CertificateRequest) { req.IPAddresses = nil }},
		{name: "extra untrusted ip", mutate: func(req *x509.CertificateRequest) {
			req.IPAddresses = append(req.IPAddresses, net.ParseIP("10.50.1.100"))
		}},
		{name: "extra untrusted dns", mutate: func(req *x509.CertificateRequest) { req.DNSNames = append(req.DNSNames, "ip-10-50-1-99.ec2.internal") }},
		{name: "email san", mutate: func(req *x509.CertificateRequest) { req.EmailAddresses = []string{"node@example.test"} }},
		{name: "uri san", mutate: func(req *x509.CertificateRequest) { req.URIs = []*url.URL{spiffeURI} }},
		{name: "extra organization", mutate: func(req *x509.CertificateRequest) { req.Subject.Organization = []string{"system:nodes", "extra"} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newCSRTestScheme(t)
			np := trustedCSRTestNodeProvision()
			node := trustedCSRTestNode("worker-a", "", "10.50.1.99")
			csr := newServingCSRWithMutator(t, "csr-bad", "worker-a", tt.mutate)
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(np).WithObjects(np, node, csr).Build()
			r := &KubeletServingCSRApprover{Client: c, Scheme: scheme}
			ok, reason, err := r.verifyKubeletServingCSR(context.Background(), csr)
			if err != nil {
				t.Fatal(err)
			}
			if ok || reason == "" {
				t.Fatalf("bad CSR approved or missing reason: ok=%v reason=%q", ok, reason)
			}
		})
	}
}

func TestKubeletServingCSRApproverRejectsRetiringLifecycle(t *testing.T) {
	now := metav1.Now()
	for _, tt := range []struct {
		name   string
		mutate func(*mlv1alpha1.NodeProvision, *corev1.Node)
		wantOK bool
	}{
		{name: "pre ready allowed", mutate: func(np *mlv1alpha1.NodeProvision, _ *corev1.Node) {
			np.Status.Phase = mlv1alpha1.NodeProvisionPhaseProvisioning
		}, wantOK: true},
		{name: "deleting node rejected", mutate: func(_ *mlv1alpha1.NodeProvision, node *corev1.Node) {
			node.DeletionTimestamp = &now
			node.Finalizers = []string{"test/finalizer"}
		}},
		{name: "deleting nodeprovision timestamp rejected", mutate: func(np *mlv1alpha1.NodeProvision, _ *corev1.Node) {
			np.DeletionTimestamp = &now
			np.Finalizers = []string{"test/finalizer"}
		}},
		{name: "deleting nodeprovision phase rejected", mutate: func(np *mlv1alpha1.NodeProvision, _ *corev1.Node) {
			np.Status.Phase = mlv1alpha1.NodeProvisionPhaseDeleting
		}},
		{name: "failed nodeprovision phase rejected", mutate: func(np *mlv1alpha1.NodeProvision, _ *corev1.Node) {
			np.Status.Phase = mlv1alpha1.NodeProvisionPhaseFailed
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newCSRTestScheme(t)
			np := trustedCSRTestNodeProvision()
			node := trustedCSRTestNode("worker-a", "", "10.50.1.99")
			tt.mutate(np, node)
			csr := newServingCSR(t, "csr-a", "worker-a", []string{"worker-a"}, []net.IP{net.ParseIP("10.50.1.99")})
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(np).WithObjects(np, node, csr).Build()
			r := &KubeletServingCSRApprover{Client: c, Scheme: scheme}
			ok, reason, err := r.verifyKubeletServingCSR(context.Background(), csr)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tt.wantOK {
				t.Fatalf("ok=%v, want %v, reason=%q", ok, tt.wantOK, reason)
			}
		})
	}
}
func TestKubeletServingCSRApproverDisabledLeavesCSRPending(t *testing.T) {
	scheme := newCSRTestScheme(t)
	np := trustedCSRTestNodeProvision()
	node := trustedCSRTestNode("worker-a", "", "10.50.1.99")
	csr := newServingCSR(t, "csr-a", "worker-a", []string{"worker-a"}, []net.IP{net.ParseIP("10.50.1.99")})
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(np).WithObjects(np, node, csr).Build()
	r := &KubeletServingCSRApprover{Client: c, Scheme: scheme, EnableKubeletServingCSRApproval: false}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: csr.Name}}); err != nil {
		t.Fatal(err)
	}
	got := &certificatesv1.CertificateSigningRequest{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: csr.Name}, got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Conditions) != 0 {
		t.Fatalf("disabled approver mutated CSR conditions: %#v", got.Status.Conditions)
	}
}

func newCSRTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := certificatesv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := mlv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func trustedCSRTestNodeProvision() *mlv1alpha1.NodeProvision {
	return &mlv1alpha1.NodeProvision{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-a", Namespace: "default", UID: types.UID("np-uid")},
		Spec:       mlv1alpha1.NodeProvisionSpec{Provider: mlv1alpha1.CloudProviderAWS},
		Status:     mlv1alpha1.NodeProvisionStatus{NodeName: "worker-a", InstanceID: "i-0123456789abcdef0", PrivateIP: "10.50.1.99"},
	}
}

func trustedCSRTestNode(name, providerID, nodeIP string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				nodeProvisionProviderLabel: string(mlv1alpha1.CloudProviderAWS),
				nodeProvisionNameLabel:     "worker-a",
				nodeProvisionNsLabel:       "default",
				nodeProvisionUIDLabel:      "np-uid",
			},
		},
		Spec: corev1.NodeSpec{ProviderID: providerID},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeHostName, Address: name},
			{Type: corev1.NodeInternalIP, Address: nodeIP},
		}},
	}
}

func newServingCSR(t *testing.T, name, nodeName string, dnsNames []string, ips []net.IP) *certificatesv1.CertificateSigningRequest {
	t.Helper()
	return newServingCSRWithMutator(t, name, nodeName, func(req *x509.CertificateRequest) {
		req.DNSNames = dnsNames
		req.IPAddresses = ips
	})
}

func newServingCSRWithMutator(t *testing.T, name, nodeName string, mutate func(*x509.CertificateRequest)) *certificatesv1.CertificateSigningRequest {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	req := &x509.CertificateRequest{
		Subject:     pkix.Name{CommonName: "system:node:" + nodeName, Organization: []string{"system:nodes"}},
		DNSNames:    []string{nodeName},
		IPAddresses: []net.IP{net.ParseIP("10.50.1.99")},
	}
	mutate(req)
	der, err := x509.CreateCertificateRequest(rand.Reader, req, key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	return &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    pemBytes,
			Username:   "system:node:" + nodeName,
			Groups:     []string{"system:nodes", "system:authenticated"},
			SignerName: certificatesv1.KubeletServingSignerName,
			Usages: []certificatesv1.KeyUsage{
				certificatesv1.UsageDigitalSignature,
				certificatesv1.UsageKeyEncipherment,
				certificatesv1.UsageServerAuth,
			},
		},
	}
}
