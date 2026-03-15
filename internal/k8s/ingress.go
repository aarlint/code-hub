package k8s

import (
	"context"
	"log"
	"strings"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const bridgeLabel = "code-hub.notdone.dev/vcluster-bridge"

// RunIngressBridge polls Ingress resources inside each running vCluster
// and mirrors them as host-cluster Ingresses.
func RunIngressBridge(ctx context.Context, hostClient *kubernetes.Clientset, onChange func()) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	prevState := make(map[string]string) // cluster -> comma-joined hosts

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// Get all vCluster kubeconfig secrets
		secrets, err := hostClient.CoreV1().Secrets(Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: LabelVCluster + "=true",
		})
		if err != nil {
			continue
		}

		activeVClusters := make(map[string]bool)

		for _, secret := range secrets.Items {
			clusterName := strings.TrimPrefix(secret.Name, VClusterSecretPrefix)
			activeVClusters[clusterName] = true

			kubeconfig, ok := secret.Data["kubeconfig"]
			if !ok {
				continue
			}

			// Build client for the vCluster
			vcClient, err := buildClientFromKubeconfig(kubeconfig)
			if err != nil {
				continue
			}

			// Get the owner from the vCluster namespace
			ns := vclusterNamespace(clusterName)
			namespace, err := hostClient.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
			if err != nil {
				continue
			}
			owner := namespace.Annotations[LabelOwner]
			if owner == "" {
				owner = namespace.Labels[LabelOwner]
			}

			// List Ingresses inside vCluster
			ingresses, err := vcClient.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
			if err != nil {
				continue
			}

			// Collect host rules
			var hostnames []string
			for _, ing := range ingresses.Items {
				for _, rule := range ing.Spec.Rules {
					if rule.Host != "" {
						hostnames = append(hostnames, rule.Host)
					}
				}
			}

			key := strings.Join(hostnames, ",")
			if prevState[clusterName] == key {
				continue
			}
			prevState[clusterName] = key

			// Mirror each Ingress to the host cluster
			syncHostIngresses(ctx, hostClient, clusterName, owner, hostnames)
			onChange()
		}

		// Clean up ingresses for deleted vClusters
		cleanupOrphanedBridgeIngresses(ctx, hostClient, activeVClusters)
	}
}

// syncHostIngresses creates/updates host-cluster Ingresses that route to vCluster synced services.
func syncHostIngresses(ctx context.Context, client *kubernetes.Clientset, clusterName, owner string, vcHosts []string) {
	vcNamespace := vclusterNamespace(clusterName)

	// First, remove any existing bridge ingresses for this cluster that are no longer needed
	existing, err := client.NetworkingV1().Ingresses(Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: bridgeLabel + "=" + clusterName,
	})
	if err == nil {
		activeHosts := make(map[string]bool)
		for _, h := range vcHosts {
			activeHosts[h+"-"+clusterName] = true
		}
		for _, ing := range existing.Items {
			if !activeHosts[ing.Name] {
				client.NetworkingV1().Ingresses(Namespace).Delete(ctx, ing.Name, metav1.DeleteOptions{})
			}
		}
	}

	// Create/update ingresses for each host
	pathType := networkingv1.PathTypePrefix
	for _, vcHost := range vcHosts {
		hostIngressName := vcHost + "-" + clusterName
		hostFQDN := vcHost + "-" + clusterName + "." + Domain

		// The vCluster syncer creates a synced service in the vCluster namespace
		// The service name pattern is: {original-service-name}-x-{original-namespace}-x-{vcluster-name}
		// For simplicity, we route to the vCluster's syncer service directly
		// which handles forwarding to the correct backend
		svcName := clusterName
		svcNamespace := vcNamespace

		ing := &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Name:      hostIngressName,
				Namespace: Namespace,
				Labels: map[string]string{
					bridgeLabel: clusterName,
					LabelOwner:  sanitizeLabelValue(owner),
				},
				Annotations: mergeAnnotations(
					map[string]string{
						LabelOwner:  owner,
						bridgeLabel: clusterName,
					},
					ingressAnnotations(),
				),
			},
			Spec: networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{
					{
						Host: hostFQDN,
						IngressRuleValue: networkingv1.IngressRuleValue{
							HTTP: &networkingv1.HTTPIngressRuleValue{
								Paths: []networkingv1.HTTPIngressPath{
									{
										Path:     "/",
										PathType: &pathType,
										Backend: networkingv1.IngressBackend{
											Service: &networkingv1.IngressServiceBackend{
												Name: svcName,
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

		_, err := client.NetworkingV1().Ingresses(Namespace).Get(ctx, hostIngressName, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			if _, err := client.NetworkingV1().Ingresses(Namespace).Create(ctx, ing, metav1.CreateOptions{}); err != nil {
				log.Printf("create bridge ingress %s: %v", hostIngressName, err)
			}
		} else if err == nil {
			if _, err := client.NetworkingV1().Ingresses(Namespace).Update(ctx, ing, metav1.UpdateOptions{}); err != nil {
				log.Printf("update bridge ingress %s: %v", hostIngressName, err)
			}
		}

		_ = svcNamespace // TODO: use ExternalName service to route to vCluster namespace
	}
}

func cleanupOrphanedBridgeIngresses(ctx context.Context, client *kubernetes.Clientset, activeVClusters map[string]bool) {
	ingresses, err := client.NetworkingV1().Ingresses(Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: bridgeLabel,
	})
	if err != nil {
		return
	}

	for _, ing := range ingresses.Items {
		clusterName := ing.Labels[bridgeLabel]
		if clusterName != "" && !activeVClusters[clusterName] {
			client.NetworkingV1().Ingresses(Namespace).Delete(ctx, ing.Name, metav1.DeleteOptions{})
			log.Printf("cleaned up orphaned bridge ingress: %s", ing.Name)
		}
	}
}

// CleanupVClusterIngresses removes all host-cluster Ingresses for a given vCluster.
func CleanupVClusterIngresses(ctx context.Context, client *kubernetes.Clientset, clusterName string) {
	ingresses, err := client.NetworkingV1().Ingresses(Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: bridgeLabel + "=" + clusterName,
	})
	if err != nil {
		return
	}
	for _, ing := range ingresses.Items {
		client.NetworkingV1().Ingresses(Namespace).Delete(ctx, ing.Name, metav1.DeleteOptions{})
	}
}

func buildClientFromKubeconfig(kubeconfig []byte) (*kubernetes.Clientset, error) {
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	config.Timeout = 5 * time.Second
	return kubernetes.NewForConfig(config)
}
