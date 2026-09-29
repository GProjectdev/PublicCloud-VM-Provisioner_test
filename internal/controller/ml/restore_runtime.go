package ml

import (
	"context"
	api "dcn.ssu.ac.kr/infra/api/ml/v1alpha1"
	"dcn.ssu.ac.kr/infra/pkg/nodesoftware"
	"dcn.ssu.ac.kr/infra/pkg/ssh"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
)

// certifyRestoreRuntime only applies to new AWS StatefulMigration nodes and an
// explicit admin-qualified environment. NodeReady alone never grants capability.
func (r *NodeProvisionReconciler) certifyRestoreRuntime(ctx context.Context, np *api.NodeProvision, node *corev1.Node) error {
	stateful, err := r.statefulMigrationRequested(ctx, np)
	if err != nil || !stateful {
		return err
	}
	nc, err := r.requireNetConfig(ctx, np)
	if err != nil {
		return err
	}
	software := nc.Spec.SoftwareConfig.NodeSoftware
	if software.MigrationRuntime == nil || !software.MigrationRuntime.CertifyRestore {
		return nil
	}
	command, err := nodesoftware.RestoreVerificationCommand(software, nc.Spec.SoftwareConfig.KubernetesVersion)
	if err != nil {
		return err
	}
	remote, err := r.getSSHClientByProvider(ctx, np)
	if err != nil {
		return err
	}
	defer remote.Conn.Close()
	if output, err := ssh.Run(remote, nodesoftware.RestoreLinkerCommand()); err != nil {
		return fmt.Errorf("restore driver linker preparation failed: %w: %.1500s", err, output)
	}
	output, err := ssh.Run(remote, command)
	if err != nil {
		return fmt.Errorf("runtime probe failed: %w: %.1500s", err, output)
	}
	if !strings.Contains(output, "RESTORE_RUNTIME_VERIFIED") {
		return fmt.Errorf("runtime probe returned no verification result")
	}
	patch := client.MergeFrom(node.DeepCopy())
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	node.Labels["migration.dcnlab.com/restore-from-file"] = "true"
	node.Annotations["migration.dcnlab.com/restore-package-sha256"] = strings.ToLower(software.MigrationRuntime.PackageSHA256)
	return r.Patch(ctx, node, patch)
}
