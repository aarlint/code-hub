package k8s

import (
	"context"
	"fmt"
	"log"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

const (
	LabelTerminal        = "code-hub.notdone.dev/terminal"
	LabelTerminalCluster = "code-hub.notdone.dev/terminal-cluster"
	TerminalImage        = "ghcr.io/aarlint/kube-terminal:latest"
	TerminalPort         = 7681
)

// CreateTerminal creates a Pod + Service + Ingress for a terminal connected to a vCluster.
func CreateTerminal(ctx context.Context, client *kubernetes.Clientset, clusterName, owner string) (string, error) {
	terminalName := "terminal-" + clusterName
	url := "https://" + terminalName + "." + Domain

	// Check if Pod already exists
	existing, err := client.CoreV1().Pods(Namespace).Get(ctx, terminalName, metav1.GetOptions{})
	if err == nil {
		// Pod exists — if not running, delete and recreate
		if existing.Status.Phase != corev1.PodRunning && existing.Status.Phase != corev1.PodPending {
			client.CoreV1().Pods(Namespace).Delete(ctx, terminalName, metav1.DeleteOptions{})
		} else {
			return url, nil
		}
	}

	labels := map[string]string{
		LabelTerminal:        "true",
		LabelTerminalCluster: clusterName,
		LabelOwner:           owner,
		"app":                terminalName,
	}

	annotations := map[string]string{
		LabelTerminal:        "true",
		LabelTerminalCluster: clusterName,
		LabelOwner:           owner,
	}

	secretName := VClusterSecretPrefix + clusterName

	// Pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        terminalName,
			Namespace:   Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{
				{
					Name:  "terminal",
					Image: TerminalImage,
					Ports: []corev1.ContainerPort{{ContainerPort: int32(TerminalPort)}},
					Env: []corev1.EnvVar{
						{Name: "KUBECONFIG", Value: "/root/.kube/kubeconfig"},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "kubeconfig", MountPath: "/root/.kube", ReadOnly: true},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "kubeconfig",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: secretName,
						},
					},
				},
			},
		},
	}
	if _, err := client.CoreV1().Pods(Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("create terminal pod: %w", err)
	}

	// Service
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        terminalName,
			Namespace:   Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": terminalName},
			Ports: []corev1.ServicePort{
				{Port: 80, TargetPort: intstr.FromInt32(int32(TerminalPort))},
			},
		},
	}
	if _, err := client.CoreV1().Services(Namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create terminal service: %w", err)
	}

	// Ingress
	pathType := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      terminalName,
			Namespace: Namespace,
			Labels:    labels,
			Annotations: mergeAnnotations(annotations, ingressAnnotations()),
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: terminalName + "." + Domain,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: terminalName,
											Port: networkingv1.ServiceBackendPort{Number: 80},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if _, err := client.NetworkingV1().Ingresses(Namespace).Create(ctx, ing, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create terminal ingress: %w", err)
	}

	log.Printf("created terminal for vcluster %s: %s", clusterName, url)
	return url, nil
}

// DeleteTerminal removes the terminal Pod + Service + Ingress for a vCluster.
func DeleteTerminal(ctx context.Context, client *kubernetes.Clientset, clusterName string) {
	terminalName := "terminal-" + clusterName

	client.CoreV1().Pods(Namespace).Delete(ctx, terminalName, metav1.DeleteOptions{})
	client.CoreV1().Services(Namespace).Delete(ctx, terminalName, metav1.DeleteOptions{})
	client.NetworkingV1().Ingresses(Namespace).Delete(ctx, terminalName, metav1.DeleteOptions{})
	log.Printf("deleted terminal for vcluster %s", clusterName)
}
