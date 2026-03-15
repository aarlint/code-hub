package auth

import (
	"context"
	"log"
	"net/http"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"code-hub/internal/idle"
	"code-hub/internal/k8s"
)

// Handler handles ForwardAuth requests from Traefik.
// It checks that the Cf-Access-Authenticated-User-Email matches the owner
// annotation on the Ingress resource matching X-Forwarded-Host.
type Handler struct {
	Client      *kubernetes.Clientset
	IdleTracker *idle.Tracker
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		http.Error(w, "missing forwarded host", 400)
		return
	}

	// Extract resource name from hostname (e.g. "code-austin-a1b2.notdone.dev" → "code-austin-a1b2")
	resourceName := strings.SplitN(host, ".", 2)[0]

	ctx := context.Background()

	// Look up the Ingress resource matching this host
	ing, err := h.Client.NetworkingV1().Ingresses(k8s.Namespace).Get(ctx, resourceName, metav1.GetOptions{})
	if err != nil {
		// Try to find by scanning all ingresses for the host rule
		owner := h.findOwnerByHost(ctx, host)
		if owner == "" {
			http.Error(w, "forbidden", 403)
			return
		}
		h.checkOwnerAndRespond(w, r, owner, resourceName)
		return
	}

	// Get owner from annotations
	owner := ing.Annotations[k8s.LabelOwner]
	if owner == "" {
		owner = ing.Labels[k8s.LabelOwner]
	}

	h.checkOwnerAndRespond(w, r, owner, resourceName)
}

func (h *Handler) checkOwnerAndRespond(w http.ResponseWriter, r *http.Request, resourceOwner, resourceName string) {
	requestingUser := strings.ToLower(r.Header.Get("Cf-Access-Authenticated-User-Email"))
	if requestingUser == "" || requestingUser != resourceOwner {
		log.Printf("auth denied: user=%q owner=%q resource=%s", requestingUser, resourceOwner, resourceName)
		http.Error(w, "forbidden", 403)
		return
	}

	// Touch idle tracker for workspace resources (not terminals)
	if !strings.HasPrefix(resourceName, "terminal-") {
		h.IdleTracker.Touch(resourceName)
	}

	w.WriteHeader(200)
}

// findOwnerByHost searches all Ingresses in the namespace for one with a matching host rule.
func (h *Handler) findOwnerByHost(ctx context.Context, host string) string {
	ingresses, err := h.Client.NetworkingV1().Ingresses(k8s.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return ""
	}

	for _, ing := range ingresses.Items {
		for _, rule := range ing.Spec.Rules {
			if rule.Host == host {
				if o := ing.Annotations[k8s.LabelOwner]; o != "" {
					return o
				}
				return ing.Labels[k8s.LabelOwner]
			}
		}
	}
	return ""
}
