package k8s

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"

	corev1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"code-hub/internal/types"
)

const (
	LabelVCluster       = "code-hub.notdone.dev/vcluster"
	VClusterSecretPrefix = "vc-kubeconfig-"
)

func vclusterNamespace(name string) string {
	return "vc-" + name
}

// CreateVCluster creates a new vCluster: namespace → vcluster create → store kubeconfig Secret.
func CreateVCluster(ctx context.Context, client *kubernetes.Clientset, name, owner string) error {
	ns := vclusterNamespace(name)

	// Create namespace with owner annotation
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				LabelVCluster: "true",
				LabelOwner:    owner,
			},
			Annotations: map[string]string{
				LabelOwner: owner,
			},
		},
	}
	if _, err := client.CoreV1().Namespaces().Create(ctx, namespace, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", ns, err)
	}

	// Create vCluster via CLI
	cmd := exec.CommandContext(ctx, "vcluster", "create", name, "-n", ns, "--connect=false")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vcluster create: %s: %w", string(output), err)
	}
	log.Printf("created vcluster %s in namespace %s", name, ns)

	// Extract and store kubeconfig
	if err := refreshKubeconfigSecret(ctx, client, name); err != nil {
		return fmt.Errorf("store kubeconfig: %w", err)
	}

	return nil
}

// DeleteVCluster removes a vCluster and all associated resources.
func DeleteVCluster(ctx context.Context, client *kubernetes.Clientset, name string) error {
	ns := vclusterNamespace(name)

	// Remove terminal resources
	DeleteTerminal(ctx, client, name)

	// Remove host-cluster ingresses for this vCluster
	CleanupVClusterIngresses(ctx, client, name)

	// Delete vCluster via CLI
	cmd := exec.CommandContext(ctx, "vcluster", "delete", name, "-n", ns)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("vcluster delete warning: %s: %v", string(output), err)
	}

	// Delete kubeconfig secret
	secretName := VClusterSecretPrefix + name
	client.CoreV1().Secrets(Namespace).Delete(ctx, secretName, metav1.DeleteOptions{})

	// Delete namespace
	client.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})

	log.Printf("deleted vcluster %s", name)
	return nil
}

// PauseVCluster pauses a running vCluster.
func PauseVCluster(ctx context.Context, name string) error {
	ns := vclusterNamespace(name)
	cmd := exec.CommandContext(ctx, "vcluster", "pause", name, "-n", ns)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vcluster pause: %s: %w", string(output), err)
	}
	log.Printf("paused vcluster %s", name)
	return nil
}

// ResumeVCluster resumes a paused vCluster and refreshes the kubeconfig.
func ResumeVCluster(ctx context.Context, client *kubernetes.Clientset, name string) error {
	ns := vclusterNamespace(name)
	cmd := exec.CommandContext(ctx, "vcluster", "resume", name, "-n", ns)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vcluster resume: %s: %w", string(output), err)
	}
	log.Printf("resumed vcluster %s", name)

	// Refresh kubeconfig after resume
	if err := refreshKubeconfigSecret(ctx, client, name); err != nil {
		log.Printf("kubeconfig refresh warning for %s: %v", name, err)
	}
	return nil
}

// ListVClusters returns all vClusters for a given owner.
func ListVClusters(ctx context.Context, client *kubernetes.Clientset, owner string) []types.ClusterInfo {
	namespaces, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: LabelVCluster + "=true",
	})
	if err != nil {
		log.Printf("list vcluster namespaces: %v", err)
		return []types.ClusterInfo{}
	}

	var result []types.ClusterInfo
	for _, ns := range namespaces.Items {
		nsOwner := ns.Labels[LabelOwner]
		if nsOwner == "" {
			nsOwner = ns.Annotations[LabelOwner]
		}
		if nsOwner != owner {
			continue
		}

		name := strings.TrimPrefix(ns.Name, "vc-")
		status := getVClusterStatus(ctx, client, name, ns.Name)

		ci := types.ClusterInfo{
			Name:   name,
			Status: status,
		}

		// Check for terminal
		terminalName := "terminal-" + name
		pod, err := client.CoreV1().Pods(Namespace).Get(ctx, terminalName, metav1.GetOptions{})
		if err == nil {
			ci.TerminalURL = "https://terminal-" + name + "." + Domain
			ci.TerminalState = string(pod.Status.Phase)
			if pod.Status.Phase == corev1.PodRunning {
				ci.TerminalState = "running"
			}
		}

		// Exposed apps from host-cluster ingresses
		ci.ExposedApps = getExposedApps(ctx, client, name)

		result = append(result, ci)
	}

	if result == nil {
		result = []types.ClusterInfo{}
	}
	return result
}

// getVClusterStatus checks the StatefulSet replica count to determine vCluster status.
func getVClusterStatus(ctx context.Context, client *kubernetes.Clientset, name, namespace string) string {
	// vCluster creates a StatefulSet named after the vCluster
	stsList, err := client.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "unknown"
	}

	for _, sts := range stsList.Items {
		if sts.Name == name || strings.Contains(sts.Name, name) {
			return statefulSetStatus(&sts)
		}
	}
	return "unknown"
}

func statefulSetStatus(sts *appsv1.StatefulSet) string {
	if sts.Spec.Replicas != nil && *sts.Spec.Replicas == 0 {
		return "paused"
	}
	if sts.Status.ReadyReplicas > 0 {
		return "running"
	}
	if sts.Spec.Replicas != nil && *sts.Spec.Replicas > 0 {
		return "starting"
	}
	return "stopped"
}

// refreshKubeconfigSecret runs `vcluster connect --print` and stores the result as a Secret.
func refreshKubeconfigSecret(ctx context.Context, client *kubernetes.Clientset, name string) error {
	ns := vclusterNamespace(name)
	cmd := exec.CommandContext(ctx, "vcluster", "connect", name, "-n", ns, "--print")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("vcluster connect --print: %w", err)
	}

	secretName := VClusterSecretPrefix + name
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: Namespace,
			Labels: map[string]string{
				LabelVCluster: "true",
			},
		},
		Data: map[string][]byte{
			"kubeconfig": output,
		},
	}

	existing, err := client.CoreV1().Secrets(Namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		// Create new
		_, err = client.CoreV1().Secrets(Namespace).Create(ctx, secret, metav1.CreateOptions{})
		return err
	}
	// Update existing
	existing.Data = secret.Data
	_, err = client.CoreV1().Secrets(Namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// getExposedApps finds host-cluster Ingresses that mirror vCluster app ingresses.
func getExposedApps(ctx context.Context, client *kubernetes.Clientset, clusterName string) []string {
	ingresses, err := client.NetworkingV1().Ingresses(Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "code-hub.notdone.dev/vcluster-bridge=" + clusterName,
	})
	if err != nil {
		return nil
	}

	var apps []string
	for _, ing := range ingresses.Items {
		for _, rule := range ing.Spec.Rules {
			if rule.Host != "" {
				apps = append(apps, strings.TrimSuffix(rule.Host, "."+Domain))
			}
		}
	}
	return apps
}
