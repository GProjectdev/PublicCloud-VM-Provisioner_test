package ml

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSyncKubeletServingCABundleCopiesExplicitCustomSource(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	source := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "serving-ca", Namespace: "kube-system"}, Data: map[string]string{"bundle.pem": string(newTestCAPEM(t))}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source).Build()
	cfg := KubeletServingCAConfig{SourceKind: "ConfigMap", SourceNamespace: "kube-system", SourceName: "serving-ca", SourceKey: "bundle.pem"}
	if err := syncKubeletServingCABundle(context.Background(), c, cfg); err != nil {
		t.Fatal(err)
	}
	got := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: defaultKubeletServingCATargetName, Namespace: defaultKubeletServingCATargetNamespace}, got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Data[defaultKubeletServingCAKey], "BEGIN CERTIFICATE") {
		t.Fatalf("target ConfigMap missing CA bundle: %#v", got.Data)
	}
	if strings.Contains(got.Data[defaultKubeletServingCAKey], "PRIVATE KEY") {
		t.Fatalf("target ConfigMap leaked private key: %q", got.Data[defaultKubeletServingCAKey])
	}
}

func TestSyncKubeletServingCABundleRejectsUnsafeSourcePEM(t *testing.T) {
	for _, tt := range []struct {
		name string
		pem  []byte
	}{
		{name: "private key block", pem: append(newTestCAPEM(t), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret")})...)},
		{name: "trailing junk", pem: append(newTestCAPEM(t), []byte("not pem junk")...)},
		{name: "non ca certificate", pem: newTestNonCAPEM(t)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			source := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "serving-ca", Namespace: "kube-system"}, Data: map[string]string{"ca.crt": string(tt.pem)}}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source).Build()
			err := syncKubeletServingCABundle(context.Background(), c, KubeletServingCAConfig{SourceNamespace: "kube-system", SourceName: "serving-ca"})
			if err == nil {
				t.Fatal("expected unsafe CA source to be rejected")
			}
			got := &corev1.ConfigMap{}
			if err := c.Get(context.Background(), types.NamespacedName{Name: defaultKubeletServingCATargetName, Namespace: defaultKubeletServingCATargetNamespace}, got); !apierrors.IsNotFound(err) {
				t.Fatalf("target ConfigMap should not be created, get err=%v data=%#v", err, got.Data)
			}
		})
	}
}

func TestStatefulMigrationReadinessFailsClosedWithoutGPUOrTLS(t *testing.T) {
	scheme := newReadinessTestScheme(t)
	np := newStatefulMigrationNP()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-worker"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(np, node, newStatefulMigrationNetConfig(), newKubeletServingCASource(t)).Build()
	r := &NodeProvisionReconciler{Client: c, KubeletServingCA: KubeletServingCAConfig{SourceNamespace: "kube-system", SourceName: "serving-ca"}}
	ok, reason, err := r.statefulMigrationReadiness(context.Background(), np, node)
	if err != nil {
		t.Fatal(err)
	}
	if ok || !strings.Contains(reason, "nvidia.com/gpu") {
		t.Fatalf("readiness did not fail closed on missing GPU: ok=%v reason=%q", ok, reason)
	}
}

func TestNodeHasGPUResourceRequiresCapacityAndAllocatable(t *testing.T) {
	one := resource.MustParse("1")
	zero := resource.MustParse("0")
	for _, tt := range []struct {
		name        string
		capacity    corev1.ResourceList
		allocatable corev1.ResourceList
		want        bool
	}{
		{name: "both positive", capacity: corev1.ResourceList{"nvidia.com/gpu": one}, allocatable: corev1.ResourceList{"nvidia.com/gpu": one}, want: true},
		{name: "capacity only", capacity: corev1.ResourceList{"nvidia.com/gpu": one}, allocatable: corev1.ResourceList{}, want: false},
		{name: "allocatable zero", capacity: corev1.ResourceList{"nvidia.com/gpu": one}, allocatable: corev1.ResourceList{"nvidia.com/gpu": zero}, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node := &corev1.Node{Status: corev1.NodeStatus{Capacity: tt.capacity, Allocatable: tt.allocatable}}
			if got := nodeHasGPUResource(node); got != tt.want {
				t.Fatalf("nodeHasGPUResource=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatefulMigrationReadinessUsesLiveTLSHandshakeWithoutCSR(t *testing.T) {
	scheme := newReadinessTestScheme(t)
	np := newStatefulMigrationNP()
	node := readyGPUNode()
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(np, node, newStatefulMigrationNetConfig(), newKubeletServingCASource(t)).Build()
	called := false
	r := &NodeProvisionReconciler{
		Client:           c,
		KubeletServingCA: KubeletServingCAConfig{SourceNamespace: "kube-system", SourceName: "serving-ca"},
		KubeletServingTLSChecker: kubeletServingTLSCheckerFunc(func(_ context.Context, hostIP, serverName string, roots *x509.CertPool) error {
			called = true
			if hostIP != "10.50.1.99" || serverName != "gpu-worker" || roots == nil {
				t.Fatalf("checker got host=%q server=%q roots nil=%v", hostIP, serverName, roots == nil)
			}
			return nil
		}),
	}
	ok, reason, err := r.statefulMigrationReadiness(context.Background(), np, node)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || reason != "" || !called {
		t.Fatalf("readiness=%v reason=%q checkerCalled=%v", ok, reason, called)
	}
}

func TestStatefulMigrationReadinessFailsWhenTLSHandshakeFails(t *testing.T) {
	scheme := newReadinessTestScheme(t)
	np := newStatefulMigrationNP()
	node := readyGPUNode()
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(np, node, newStatefulMigrationNetConfig(), newKubeletServingCASource(t)).Build()
	r := &NodeProvisionReconciler{
		Client:           c,
		KubeletServingCA: KubeletServingCAConfig{SourceNamespace: "kube-system", SourceName: "serving-ca"},
		KubeletServingTLSChecker: kubeletServingTLSCheckerFunc(func(context.Context, string, string, *x509.CertPool) error {
			return errors.New("handshake failed")
		}),
	}
	ok, reason, err := r.statefulMigrationReadiness(context.Background(), np, node)
	if err != nil {
		t.Fatal(err)
	}
	if ok || !strings.Contains(reason, "10250") {
		t.Fatalf("readiness should wait for TLS handshake: ok=%v reason=%q", ok, reason)
	}
}
func TestKubeletServingTLSCheckerRequiresIPSANForIPEndpoint(t *testing.T) {
	ca := newTestCA(t)
	for _, tt := range []struct {
		name    string
		cert    tls.Certificate
		wantErr bool
	}{
		{name: "dns only rejected", cert: newTestServingCert(t, ca, []string{"gpu-worker"}, nil), wantErr: true},
		{name: "ip and dns accepted", cert: newTestServingCert(t, ca, []string{"gpu-worker"}, []net.IP{net.ParseIP("127.0.0.1")}), wantErr: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			host, port := startTestKubeletTLSServer(t, tt.cert)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := newKubeletServingTLSChecker(port).Check(ctx, host, "gpu-worker", ca.pool)
			if tt.wantErr && err == nil {
				t.Fatal("expected DNS-only certificate to fail IP endpoint verification")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected IP SAN certificate to verify: %v", err)
			}
		})
	}
}

func newReadinessTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := mlv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func newStatefulMigrationNP() *mlv1alpha1.NodeProvision {
	return &mlv1alpha1.NodeProvision{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-worker", Namespace: "default"},
		Spec: mlv1alpha1.NodeProvisionSpec{
			Provider:     mlv1alpha1.CloudProviderAWS,
			HardwareType: "gpu",
			NodeLabel:    "gpu",
		},
		Status: mlv1alpha1.NodeProvisionStatus{NodeName: "gpu-worker", PrivateIP: "10.50.1.99", InstanceID: "i-0123456789abcdef0"},
	}
}

func newStatefulMigrationNetConfig() *mlv1alpha1.NodeProvisionNetConfig {
	return &mlv1alpha1.NodeProvisionNetConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-vpc-netconfig", Namespace: "default"},
		Spec:       mlv1alpha1.NodeProvisionNetConfigSpec{SoftwareConfig: mlv1alpha1.SoftwareConfig{NodeSoftware: &mlv1alpha1.NodeSoftwareConfig{RuntimeProfile: "StatefulMigration"}}},
	}
}

func readyGPUNode() *corev1.Node {
	gpu := resource.MustParse("1")
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-worker"},
		Status: corev1.NodeStatus{
			Capacity:    corev1.ResourceList{"nvidia.com/gpu": gpu},
			Allocatable: corev1.ResourceList{"nvidia.com/gpu": gpu},
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.50.1.99"},
			},
		},
	}
}

func newKubeletServingCASource(t *testing.T) *corev1.ConfigMap {
	t.Helper()
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "serving-ca", Namespace: "kube-system"}, Data: map[string]string{"ca.crt": string(newTestCAPEM(t))}}
}

func newTestCAPEM(t *testing.T) []byte {
	t.Helper()
	return newTestCertPEM(t, true)
}

func newTestNonCAPEM(t *testing.T) []byte {
	t.Helper()
	return newTestCertPEM(t, false)
}

func newTestCertPEM(t *testing.T, isCA bool) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-kubelet-serving-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "test-kubelet-serving-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return testCA{cert: cert, key: key, pool: pool}
}

func newTestServingCert(t *testing.T, ca testCA, dnsNames []string, ips []net.IP) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "system:node:gpu-worker"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func startTestKubeletTLSServer(t *testing.T, cert tls.Certificate) (string, string) {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if tlsConn, ok := conn.(*tls.Conn); ok {
			_ = tlsConn.Handshake()
		}
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}
