package k8s

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"code-hub/internal/types"
)

const (
	LabelManaged = "code-hub.notdone.dev/managed"
	LabelOwner   = "code-hub.notdone.dev/owner"
	LabelType    = "code-hub.notdone.dev/type"

	Domain    = "notdone.dev"
	Namespace = "default"

	MiddlewareName = "code-hub-auth"
)

var (
	emailRe       = regexp.MustCompile(`[^a-z0-9]`)
	ClusterNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)
)

type workspaceType struct {
	Image         string
	ContainerPort int32
	VolumePath    string
	Prefix        string
	Env           []corev1.EnvVar
}

var WorkspaceTypes = map[string]workspaceType{
	"vscode": {
		Image:         "lscr.io/linuxserver/code-server:latest",
		ContainerPort: 8443,
		VolumePath:    "/config",
		Prefix:        "code",
		Env: []corev1.EnvVar{
			{Name: "PUID", Value: "1000"},
			{Name: "PGID", Value: "1000"},
			{Name: "TZ", Value: "America/New_York"},
			{Name: "DEFAULT_WORKSPACE", Value: "/config/workspace"},
		},
	},
	"ai-code": {
		Image:         "ghcr.io/aarlint/claude-code-web:latest",
		ContainerPort: 3000,
		VolumePath:    "/home/node",
		Prefix:        "ai",
		Env: []corev1.EnvVar{
			{Name: "TZ", Value: "America/New_York"},
		},
	},
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func sanitizeEmail(email string) string {
	prefix := strings.SplitN(email, "@", 2)[0]
	return emailRe.ReplaceAllString(strings.ToLower(prefix), "")
}

func managedLabels(owner, wsType string) map[string]string {
	return map[string]string{
		LabelManaged: "true",
		LabelOwner:   owner,
		LabelType:    wsType,
	}
}

func managedAnnotations(owner, wsType string) map[string]string {
	return map[string]string{
		LabelManaged: "true",
		LabelOwner:   owner,
		LabelType:    wsType,
	}
}

func ingressAnnotations() map[string]string {
	return map[string]string{
		"traefik.ingress.kubernetes.io/router.middlewares": Namespace + "-" + MiddlewareName + "@kubernetescrd",
	}
}

// CreateWorkspace creates a Deployment + Service + Ingress + PVC for a workspace.
func CreateWorkspace(ctx context.Context, client *kubernetes.Clientset, owner, wsType, clusterName string) (*types.InstanceInfo, error) {
	wt, ok := WorkspaceTypes[wsType]
	if !ok {
		return nil, fmt.Errorf("unknown workspace type: %s", wsType)
	}

	prefix := sanitizeEmail(owner)
	if prefix == "" {
		prefix = "user"
	}
	name := wt.Prefix + "-" + prefix + "-" + randHex(2)
	labels := managedLabels(owner, wsType)
	annotations := managedAnnotations(owner, wsType)

	if clusterName != "" {
		annotations["code-hub.notdone.dev/cluster"] = clusterName
	}

	// 1. PVC
	pvcName := name + "-data"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        pvcName,
			Namespace:   Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: strPtr("local-path"),
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("5Gi"),
				},
			},
		},
	}
	if _, err := client.CoreV1().PersistentVolumeClaims(Namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create PVC: %w", err)
	}

	// 2. Deployment
	replicas := int32(1)
	envVars := append([]corev1.EnvVar{}, wt.Env...)
	volumes := []corev1.Volume{
		{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
				},
			},
		},
	}
	volumeMounts := []corev1.VolumeMount{
		{Name: "data", MountPath: wt.VolumePath},
	}

	// Mount vCluster kubeconfig if cluster is specified
	if clusterName != "" {
		secretName := "vc-kubeconfig-" + clusterName
		volumes = append(volumes, corev1.Volume{
			Name: "kubeconfig",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: secretName,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "kubeconfig",
			MountPath: "/home/.kube",
			ReadOnly:  true,
		})
		envVars = append(envVars,
			corev1.EnvVar{Name: "KUBECONFIG", Value: "/home/.kube/kubeconfig"},
		)
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":        name,
						LabelManaged: "true",
						LabelOwner:   owner,
						LabelType:    wsType,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:         "workspace",
							Image:        wt.Image,
							Ports:        []corev1.ContainerPort{{ContainerPort: wt.ContainerPort}},
							Env:          envVars,
							VolumeMounts: volumeMounts,
						},
					},
					Volumes: volumes,
				},
			},
		},
	}
	if _, err := client.AppsV1().Deployments(Namespace).Create(ctx, deploy, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create Deployment: %w", err)
	}

	// 3. Service
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports: []corev1.ServicePort{
				{Port: 80, TargetPort: intstr.FromInt32(wt.ContainerPort)},
			},
		},
	}
	if _, err := client.CoreV1().Services(Namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create Service: %w", err)
	}

	// 4. Ingress
	pathType := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
			Labels:    labels,
			Annotations: mergeAnnotations(annotations, ingressAnnotations()),
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: name + "." + Domain,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: name,
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
	if _, err := client.NetworkingV1().Ingresses(Namespace).Create(ctx, ing, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create Ingress: %w", err)
	}

	info := &types.InstanceInfo{
		Name:    name,
		Type:    wsType,
		State:   "running",
		Status:  "starting",
		URL:     "https://" + name + "." + Domain,
		Owner:   owner,
		Cluster: clusterName,
	}
	return info, nil
}

// StopWorkspace scales the Deployment to 0 replicas.
func StopWorkspace(ctx context.Context, client *kubernetes.Clientset, name string) error {
	deploy, err := client.AppsV1().Deployments(Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}
	replicas := int32(0)
	deploy.Spec.Replicas = &replicas
	_, err = client.AppsV1().Deployments(Namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	return err
}

// StartWorkspace scales the Deployment to 1 replica.
func StartWorkspace(ctx context.Context, client *kubernetes.Clientset, name string) error {
	deploy, err := client.AppsV1().Deployments(Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}
	replicas := int32(1)
	deploy.Spec.Replicas = &replicas
	_, err = client.AppsV1().Deployments(Namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	return err
}

// DeleteWorkspace removes the Deployment, Service, Ingress, and PVC.
func DeleteWorkspace(ctx context.Context, client *kubernetes.Clientset, name string) error {
	propagation := metav1.DeletePropagationForeground
	opts := metav1.DeleteOptions{PropagationPolicy: &propagation}

	var errs []string
	if err := client.AppsV1().Deployments(Namespace).Delete(ctx, name, opts); err != nil && !errors.IsNotFound(err) {
		errs = append(errs, "deployment: "+err.Error())
	}
	if err := client.CoreV1().Services(Namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		errs = append(errs, "service: "+err.Error())
	}
	if err := client.NetworkingV1().Ingresses(Namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		errs = append(errs, "ingress: "+err.Error())
	}
	pvcName := name + "-data"
	if err := client.CoreV1().PersistentVolumeClaims(Namespace).Delete(ctx, pvcName, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		errs = append(errs, "pvc: "+err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("delete errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ListWorkspaces returns all managed workspaces for an owner.
func ListWorkspaces(ctx context.Context, client *kubernetes.Clientset, owner string) []types.InstanceInfo {
	labelSelector := LabelManaged + "=true"
	deploys, err := client.AppsV1().Deployments(Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		log.Printf("list deployments: %v", err)
		return []types.InstanceInfo{}
	}

	var result []types.InstanceInfo
	for _, d := range deploys.Items {
		if d.Annotations[LabelOwner] != owner && d.Labels[LabelOwner] != owner {
			continue
		}

		state := "stopped"
		if d.Spec.Replicas != nil && *d.Spec.Replicas > 0 {
			if d.Status.ReadyReplicas > 0 {
				state = "running"
			} else {
				state = "starting"
			}
		}

		wsType := d.Labels[LabelType]
		if wsType == "" {
			wsType = "vscode"
		}

		info := types.InstanceInfo{
			Name:    d.Name,
			Type:    wsType,
			State:   state,
			Status:  state,
			URL:     "https://" + d.Name + "." + Domain,
			Owner:   owner,
			Cluster: d.Annotations["code-hub.notdone.dev/cluster"],
		}
		result = append(result, info)
	}

	if result == nil {
		result = []types.InstanceInfo{}
	}
	return result
}

// GetGlobalStats returns workspace counts across all users.
func GetGlobalStats(ctx context.Context, client *kubernetes.Clientset) types.GlobalStats {
	labelSelector := LabelManaged + "=true"
	deploys, err := client.AppsV1().Deployments(Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return types.GlobalStats{ByType: map[string]types.TypeStats{}}
	}

	s := types.GlobalStats{ByType: map[string]types.TypeStats{}}
	s.Total = len(deploys.Items)
	for _, d := range deploys.Items {
		running := d.Spec.Replicas != nil && *d.Spec.Replicas > 0 && d.Status.ReadyReplicas > 0
		if running {
			s.Running++
		}

		t := d.Labels[LabelType]
		if t == "" {
			t = "vscode"
		}
		ts := s.ByType[t]
		ts.Total++
		if running {
			ts.Running++
		}
		ts.Stopped = ts.Total - ts.Running
		s.ByType[t] = ts
	}
	s.Stopped = s.Total - s.Running
	return s
}

// GetWorkspaceOwner returns the owner of a workspace, or "" if not found.
func GetWorkspaceOwner(ctx context.Context, client *kubernetes.Clientset, name string) string {
	deploy, err := client.AppsV1().Deployments(Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	if o := deploy.Labels[LabelOwner]; o != "" {
		return o
	}
	return deploy.Annotations[LabelOwner]
}

func strPtr(s string) *string { return &s }

func mergeAnnotations(base, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}
