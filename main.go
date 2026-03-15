package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"code-hub/internal/auth"
	"code-hub/internal/hub"
	"code-hub/internal/idle"
	"code-hub/internal/k8s"
	"code-hub/internal/types"
)

const (
	idleTimeout    = 1 * time.Hour
	reapInterval   = 1 * time.Minute
)

var (
	listenAddr         = envStr("LISTEN", ":8080")
	staticDir          = envStr("STATIC_DIR", "/app")
	clusterIdleTimeout = envDuration("CLUSTER_IDLE_TIMEOUT", 8*time.Hour)
)

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func getOwner(r *http.Request) string {
	email := r.Header.Get("Cf-Access-Authenticated-User-Email")
	if email == "" {
		email = "local"
	}
	return strings.ToLower(email)
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	client, err := k8s.NewClient()
	if err != nil {
		log.Fatalf("k8s client: %v", err)
	}

	wsHub := hub.New()
	wsIdle := idle.NewTracker()
	clusterIdle := idle.NewTracker()

	getPayload := func(owner string) []byte {
		return getInstancesJSON(client, owner, wsIdle, clusterIdle)
	}

	broadcastAll := func() {
		wsHub.BroadcastAll(getPayload)
	}

	authHandler := &auth.Handler{
		Client:      client,
		IdleTracker: wsIdle,
	}

	ctx := context.Background()

	// Start background goroutines
	go k8s.WatchResources(ctx, client, broadcastAll)
	go k8s.RunIngressBridge(ctx, client, broadcastAll)
	go reapIdleWorkspaces(client, wsIdle, broadcastAll)
	go reapIdleClusters(client, clusterIdle, broadcastAll)
	go seedIdleTrackers(client, wsIdle, clusterIdle)

	mux := http.NewServeMux()

	// WebSocket
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWS(w, r, client, wsHub, wsIdle, clusterIdle)
	})

	// Instances (workspaces)
	mux.HandleFunc("/api/instances", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listInstances(w, r, client, wsIdle, clusterIdle)
		case http.MethodPost:
			createInstance(w, r, client, wsIdle, clusterIdle, broadcastAll)
		default:
			http.Error(w, "method not allowed", 405)
		}
	})

	mux.HandleFunc("/api/instances/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/instances/")
		parts := strings.SplitN(path, "/", 2)
		name := parts[0]
		if name == "" {
			http.Error(w, "missing name", 400)
			return
		}

		if len(parts) == 2 {
			switch {
			case parts[1] == "stop" && r.Method == http.MethodPost:
				stopInstance(w, r, client, name, wsIdle, broadcastAll)
			case parts[1] == "start" && r.Method == http.MethodPost:
				startInstance(w, r, client, name, wsIdle, broadcastAll)
			default:
				http.Error(w, "not found", 404)
			}
			return
		}

		if r.Method == http.MethodDelete {
			deleteInstance(w, r, client, name, wsIdle, broadcastAll)
			return
		}

		http.Error(w, "not found", 404)
	})

	// Clusters (vClusters)
	mux.HandleFunc("/api/clusters", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListClusters(w, r, client, clusterIdle)
		case http.MethodPost:
			handleCreateCluster(w, r, client, clusterIdle, broadcastAll)
		default:
			http.Error(w, "method not allowed", 405)
		}
	})

	mux.HandleFunc("/api/clusters/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/clusters/")
		parts := strings.SplitN(path, "/", 2)
		name := parts[0]
		if name == "" {
			http.Error(w, "missing cluster name", 400)
			return
		}

		if len(parts) == 2 {
			switch {
			case parts[1] == "start" && r.Method == http.MethodPost:
				handleStartCluster(w, r, client, name, clusterIdle, broadcastAll)
			case parts[1] == "stop" && r.Method == http.MethodPost:
				handleStopCluster(w, r, client, name, broadcastAll)
			case parts[1] == "extend" && r.Method == http.MethodPost:
				handleExtendCluster(w, r, clusterIdle, broadcastAll)
			case parts[1] == "terminal" && r.Method == http.MethodPost:
				handleLaunchTerminal(w, r, client, name, clusterIdle, broadcastAll)
			case parts[1] == "terminal" && r.Method == http.MethodDelete:
				handleRemoveTerminal(w, r, client, name, broadcastAll)
			default:
				http.Error(w, "not found", 404)
			}
			return
		}

		if r.Method == http.MethodDelete {
			handleDeleteCluster(w, r, client, name, broadcastAll)
			return
		}

		http.Error(w, "not found", 404)
	})

	// Auth
	mux.Handle("/api/auth", authHandler)

	// Static files
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Clean(r.URL.Path)
		if p == "/" || p == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
			return
		}
		file := filepath.Join(staticDir, p)
		if _, err := os.Stat(file); err != nil {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
			return
		}
		if strings.HasSuffix(p, ".js") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFile(w, r, file)
	})

	log.Printf("code-hub listening on %s", listenAddr)
	log.Fatal(http.ListenAndServe(listenAddr, mux))
}

// ===== WebSocket =====

func handleWS(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, wsHub *hub.Hub, wsIdle, clusterIdle *idle.Tracker) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

	owner := getOwner(r)
	wsHub.Add(owner, conn)
	log.Printf("ws connected: %s", owner)

	data := getInstancesJSON(client, owner, wsIdle, clusterIdle)
	conn.WriteMessage(websocket.TextMessage, data)

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}()

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}

	wsHub.Remove(owner, conn)
	conn.Close()
	log.Printf("ws disconnected: %s", owner)
}

// ===== Data Helpers =====

func getInstancesJSON(client *kubernetes.Clientset, owner string, wsIdle, clusterIdle *idle.Tracker) []byte {
	ctx := context.Background()
	instances := k8s.ListWorkspaces(ctx, client, owner)

	// Attach idle tracking data
	for i := range instances {
		if ts, ok := wsIdle.Get(instances[i].Name); ok {
			unix := ts.UnixMilli()
			instances[i].LastAccess = &unix
		}
	}

	clusters := k8s.ListVClusters(ctx, client, owner)
	for i := range clusters {
		if ts, ok := clusterIdle.Get(clusters[i].Name); ok {
			unix := ts.UnixMilli()
			clusters[i].LastStart = &unix
		}
	}

	resp := types.ListResponse{
		Instances:          instances,
		Global:             k8s.GetGlobalStats(ctx, client),
		Clusters:           clusters,
		ClusterIdleTimeout: clusterIdleTimeout.Milliseconds(),
	}
	data, _ := json.Marshal(resp)
	return data
}

// ===== Workspace Handlers =====

func listInstances(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, wsIdle, clusterIdle *idle.Tracker) {
	owner := getOwner(r)
	w.Header().Set("Content-Type", "application/json")
	w.Write(getInstancesJSON(client, owner, wsIdle, clusterIdle))
}

type createRequest struct {
	Type    string `json:"type"`
	Cluster string `json:"cluster,omitempty"`
}

func createInstance(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, wsIdle, clusterIdle *idle.Tracker, broadcastAll func()) {
	owner := getOwner(r)
	ctx := context.Background()

	var req createRequest
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}
	if req.Type == "" {
		req.Type = "vscode"
	}

	// Validate cluster if specified
	if req.Cluster != "" {
		if !k8s.ClusterNameRe.MatchString(req.Cluster) {
			http.Error(w, "invalid cluster name", 400)
			return
		}
		clusters := k8s.ListVClusters(ctx, client, owner)
		found := false
		for _, c := range clusters {
			if c.Name == req.Cluster && c.Status == "running" {
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "cluster not found or not running", 404)
			return
		}
	}

	info, err := k8s.CreateWorkspace(ctx, client, owner, req.Type, req.Cluster)
	if err != nil {
		log.Printf("create workspace: %v", err)
		http.Error(w, "create failed: "+err.Error(), 500)
		return
	}

	wsIdle.Touch(info.Name)
	go broadcastAll()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(info)
}

func stopInstance(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, name string, wsIdle *idle.Tracker, broadcastAll func()) {
	owner := getOwner(r)
	ctx := context.Background()

	if wsOwner := k8s.GetWorkspaceOwner(ctx, client, name); wsOwner != owner {
		http.Error(w, "forbidden", 403)
		return
	}

	if err := k8s.StopWorkspace(ctx, client, name); err != nil {
		log.Printf("stop workspace: %v", err)
		http.Error(w, "stop failed: "+err.Error(), 500)
		return
	}

	go broadcastAll()
	w.WriteHeader(204)
}

func startInstance(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, name string, wsIdle *idle.Tracker, broadcastAll func()) {
	owner := getOwner(r)
	ctx := context.Background()

	if wsOwner := k8s.GetWorkspaceOwner(ctx, client, name); wsOwner != owner {
		http.Error(w, "forbidden", 403)
		return
	}

	if err := k8s.StartWorkspace(ctx, client, name); err != nil {
		log.Printf("start workspace: %v", err)
		http.Error(w, "start failed: "+err.Error(), 500)
		return
	}

	wsIdle.Touch(name)
	go broadcastAll()
	w.WriteHeader(204)
}

func deleteInstance(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, name string, wsIdle *idle.Tracker, broadcastAll func()) {
	owner := getOwner(r)
	ctx := context.Background()

	if wsOwner := k8s.GetWorkspaceOwner(ctx, client, name); wsOwner != owner {
		http.Error(w, "forbidden", 403)
		return
	}

	if err := k8s.DeleteWorkspace(ctx, client, name); err != nil {
		log.Printf("delete workspace: %v", err)
		http.Error(w, "delete failed: "+err.Error(), 500)
		return
	}

	wsIdle.Remove(name)
	go broadcastAll()
	w.WriteHeader(204)
}

// ===== Cluster Handlers =====

func handleListClusters(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, clusterIdle *idle.Tracker) {
	owner := getOwner(r)
	ctx := context.Background()
	clusters := k8s.ListVClusters(ctx, client, owner)

	for i := range clusters {
		if ts, ok := clusterIdle.Get(clusters[i].Name); ok {
			unix := ts.UnixMilli()
			clusters[i].LastStart = &unix
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(clusters)
}

func handleCreateCluster(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, clusterIdle *idle.Tracker, broadcastAll func()) {
	owner := getOwner(r)
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", 400)
		return
	}

	if !k8s.ClusterNameRe.MatchString(req.Name) {
		http.Error(w, "invalid cluster name: must match ^[a-z][a-z0-9-]{0,19}$", 400)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := k8s.CreateVCluster(ctx, client, req.Name, owner); err != nil {
		log.Printf("create vcluster: %v", err)
		http.Error(w, "cluster create failed: "+err.Error(), 500)
		return
	}

	clusterIdle.Touch(req.Name)
	log.Printf("created vcluster: %s (owner: %s)", req.Name, owner)
	go broadcastAll()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(map[string]string{"name": req.Name, "status": "running"})
}

func handleStartCluster(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, name string, clusterIdle *idle.Tracker, broadcastAll func()) {
	if !k8s.ClusterNameRe.MatchString(name) {
		http.Error(w, "invalid cluster name", 400)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := k8s.ResumeVCluster(ctx, client, name); err != nil {
		log.Printf("resume vcluster: %v", err)
		http.Error(w, "cluster start failed: "+err.Error(), 500)
		return
	}

	clusterIdle.Touch(name)
	go broadcastAll()
	w.WriteHeader(204)
}

func handleStopCluster(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, name string, broadcastAll func()) {
	if !k8s.ClusterNameRe.MatchString(name) {
		http.Error(w, "invalid cluster name", 400)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := k8s.PauseVCluster(ctx, name); err != nil {
		log.Printf("pause vcluster: %v", err)
		http.Error(w, "cluster stop failed: "+err.Error(), 500)
		return
	}

	go broadcastAll()
	w.WriteHeader(204)
}

func handleExtendCluster(w http.ResponseWriter, r *http.Request, clusterIdle *idle.Tracker, broadcastAll func()) {
	name := strings.TrimPrefix(r.URL.Path, "/api/clusters/")
	name = strings.TrimSuffix(name, "/extend")
	if !k8s.ClusterNameRe.MatchString(name) {
		http.Error(w, "invalid cluster name", 400)
		return
	}

	clusterIdle.Touch(name)
	go broadcastAll()
	w.WriteHeader(204)
}

func handleDeleteCluster(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, name string, broadcastAll func()) {
	if !k8s.ClusterNameRe.MatchString(name) {
		http.Error(w, "invalid cluster name", 400)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := k8s.DeleteVCluster(ctx, client, name); err != nil {
		log.Printf("delete vcluster: %v", err)
		http.Error(w, "cluster delete failed: "+err.Error(), 500)
		return
	}

	go broadcastAll()
	w.WriteHeader(204)
}

func handleLaunchTerminal(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, clusterName string, clusterIdle *idle.Tracker, broadcastAll func()) {
	owner := getOwner(r)
	ctx := context.Background()

	if !k8s.ClusterNameRe.MatchString(clusterName) {
		http.Error(w, "invalid cluster name", 400)
		return
	}

	// Verify cluster is running
	clusters := k8s.ListVClusters(ctx, client, owner)
	found := false
	for _, c := range clusters {
		if c.Name == clusterName && c.Status == "running" {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "cluster not found or not running", 404)
		return
	}

	url, err := k8s.CreateTerminal(ctx, client, clusterName, owner)
	if err != nil {
		log.Printf("create terminal: %v", err)
		http.Error(w, "terminal launch failed: "+err.Error(), 500)
		return
	}

	go broadcastAll()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(map[string]string{"url": url})
}

func handleRemoveTerminal(w http.ResponseWriter, r *http.Request, client *kubernetes.Clientset, clusterName string, broadcastAll func()) {
	ctx := context.Background()
	k8s.DeleteTerminal(ctx, client, clusterName)
	go broadcastAll()
	w.WriteHeader(204)
}

// ===== Background Goroutines =====

func seedIdleTrackers(client *kubernetes.Clientset, wsIdle, clusterIdle *idle.Tracker) {
	ctx := context.Background()

	// Seed workspace tracker with all running workspaces
	deploys, err := client.AppsV1().Deployments(k8s.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: k8s.LabelManaged + "=true",
	})
	if err == nil {
		for _, d := range deploys.Items {
			if d.Spec.Replicas != nil && *d.Spec.Replicas > 0 {
				wsIdle.Touch(d.Name)
			}
		}
	}

	// Seed cluster tracker with all running vClusters
	namespaces, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: k8s.LabelVCluster + "=true",
	})
	if err == nil {
		for _, ns := range namespaces.Items {
			name := strings.TrimPrefix(ns.Name, "vc-")
			clusterIdle.Touch(name)
		}
	}
}

func reapIdleWorkspaces(client *kubernetes.Clientset, wsIdle *idle.Tracker, broadcastAll func()) {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()

	for range ticker.C {
		for _, name := range wsIdle.IdleNames(idleTimeout) {
			ctx := context.Background()
			deploy, err := client.AppsV1().Deployments(k8s.Namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				wsIdle.Remove(name)
				continue
			}
			if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas > 0 {
				log.Printf("idle reaper: stopping workspace %s (idle > %v)", name, idleTimeout)
				if err := k8s.StopWorkspace(ctx, client, name); err != nil {
					log.Printf("idle reaper stop error: %v", err)
				}
				broadcastAll()
			}
			wsIdle.Remove(name)
		}
	}
}

func reapIdleClusters(client *kubernetes.Clientset, clusterIdle *idle.Tracker, broadcastAll func()) {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()

	for range ticker.C {
		for _, name := range clusterIdle.IdleNames(clusterIdleTimeout) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			log.Printf("cluster reaper: pausing vcluster %s (idle > %v)", name, clusterIdleTimeout)
			if err := k8s.PauseVCluster(ctx, name); err != nil {
				log.Printf("cluster reaper pause error: %v", err)
			} else {
				broadcastAll()
			}
			cancel()
			clusterIdle.Remove(name)
		}
	}
}
