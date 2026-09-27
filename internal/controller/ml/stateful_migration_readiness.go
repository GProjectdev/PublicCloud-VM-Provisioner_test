package ml

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"strings"
	"time"

	mlv1alpha1 "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	defaultKubeletServingCATargetNamespace = "stateful-migration-system"
	defaultKubeletServingCATargetName      = "kubelet-serving-ca"
	defaultKubeletServingCAKey             = "ca.crt"
	defaultKubeletServingCASyncPeriod      = 5 * time.Minute
	kubeletServingTLSPort                  = "10250"
	kubeletServingTLSHandshakeTimeout      = 5 * time.Second
)

type KubeletServingCAConfig struct {
	SourceKind      string
	SourceNamespace string
	SourceName      string
	SourceKey       string
	TargetNamespace string
	TargetName      string
	TargetKey       string
}

type KubeletServingCABundleSyncer struct {
	client.Client
	Config   KubeletServingCAConfig
	Interval time.Duration
}

type KubeletServingTLSChecker interface {
	Check(ctx context.Context, hostIP, serverName string, roots *x509.CertPool) error
}

type kubeletServingTLSCheckerFunc func(ctx context.Context, hostIP, serverName string, roots *x509.CertPool) error

func (f kubeletServingTLSCheckerFunc) Check(ctx context.Context, hostIP, serverName string, roots *x509.CertPool) error {
	return f(ctx, hostIP, serverName, roots)
}

func defaultKubeletServingTLSChecker() KubeletServingTLSChecker {
	return newKubeletServingTLSChecker(kubeletServingTLSPort)
}

func newKubeletServingTLSChecker(port string) KubeletServingTLSChecker {
	return kubeletServingTLSCheckerFunc(func(ctx context.Context, hostIP, _ string, roots *x509.CertPool) error {
		if net.ParseIP(hostIP) == nil {
			return fmt.Errorf("invalid kubelet IP %q", hostIP)
		}
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, kubeletServingTLSHandshakeTimeout)
			defer cancel()
		}
		dialer := &net.Dialer{Timeout: kubeletServingTLSHandshakeTimeout}
		rawConn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(hostIP, port))
		if err != nil {
			return err
		}
		defer rawConn.Close()
		conn := tls.Client(rawConn, &tls.Config{RootCAs: roots, ServerName: hostIP, MinVersion: tls.VersionTLS12})
		if err := conn.HandshakeContext(ctx); err != nil {
			return err
		}
		return conn.Close()
	})
}

func (c KubeletServingCAConfig) withDefaults() KubeletServingCAConfig {
	if c.SourceKind == "" {
		c.SourceKind = "ConfigMap"
	}
	if c.SourceKey == "" {
		c.SourceKey = defaultKubeletServingCAKey
	}
	if c.TargetNamespace == "" {
		c.TargetNamespace = defaultKubeletServingCATargetNamespace
	}
	if c.TargetName == "" {
		c.TargetName = defaultKubeletServingCATargetName
	}
	if c.TargetKey == "" {
		c.TargetKey = defaultKubeletServingCAKey
	}
	return c
}

func (c KubeletServingCAConfig) sourceConfigured() bool {
	c = c.withDefaults()
	return c.SourceNamespace != "" && c.SourceName != "" && c.SourceKey != ""
}

func (s *KubeletServingCABundleSyncer) Start(ctx context.Context) error {
	cfg := s.Config.withDefaults()
	if !cfg.sourceConfigured() {
		<-ctx.Done()
		return nil
	}
	interval := s.Interval
	if interval <= 0 {
		interval = defaultKubeletServingCASyncPeriod
	}
	log := logf.FromContext(ctx).WithName("kubelet-serving-ca-syncer")
	if err := syncKubeletServingCABundle(ctx, s.Client, cfg); err != nil {
		log.Error(err, "Failed to sync kubelet serving CA bundle")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := syncKubeletServingCABundle(ctx, s.Client, cfg); err != nil {
				log.Error(err, "Failed to sync kubelet serving CA bundle")
			}
		}
	}
}

func (r *KubeletServingCSRApprover) syncKubeletServingCABundle(ctx context.Context) error {
	return syncKubeletServingCABundle(ctx, r.Client, r.KubeletServingCA)
}

func syncKubeletServingCABundle(ctx context.Context, c client.Client, cfg KubeletServingCAConfig) error {
	cfg = cfg.withDefaults()
	if !cfg.sourceConfigured() {
		return nil
	}
	caPEM, err := readKubeletServingCA(ctx, c, cfg)
	if err != nil {
		return err
	}
	_, publicCAPEM, err := kubeletServingCAPoolAndPublicPEM(caPEM)
	if err != nil {
		return fmt.Errorf("validating kubelet serving CA bundle: %w", err)
	}
	target := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: cfg.TargetName, Namespace: cfg.TargetNamespace}
	if err := c.Get(ctx, key, target); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("getting kubelet serving CA target ConfigMap %s/%s: %w", cfg.TargetNamespace, cfg.TargetName, err)
		}
		target = &corev1.ConfigMap{ObjectMeta: objectMeta(cfg.TargetName, cfg.TargetNamespace), Data: map[string]string{cfg.TargetKey: string(publicCAPEM)}}
		return c.Create(ctx, target)
	}
	base := target.DeepCopy()
	if target.Data == nil {
		target.Data = map[string]string{}
	}
	if target.Data[cfg.TargetKey] == string(publicCAPEM) {
		return nil
	}
	target.Data[cfg.TargetKey] = string(publicCAPEM)
	return c.Patch(ctx, target, client.MergeFrom(base))
}

func readKubeletServingCA(ctx context.Context, c client.Client, cfg KubeletServingCAConfig) ([]byte, error) {
	kind := strings.ToLower(cfg.SourceKind)
	switch kind {
	case "", "configmap", "configmaps":
		cm := &corev1.ConfigMap{}
		if err := c.Get(ctx, types.NamespacedName{Name: cfg.SourceName, Namespace: cfg.SourceNamespace}, cm); err != nil {
			return nil, fmt.Errorf("getting kubelet serving CA source ConfigMap %s/%s: %w", cfg.SourceNamespace, cfg.SourceName, err)
		}
		ca := strings.TrimSpace(cm.Data[cfg.SourceKey])
		if ca == "" {
			return nil, fmt.Errorf("source ConfigMap %s/%s key %q is empty", cfg.SourceNamespace, cfg.SourceName, cfg.SourceKey)
		}
		return []byte(ca + "\n"), nil
	case "secret", "secrets":
		secret := &corev1.Secret{}
		if err := c.Get(ctx, types.NamespacedName{Name: cfg.SourceName, Namespace: cfg.SourceNamespace}, secret); err != nil {
			return nil, fmt.Errorf("getting kubelet serving CA source Secret %s/%s: %w", cfg.SourceNamespace, cfg.SourceName, err)
		}
		ca := strings.TrimSpace(string(secret.Data[cfg.SourceKey]))
		if ca == "" {
			return nil, fmt.Errorf("source Secret %s/%s key %q is empty", cfg.SourceNamespace, cfg.SourceName, cfg.SourceKey)
		}
		return []byte(ca + "\n"), nil
	default:
		return nil, fmt.Errorf("unsupported kubelet serving CA source kind %q", cfg.SourceKind)
	}
}

func objectMeta(name, namespace string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "remote-cluster-provisioner"}}
}

func (r *NodeProvisionReconciler) statefulMigrationReadiness(ctx context.Context, np *mlv1alpha1.NodeProvision, node *corev1.Node) (bool, string, error) {
	stateful, err := r.statefulMigrationRequested(ctx, np)
	if err != nil {
		return false, "", err
	}
	if !stateful {
		return true, "", nil
	}
	if requiresGPUResource(np) && !nodeHasGPUResource(node) {
		return false, "StatefulMigration GPU node is not ready: nvidia.com/gpu is absent from node capacity or allocatable", nil
	}
	cfg := r.KubeletServingCA.withDefaults()
	if !cfg.sourceConfigured() {
		return false, "StatefulMigration TLS gate is not configured: kubelet serving CA source is required", nil
	}
	trustedIP := trustedKubeletIPForNode(np, node)
	if trustedIP == "" {
		return false, "StatefulMigration TLS gate is waiting for NodeProvision trusted IP to appear on the Node", nil
	}
	if err := syncKubeletServingCABundle(ctx, r.Client, cfg); err != nil {
		return false, "", err
	}
	ok, reason, err := r.nodeHasTrustedKubeletServingCert(ctx, node, cfg, trustedIP)
	if !ok || err != nil {
		return ok, reason, err
	}
	return true, "", nil
}

func (r *NodeProvisionReconciler) statefulMigrationRequested(ctx context.Context, np *mlv1alpha1.NodeProvision) (bool, error) {
	if np.Spec.Provider != mlv1alpha1.CloudProviderAWS {
		return false, nil
	}
	configs := &mlv1alpha1.NodeProvisionNetConfigList{}
	if err := r.List(ctx, configs, client.InNamespace(np.Namespace)); err != nil {
		return false, err
	}
	if len(configs.Items) == 0 {
		return false, nil
	}
	if len(configs.Items) > 1 {
		return false, fmt.Errorf("multiple NodeProvisionNetConfig resources found in namespace %s", np.Namespace)
	}
	software := configs.Items[0].Spec.SoftwareConfig.NodeSoftware
	return software != nil && software.RuntimeProfile == "StatefulMigration", nil
}

func requiresGPUResource(np *mlv1alpha1.NodeProvision) bool {
	return strings.EqualFold(np.Spec.HardwareType, "gpu") || strings.Contains(strings.ToLower(np.Spec.NodeLabel), "gpu")
}

func nodeHasGPUResource(node *corev1.Node) bool {
	capacity, hasCapacity := node.Status.Capacity[corev1.ResourceName("nvidia.com/gpu")]
	allocatable, hasAllocatable := node.Status.Allocatable[corev1.ResourceName("nvidia.com/gpu")]
	zero := resource.MustParse("0")
	return hasCapacity && hasAllocatable && capacity.Cmp(zero) > 0 && allocatable.Cmp(zero) > 0
}

func trustedKubeletIPForNode(np *mlv1alpha1.NodeProvision, node *corev1.Node) string {
	trusted := trustedProvisionedIPs(np)
	for _, addr := range node.Status.Addresses {
		if addr.Type != corev1.NodeInternalIP && addr.Type != corev1.NodeExternalIP {
			continue
		}
		if ip := net.ParseIP(addr.Address); ip != nil && trusted[ip.String()] {
			return ip.String()
		}
	}
	return ""
}

func (r *NodeProvisionReconciler) nodeHasTrustedKubeletServingCert(ctx context.Context, node *corev1.Node, cfg KubeletServingCAConfig, trustedIP string) (bool, string, error) {
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Name: cfg.TargetName, Namespace: cfg.TargetNamespace}, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "StatefulMigration TLS gate is waiting for kubelet-serving-ca ConfigMap", nil
		}
		return false, "", err
	}
	roots, err := certPoolFromPEM([]byte(cm.Data[cfg.TargetKey]))
	if err != nil {
		return false, "", err
	}
	checker := r.KubeletServingTLSChecker
	if checker == nil {
		checker = defaultKubeletServingTLSChecker()
	}
	if err := checker.Check(ctx, trustedIP, node.Name, roots); err != nil {
		return false, "StatefulMigration TLS gate is waiting for kubelet 10250 to present a serving certificate trusted by the configured kubelet-serving CA", nil
	}
	return true, "", nil
}
func certPoolFromPEM(caPEM []byte) (*x509.CertPool, error) {
	pool, _, err := kubeletServingCAPoolAndPublicPEM(caPEM)
	return pool, err
}

func kubeletServingCAPoolAndPublicPEM(caPEM []byte) (*x509.CertPool, []byte, error) {
	pool := x509.NewCertPool()
	rest := caPEM
	var public bytes.Buffer
	added := false
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			if strings.TrimSpace(string(rest)) != "" {
				return nil, nil, fmt.Errorf("trailing non-PEM data found")
			}
			break
		}
		if strings.TrimSpace(string(rest[:len(rest)-len(remaining)])) == "" {
			return nil, nil, fmt.Errorf("empty PEM block found")
		}
		rest = remaining
		if block.Type != "CERTIFICATE" {
			return nil, nil, fmt.Errorf("unexpected PEM block type %q", block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, err
		}
		if !cert.IsCA || !cert.BasicConstraintsValid {
			return nil, nil, fmt.Errorf("certificate %q is not a CA certificate", cert.Subject.CommonName)
		}
		pool.AddCert(cert)
		if err := pem.Encode(&public, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}); err != nil {
			return nil, nil, err
		}
		added = true
	}
	if !added {
		return nil, nil, fmt.Errorf("no PEM certificates found")
	}
	return pool, public.Bytes(), nil
}
