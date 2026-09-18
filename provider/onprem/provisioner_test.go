package onprem

import "testing"

func TestKubernetesAPIAllowedIP(t *testing.T) {
	got, err := KubernetesAPIAllowedIP("kubeadm join 192.168.30.100:6443 --token abc.def")
	if err != nil {
		t.Fatalf("KubernetesAPIAllowedIP returned an error: %v", err)
	}
	if got != "192.168.30.100/32" {
		t.Fatalf("KubernetesAPIAllowedIP = %q, want %q", got, "192.168.30.100/32")
	}
}

func TestKubernetesAPIAllowedIPRejectsHostname(t *testing.T) {
	if _, err := KubernetesAPIAllowedIP("kubeadm join api.example.com:6443 --token abc.def"); err == nil {
		t.Fatal("KubernetesAPIAllowedIP accepted a hostname that WireGuard cannot use as AllowedIPs")
	}
}
