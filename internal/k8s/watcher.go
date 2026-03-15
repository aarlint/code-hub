package k8s

import (
	"context"
	"log"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

// WatchResources watches Deployments, Pods, and Ingresses with managed labels
// and calls onChange whenever a relevant resource changes.
func WatchResources(ctx context.Context, client *kubernetes.Clientset, onChange func()) {
	labelSelector := LabelManaged + "=true"

	// Watch deployments
	go watchLoop(ctx, "deployments", func() (watch.Interface, error) {
		return client.AppsV1().Deployments(Namespace).Watch(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
	}, onChange)

	// Watch pods (for readiness status)
	go watchLoop(ctx, "pods", func() (watch.Interface, error) {
		return client.CoreV1().Pods(Namespace).Watch(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
	}, onChange)

	// Watch terminal pods
	go watchLoop(ctx, "terminal-pods", func() (watch.Interface, error) {
		return client.CoreV1().Pods(Namespace).Watch(ctx, metav1.ListOptions{
			LabelSelector: LabelTerminal + "=true",
		})
	}, onChange)

	// Watch vCluster StatefulSets in all vc-* namespaces
	go watchVClusterStatefulSets(ctx, client, onChange)
}

func watchLoop(ctx context.Context, name string, watchFn func() (watch.Interface, error), onChange func()) {
	for {
		w, err := watchFn()
		if err != nil {
			log.Printf("watch %s error: %v (retrying in 5s)", name, err)
			time.Sleep(5 * time.Second)
			continue
		}

		for event := range w.ResultChan() {
			switch event.Type {
			case watch.Added, watch.Modified, watch.Deleted:
				// Debounce: small delay to batch rapid changes
				time.Sleep(200 * time.Millisecond)
				onChange()
			}
		}

		log.Printf("watch %s channel closed, reconnecting...", name)
		time.Sleep(2 * time.Second)
	}
}

func watchVClusterStatefulSets(ctx context.Context, client *kubernetes.Clientset, onChange func()) {
	for {
		// List vc-* namespaces
		nsList, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
			LabelSelector: LabelVCluster + "=true",
		})
		if err != nil {
			time.Sleep(10 * time.Second)
			continue
		}

		for _, ns := range nsList.Items {
			nsName := ns.Name
			go watchLoop(ctx, "sts-"+nsName, func() (watch.Interface, error) {
				return client.AppsV1().StatefulSets(nsName).Watch(ctx, metav1.ListOptions{})
			}, onChange)
		}

		// Re-check for new namespaces every 30s
		time.Sleep(30 * time.Second)
	}
}

// WatchVClusterNamespaces returns the list of vCluster namespace names.
func WatchVClusterNamespaces(ctx context.Context, client *kubernetes.Clientset) []string {
	nsList, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: LabelVCluster + "=true",
	})
	if err != nil {
		return nil
	}
	var names []string
	for _, ns := range nsList.Items {
		names = append(names, strings.TrimPrefix(ns.Name, "vc-"))
	}
	return names
}
