package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/gorilla/websocket"
)

const (
	labelManaged         = "code-hub.managed"
	labelOwner           = "code-hub.owner"
	labelType            = "code-hub.type"
	labelTerminalManaged  = "code-hub.terminal"
	labelTerminalCluster  = "code-hub.terminal.cluster"
	labelBridgeManaged    = "code-hub.bridge"
	labelBridgeCluster    = "code-hub.bridge.cluster"
	networkName           = "traefik-proxy"
	terminalImage         = "kube-terminal:latest"
	terminalPort          = "7681"
	kubeconfigDir        = "/app/kubeconfigs"
	traefikDynamicDir    = "/app/traefik-dynamic"
	bridgeImage          = "nginx:alpine"
	idleTimeout          = 1 * time.Hour
	reapInterval         = 1 * time.Minute
	ingressPollInterval  = 10 * time.Second
)

var clusterNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)

type clusterInfo struct {
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	Nodes         int      `json:"nodes"`
	K3sVersion    string   `json:"k3sVersion"`
	Network       string   `json:"network"`
	TerminalURL   string   `json:"terminalUrl,omitempty"`
	TerminalState string   `json:"terminalState,omitempty"`
	ExposedApps   []string `json:"exposedApps,omitempty"`
	LastStart     *int64   `json:"lastStart,omitempty"`
}

// idleTracker records the last time each container was accessed
type idleTracker struct {
	mu       sync.Mutex
	lastSeen map[string]time.Time
}

func newIdleTracker() *idleTracker {
	return &idleTracker{lastSeen: make(map[string]time.Time)}
}

func (t *idleTracker) touch(containerName string) {
	t.mu.Lock()
	t.lastSeen[containerName] = time.Now()
	t.mu.Unlock()
}

func (t *idleTracker) remove(containerName string) {
	t.mu.Lock()
	delete(t.lastSeen, containerName)
	t.mu.Unlock()
}

func (t *idleTracker) get(containerName string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ts, ok := t.lastSeen[containerName]
	return ts, ok
}

func (t *idleTracker) idleContainers() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := time.Now().Add(-idleTimeout)
	var idle []string
	for name, last := range t.lastSeen {
		if last.Before(cutoff) {
			idle = append(idle, name)
		}
	}
	return idle
}

type workspaceType struct {
	Image   string
	Port    string
	Env     []string
	Prefix  string
	BuildIt bool // true = local image, don't pull from registry
}

var workspaceTypes = map[string]workspaceType{
	"vscode": {
		Image:  "lscr.io/linuxserver/code-server:latest",
		Port:   "8443",
		Prefix: "code",
		Env: []string{
			"PUID=1000",
			"PGID=1000",
			"TZ=America/New_York",
			"DEFAULT_WORKSPACE=/config/workspace",
		},
	},
	"ai-code": {
		Image:   "claude-code-web:latest",
		Port:    "3000",
		Prefix:  "ai",
		BuildIt: true,
		Env: []string{
			"TZ=America/New_York",
		},
	},
}

var (
	listenAddr         = env("LISTEN", ":8080")
	staticDir          = env("STATIC_DIR", "/app")
	clusterIdleTimeout = envDuration("CLUSTER_IDLE_TIMEOUT", 8*time.Hour)
	emailRe    = regexp.MustCompile(`[^a-z0-9]`)
)

func env(key, fallback string) string {
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

// hub tracks all WebSocket connections grouped by owner
type hub struct {
	mu    sync.Mutex
	conns map[string]map[*websocket.Conn]struct{}
}

func newHub() *hub {
	return &hub{conns: make(map[string]map[*websocket.Conn]struct{})}
}

func (h *hub) add(owner string, conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[owner] == nil {
		h.conns[owner] = make(map[*websocket.Conn]struct{})
	}
	h.conns[owner][conn] = struct{}{}
}

func (h *hub) remove(owner string, conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set, ok := h.conns[owner]; ok {
		delete(set, conn)
		if len(set) == 0 {
			delete(h.conns, owner)
		}
	}
}

func (h *hub) broadcast(owner string, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for conn := range h.conns[owner] {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			conn.Close()
			delete(h.conns[owner], conn)
		}
	}
}

func (h *hub) broadcastAll(cli *client.Client, idle *idleTracker, clusterIdle *idleTracker) {
	h.mu.Lock()
	owners := make([]string, 0, len(h.conns))
	for owner := range h.conns {
		owners = append(owners, owner)
	}
	h.mu.Unlock()

	for _, owner := range owners {
		data := getInstancesJSON(cli, owner, idle, clusterIdle)
		h.broadcast(owner, data)
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("docker client: %v", err)
	}

	wsHub := newHub()
	idle := newIdleTracker()
	clusterIdle := newIdleTracker()

	go watchDockerEvents(cli, wsHub, idle, clusterIdle)
	go reapIdleContainers(cli, idle, wsHub, clusterIdle)
	go reapIdleClusters(cli, clusterIdle, wsHub, idle)
	go watchIngresses(cli, idle, clusterIdle, wsHub)

	mux := http.NewServeMux()

	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWS(w, r, cli, wsHub, idle, clusterIdle)
	})

	mux.HandleFunc("/api/instances", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listInstances(w, r, cli, idle, clusterIdle)
		case http.MethodPost:
			createInstance(w, r, cli, wsHub, idle, clusterIdle)
		default:
			http.Error(w, "method not allowed", 405)
		}
	})
	mux.HandleFunc("/api/instances/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/instances/")
		parts := strings.SplitN(path, "/", 2)
		id := parts[0]
		if id == "" {
			http.Error(w, "missing id", 400)
			return
		}

		if len(parts) == 2 {
			switch {
			case parts[1] == "status" && r.Method == http.MethodGet:
				getStatus(w, r, cli, id)
			case parts[1] == "stop" && r.Method == http.MethodPost:
				stopInstance(w, r, cli, id)
			case parts[1] == "start" && r.Method == http.MethodPost:
				startInstance(w, r, cli, id, idle)
			default:
				http.Error(w, "not found", 404)
			}
			return
		}

		if r.Method == http.MethodDelete {
			deleteInstance(w, r, cli, id, wsHub, idle)
			return
		}

		http.Error(w, "not found", 404)
	})

	mux.HandleFunc("/api/clusters", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListClusters(w, r, cli, clusterIdle)
		case http.MethodPost:
			handleCreateCluster(w, r, cli, wsHub, idle, clusterIdle)
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
				handleStartCluster(w, r, cli, wsHub, idle, clusterIdle)
			case parts[1] == "stop" && r.Method == http.MethodPost:
				handleStopCluster(w, r, cli, wsHub, idle)
			case parts[1] == "extend" && r.Method == http.MethodPost:
				handleExtendCluster(w, r, clusterIdle, wsHub, cli, idle)
			case parts[1] == "terminal" && r.Method == http.MethodPost:
				handleLaunchTerminal(w, r, cli, name, wsHub, idle, clusterIdle)
			case parts[1] == "terminal" && r.Method == http.MethodDelete:
				handleRemoveTerminal(w, r, cli, name, wsHub, idle)
			default:
				http.Error(w, "not found", 404)
			}
			return
		}

		if r.Method == http.MethodDelete {
			handleDeleteCluster(w, r, cli, name, wsHub, idle)
			return
		}

		http.Error(w, "not found", 404)
	})

	mux.HandleFunc("/api/auth", func(w http.ResponseWriter, r *http.Request) {
		handleAuth(w, r, cli, idle)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Clean(r.URL.Path)
		if p == "/" || p == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
			return
		}
		file := filepath.Join(staticDir, p)
		if _, err := os.Stat(file); err != nil {
			// SPA fallback: serve index.html for unknown paths
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

func handleWS(w http.ResponseWriter, r *http.Request, cli *client.Client, wsHub *hub, idle *idleTracker, clusterIdle *idleTracker) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}

	owner := getOwner(r)
	wsHub.add(owner, conn)
	log.Printf("ws connected: %s", owner)

	data := getInstancesJSON(cli, owner, idle, clusterIdle)
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

	wsHub.remove(owner, conn)
	conn.Close()
	log.Printf("ws disconnected: %s", owner)
}

func watchDockerEvents(cli *client.Client, wsHub *hub, idle *idleTracker, clusterIdle *idleTracker) {
	for {
		ctx := context.Background()

		// Watch managed containers, k3d containers, and headlamp containers
		f := filters.NewArgs()
		f.Add("type", string(events.ContainerEventType))

		msgCh, errCh := cli.Events(ctx, events.ListOptions{Filters: f})

		for {
			select {
			case msg := <-msgCh:
				// Only broadcast for relevant containers
				labels := msg.Actor.Attributes
				isManaged := labels[labelManaged] == "true"
				isK3d := labels["app"] == "k3d"
				isTerminal := labels[labelTerminalManaged] == "true"
				isBridge := labels[labelBridgeManaged] == "true"
				if !isManaged && !isK3d && !isTerminal && !isBridge {
					continue
				}
				switch msg.Action {
				case "start", "stop", "die", "destroy", "kill":
					time.Sleep(200 * time.Millisecond)
					wsHub.broadcastAll(cli, idle, clusterIdle)
				}
			case err := <-errCh:
				if err != nil {
					log.Printf("docker events error: %v (reconnecting in 5s)", err)
				}
				time.Sleep(5 * time.Second)
				goto reconnect
			}
		}
	reconnect:
	}
}

type instanceInfo struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	State      string  `json:"state"`
	Status     string  `json:"status"`
	URL        string  `json:"url"`
	Owner      string  `json:"owner"`
	Cluster    string  `json:"cluster,omitempty"`
	LastAccess *int64  `json:"lastAccess,omitempty"`
}

type typeStats struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Stopped int `json:"stopped"`
}

type globalStats struct {
	Total   int                  `json:"total"`
	Running int                  `json:"running"`
	Stopped int                  `json:"stopped"`
	ByType  map[string]typeStats `json:"byType"`
}

type listResponse struct {
	Instances          []instanceInfo `json:"instances"`
	Global             globalStats    `json:"global"`
	Clusters           []clusterInfo  `json:"clusters"`
	ClusterIdleTimeout int64          `json:"clusterIdleTimeout"`
}

func getOwner(r *http.Request) string {
	email := r.Header.Get("Cf-Access-Authenticated-User-Email")
	if email == "" {
		email = "local"
	}
	return strings.ToLower(email)
}

func sanitizeEmail(email string) string {
	prefix := strings.SplitN(email, "@", 2)[0]
	return emailRe.ReplaceAllString(strings.ToLower(prefix), "")
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func getInstancesForOwner(cli *client.Client, owner string, idle *idleTracker) []instanceInfo {
	ctx := context.Background()
	f := filters.NewArgs()
	f.Add("label", labelManaged+"=true")

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		log.Printf("list error: %v", err)
		return []instanceInfo{}
	}

	var result []instanceInfo
	for _, c := range containers {
		if c.Labels[labelOwner] != owner {
			continue
		}
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		wsType := c.Labels[labelType]
		if wsType == "" {
			wsType = "vscode"
		}
		info := instanceInfo{
			ID:      c.ID[:12],
			Name:    name,
			Type:    wsType,
			State:   c.State,
			Status:  c.Status,
			URL:     "https://" + name + ".arlint.dev",
			Owner:   owner,
			Cluster: c.Labels["code-hub.cluster"],
		}
		if ts, ok := idle.get(name); ok {
			unix := ts.UnixMilli()
			info.LastAccess = &unix
		}
		result = append(result, info)
	}

	if result == nil {
		result = []instanceInfo{}
	}
	return result
}

func getGlobalStats(cli *client.Client) globalStats {
	ctx := context.Background()
	f := filters.NewArgs()
	f.Add("label", labelManaged+"=true")

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		return globalStats{ByType: map[string]typeStats{}}
	}

	s := globalStats{ByType: map[string]typeStats{}}
	s.Total = len(containers)
	for _, c := range containers {
		running := c.State == "running"
		if running {
			s.Running++
		}

		t := c.Labels[labelType]
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

func getInstancesJSON(cli *client.Client, owner string, idle *idleTracker, clusterIdle *idleTracker) []byte {
	resp := listResponse{
		Instances:          getInstancesForOwner(cli, owner, idle),
		Global:             getGlobalStats(cli),
		Clusters:           getClusters(cli, owner, clusterIdle),
		ClusterIdleTimeout: clusterIdleTimeout.Milliseconds(),
	}
	data, _ := json.Marshal(resp)
	return data
}

func listInstances(w http.ResponseWriter, r *http.Request, cli *client.Client, idle *idleTracker, clusterIdle *idleTracker) {
	owner := getOwner(r)
	resp := listResponse{
		Instances:          getInstancesForOwner(cli, owner, idle),
		Global:             getGlobalStats(cli),
		Clusters:           getClusters(cli, owner, clusterIdle),
		ClusterIdleTimeout: clusterIdleTimeout.Milliseconds(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type createRequest struct {
	Type    string `json:"type"`
	Cluster string `json:"cluster,omitempty"`
}

func createInstance(w http.ResponseWriter, r *http.Request, cli *client.Client, wsHub *hub, idle *idleTracker, clusterIdle *idleTracker) {
	owner := getOwner(r)
	ctx := context.Background()

	var req createRequest
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}
	if req.Type == "" {
		req.Type = "vscode"
	}

	wt, ok := workspaceTypes[req.Type]
	if !ok {
		http.Error(w, "unknown workspace type: "+req.Type, 400)
		return
	}

	// Validate and prepare cluster connection if requested
	var clusterName string
	if req.Cluster != "" {
		if !clusterNameRe.MatchString(req.Cluster) {
			http.Error(w, "invalid cluster name", 400)
			return
		}
		// Verify cluster exists and is running
		allClusters := getClusters(cli, owner, clusterIdle)
		found := false
		for _, c := range allClusters {
			if c.Name == req.Cluster {
				if c.Status != "running" {
					http.Error(w, "cluster is not running", 400)
					return
				}
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "cluster not found", 404)
			return
		}
		// Extract kubeconfig so we can mount it
		if err := extractKubeconfig(cli, req.Cluster); err != nil {
			log.Printf("kubeconfig extraction error for workspace: %v", err)
			http.Error(w, "failed to extract kubeconfig: "+err.Error(), 500)
			return
		}
		clusterName = req.Cluster
	}

	prefix := sanitizeEmail(owner)
	if prefix == "" {
		prefix = "user"
	}
	name := wt.Prefix + "-" + prefix + "-" + randHex(2)

	labels := map[string]string{
		labelManaged: "true",
		labelOwner:   owner,
		labelType:    req.Type,
		"traefik.enable":         "true",
		"traefik.docker.network": networkName,
		"traefik.http.routers." + name + ".rule":                          "Host(`" + name + ".arlint.dev`)",
		"traefik.http.routers." + name + ".middlewares":                   name + "-auth",
		"traefik.http.middlewares." + name + "-auth.forwardauth.address":  "http://code:8080/api/auth",
		"traefik.http.middlewares." + name + "-auth.forwardauth.authResponseHeaders": "Cf-Access-Authenticated-User-Email",
		"traefik.http.services." + name + ".loadbalancer.server.port":    wt.Port,
	}

	if clusterName != "" {
		labels["code-hub.cluster"] = clusterName
	}

	// Pull image if it's not a locally-built one
	if !wt.BuildIt {
		pull, err := cli.ImagePull(ctx, wt.Image, image.PullOptions{})
		if err != nil {
			log.Printf("image pull error: %v", err)
			http.Error(w, "image pull failed: "+err.Error(), 500)
			return
		}
		io.Copy(io.Discard, pull)
		pull.Close()
	}

	volumeName := name + "-data"
	var binds []string
	switch req.Type {
	case "vscode":
		binds = []string{volumeName + ":/config"}
	case "ai-code":
		binds = []string{volumeName + ":/home/node"}
	}

	envVars := append([]string{}, wt.Env...)

	// Mount kubeconfig and kubectl into workspace if cluster is specified
	if clusterName != "" {
		binds = append(binds,
			"kubeconfigs:/home/.kube:ro",
			"kube-tools:/usr/local/kube-tools:ro",
		)
		envVars = append(envVars,
			"KUBECONFIG=/home/.kube/"+clusterName+".yaml",
			"PATH=/usr/local/kube-tools:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		)
	}

	cfg := &container.Config{
		Image:  wt.Image,
		Env:    envVars,
		Labels: labels,
	}

	hostCfg := &container.HostConfig{
		Binds:         binds,
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}

	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: {},
		},
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
	if err != nil {
		log.Printf("create error: %v", err)
		http.Error(w, "create failed: "+err.Error(), 500)
		return
	}

	// Connect workspace to k3d network for API server access
	if clusterName != "" {
		k3dNetwork := "k3d-" + clusterName
		if err := cli.NetworkConnect(ctx, k3dNetwork, resp.ID, nil); err != nil {
			log.Printf("warning: failed to connect workspace to k3d network %s: %v", k3dNetwork, err)
		}
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		log.Printf("start error: %v", err)
		http.Error(w, "start failed: "+err.Error(), 500)
		return
	}

	idle.touch(name)

	info := instanceInfo{
		ID:    resp.ID[:12],
		Name:  name,
		Type:  req.Type,
		State: "running",
		URL:   "https://" + name + ".arlint.dev",
		Owner: owner,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(info)
}

func deleteInstance(w http.ResponseWriter, r *http.Request, cli *client.Client, id string, wsHub *hub, idle *idleTracker) {
	owner := getOwner(r)
	ctx := context.Background()

	inspect, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}

	if inspect.Config.Labels[labelOwner] != owner {
		http.Error(w, "forbidden", 403)
		return
	}

	name := strings.TrimPrefix(inspect.Name, "/")
	cli.ContainerStop(ctx, id, container.StopOptions{})
	if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
		log.Printf("remove error: %v", err)
		http.Error(w, "remove failed", 500)
		return
	}

	// Remove the data volume
	volumeName := name + "-data"
	if err := cli.VolumeRemove(ctx, volumeName, true); err != nil {
		log.Printf("volume remove warning: %s: %v", volumeName, err)
	}

	idle.remove(name)
	w.WriteHeader(204)
}

func stopInstance(w http.ResponseWriter, r *http.Request, cli *client.Client, id string) {
	owner := getOwner(r)
	ctx := context.Background()

	inspect, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}

	if inspect.Config.Labels[labelOwner] != owner {
		http.Error(w, "forbidden", 403)
		return
	}

	if err := cli.ContainerStop(ctx, id, container.StopOptions{}); err != nil {
		log.Printf("stop error: %v", err)
		http.Error(w, "stop failed: "+err.Error(), 500)
		return
	}

	w.WriteHeader(204)
}

func startInstance(w http.ResponseWriter, r *http.Request, cli *client.Client, id string, idle *idleTracker) {
	owner := getOwner(r)
	ctx := context.Background()

	inspect, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}

	if inspect.Config.Labels[labelOwner] != owner {
		http.Error(w, "forbidden", 403)
		return
	}

	if err := cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		log.Printf("start error: %v", err)
		http.Error(w, "start failed: "+err.Error(), 500)
		return
	}

	name := strings.TrimPrefix(inspect.Name, "/")
	idle.touch(name)

	w.WriteHeader(204)
}

func handleAuth(w http.ResponseWriter, r *http.Request, cli *client.Client, idle *idleTracker) {
	// Traefik forwardAuth sends the original request's host in X-Forwarded-Host
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		http.Error(w, "missing forwarded host", 400)
		return
	}

	// Extract container name from hostname (e.g. "code-austin-a1b2.arlint.dev" → "code-austin-a1b2")
	containerName := strings.SplitN(host, ".", 2)[0]

	ctx := context.Background()

	// Check if this is a bridge-routed request (e.g. "myapp-dev.arlint.dev")
	// Bridge containers are named "bridge-{cluster}", so look up by pattern
	if isBridgeHost(containerName) {
		handleBridgeAuth(w, r, cli, containerName)
		return
	}

	inspect, err := cli.ContainerInspect(ctx, containerName)
	if err != nil {
		http.Error(w, "forbidden", 403)
		return
	}

	// Verify it's a code-hub managed, headlamp, or terminal container
	isManaged := inspect.Config.Labels[labelManaged] == "true"
	isTerminal := inspect.Config.Labels[labelTerminalManaged] == "true"
	if !isManaged && !isTerminal {
		http.Error(w, "forbidden", 403)
		return
	}

	// Check that the requesting user owns this container
	owner := strings.ToLower(r.Header.Get("Cf-Access-Authenticated-User-Email"))
	if owner == "" || inspect.Config.Labels[labelOwner] != owner {
		log.Printf("auth denied: user=%q owner=%q container=%s", owner, inspect.Config.Labels[labelOwner], containerName)
		http.Error(w, "forbidden", 403)
		return
	}

	// Record activity for idle tracking (skip for terminal containers)
	if isManaged {
		idle.touch(containerName)
	}

	w.WriteHeader(200)
}

// isBridgeHost checks if a hostname corresponds to a bridge-routed ingress.
// Bridge routes follow the pattern {app}-{cluster}.arlint.dev, and we detect
// them by checking if any bridge-{cluster} container exists for a suffix.
func isBridgeHost(hostname string) bool {
	// We check by looking for bridge config files on disk — if a traefik config
	// exists for a cluster whose name is a suffix of this hostname, it's a bridge route.
	entries, err := os.ReadDir(traefikDynamicDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "bridge-") || !strings.HasSuffix(name, ".yml") {
			continue
		}
		cluster := strings.TrimPrefix(strings.TrimSuffix(name, ".yml"), "bridge-")
		if strings.HasSuffix(hostname, "-"+cluster) {
			return true
		}
	}
	return false
}

func handleBridgeAuth(w http.ResponseWriter, r *http.Request, cli *client.Client, hostname string) {
	// Find which cluster this hostname belongs to by checking bridge containers
	ctx := context.Background()
	f := filters.NewArgs()
	f.Add("label", labelBridgeManaged+"=true")

	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		http.Error(w, "forbidden", 403)
		return
	}

	var bridgeOwner string
	for _, c := range containers {
		cluster := c.Labels[labelBridgeCluster]
		if cluster != "" && strings.HasSuffix(hostname, "-"+cluster) {
			bridgeOwner = c.Labels[labelOwner]
			break
		}
	}

	if bridgeOwner == "" {
		http.Error(w, "forbidden", 403)
		return
	}

	owner := strings.ToLower(r.Header.Get("Cf-Access-Authenticated-User-Email"))
	if owner == "" || owner != bridgeOwner {
		log.Printf("bridge auth denied: user=%q owner=%q host=%s", owner, bridgeOwner, hostname)
		http.Error(w, "forbidden", 403)
		return
	}

	w.WriteHeader(200)
}

func reapIdleContainers(cli *client.Client, idle *idleTracker, wsHub *hub, clusterIdle *idleTracker) {
	// Seed tracker: mark all currently running managed containers as "just seen"
	// so they get a full hour from process start before being reaped
	ctx := context.Background()
	f := filters.NewArgs()
	f.Add("label", labelManaged+"=true")
	f.Add("status", "running")
	if containers, err := cli.ContainerList(ctx, container.ListOptions{Filters: f}); err == nil {
		for _, c := range containers {
			name := ""
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			if name != "" {
				idle.touch(name)
			}
		}
	}

	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for range ticker.C {
		for _, name := range idle.idleContainers() {
			inspect, err := cli.ContainerInspect(context.Background(), name)
			if err != nil {
				idle.remove(name)
				continue
			}
			// Only stop running containers
			if inspect.State.Running {
				log.Printf("idle reaper: stopping %s (idle > %v)", name, idleTimeout)
				cli.ContainerStop(context.Background(), name, container.StopOptions{})
			}
			idle.remove(name)
		}
	}
}

func reapIdleClusters(cli *client.Client, clusterIdle *idleTracker, wsHub *hub, idle *idleTracker) {
	// Seed: mark all currently running k3d clusters as "just seen"
	ctx := context.Background()
	f := filters.NewArgs()
	f.Add("label", "app=k3d")
	f.Add("status", "running")
	if containers, err := cli.ContainerList(ctx, container.ListOptions{Filters: f}); err == nil {
		seen := make(map[string]bool)
		for _, c := range containers {
			clName := c.Labels["k3d.cluster"]
			if clName != "" && !seen[clName] {
				clusterIdle.touch(clName)
				seen[clName] = true
			}
		}
	}

	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-clusterIdleTimeout)
		clusterIdle.mu.Lock()
		var stale []string
		for name, last := range clusterIdle.lastSeen {
			if last.Before(cutoff) {
				stale = append(stale, name)
			}
		}
		clusterIdle.mu.Unlock()

		for _, name := range stale {
			// Check if cluster is still running before stopping
			cf := filters.NewArgs()
			cf.Add("label", "app=k3d")
			cf.Add("label", "k3d.cluster="+name)
			cf.Add("status", "running")
			running, err := cli.ContainerList(context.Background(), container.ListOptions{Filters: cf})
			if err != nil || len(running) == 0 {
				clusterIdle.remove(name)
				continue
			}

			log.Printf("cluster reaper: stopping cluster %s (idle > %v)", name, clusterIdleTimeout)
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			cmd := exec.CommandContext(stopCtx, "k3d", "cluster", "stop", name)
			if output, err := cmd.CombinedOutput(); err != nil {
				log.Printf("cluster reaper: failed to stop %s: %v: %s", name, err, string(output))
			} else {
				log.Printf("cluster reaper: stopped cluster %s", name)
			}
			cancel()
			clusterIdle.remove(name)
			wsHub.broadcastAll(cli, idle, clusterIdle)
		}
	}
}

func getStatus(w http.ResponseWriter, r *http.Request, cli *client.Client, id string) {
	owner := getOwner(r)
	ctx := context.Background()

	inspect, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}

	if inspect.Config.Labels[labelOwner] != owner {
		http.Error(w, "forbidden", 403)
		return
	}

	name := strings.TrimPrefix(inspect.Name, "/")
	wsType := inspect.Config.Labels[labelType]
	if wsType == "" {
		wsType = "vscode"
	}

	info := instanceInfo{
		ID:     inspect.ID[:12],
		Name:   name,
		Type:   wsType,
		State:  inspect.State.Status,
		Status: inspect.State.Status,
		URL:    "https://" + name + ".arlint.dev",
		Owner:  owner,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// ===== CLUSTER MANAGEMENT =====

func getClusters(cli *client.Client, owner string, clusterIdle *idleTracker) []clusterInfo {
	ctx := context.Background()
	f := filters.NewArgs()
	f.Add("label", "app=k3d")

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		log.Printf("cluster list error: %v", err)
		return []clusterInfo{}
	}

	type clusterData struct {
		nodes      int
		running    int
		k3sVersion string
		network    string
	}

	clusterMap := make(map[string]*clusterData)
	for _, c := range containers {
		clName := c.Labels["k3d.cluster"]
		if clName == "" {
			continue
		}
		// Filter by owner — only show clusters belonging to this user
		if clOwner := c.Labels[labelOwner]; clOwner != "" && clOwner != owner {
			continue
		}
		cd, ok := clusterMap[clName]
		if !ok {
			cd = &clusterData{network: "k3d-" + clName}
			clusterMap[clName] = cd
		}
		if c.Labels["k3d.role"] == "loadbalancer" {
			continue
		}
		cd.nodes++
		if c.State == "running" {
			cd.running++
		}
		if v := c.Labels["k3d.cluster.image.k3s"]; v != "" && cd.k3sVersion == "" {
			cd.k3sVersion = v
		}
	}

	// Find terminal containers
	tf := filters.NewArgs()
	tf.Add("label", labelTerminalManaged+"=true")
	terminals, _ := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: tf})

	terminalMap := make(map[string]string) // cluster -> state
	for _, t := range terminals {
		cl := t.Labels[labelTerminalCluster]
		if cl == "" {
			continue
		}
		if tOwner := t.Labels[labelOwner]; tOwner != "" && tOwner != owner {
			continue
		}
		terminalMap[cl] = t.State
	}

	var result []clusterInfo
	for name, cd := range clusterMap {
		status := "stopped"
		if cd.running == cd.nodes {
			status = "running"
		} else if cd.running > 0 {
			status = "partial"
		}

		ci := clusterInfo{
			Name:       name,
			Status:     status,
			Nodes:      cd.nodes,
			K3sVersion: cd.k3sVersion,
			Network:    cd.network,
		}

		if tState, ok := terminalMap[name]; ok {
			ci.TerminalURL = "https://terminal-" + name + ".arlint.dev"
			ci.TerminalState = tState
		}

		ci.ExposedApps = getExposedApps(name)

		if ts, ok := clusterIdle.get(name); ok {
			unix := ts.UnixMilli()
			ci.LastStart = &unix
		}

		result = append(result, ci)
	}

	if result == nil {
		result = []clusterInfo{}
	}
	return result
}

func handleListClusters(w http.ResponseWriter, r *http.Request, cli *client.Client, clusterIdle *idleTracker) {
	owner := getOwner(r)
	clusters := getClusters(cli, owner, clusterIdle)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(clusters)
}

func handleCreateCluster(w http.ResponseWriter, r *http.Request, cli *client.Client, wsHub *hub, idle *idleTracker, clusterIdle *idleTracker) {
	owner := getOwner(r)
	var req struct {
		Name    string `json:"name"`
		Servers int    `json:"servers"`
		Agents  int    `json:"agents"`
		Image   string `json:"image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", 400)
		return
	}

	if !clusterNameRe.MatchString(req.Name) {
		http.Error(w, "invalid cluster name: must match ^[a-z][a-z0-9-]{0,19}$", 400)
		return
	}

	if req.Servers < 1 {
		req.Servers = 1
	} else if req.Servers > 5 {
		req.Servers = 5
	}
	if req.Agents < 0 {
		req.Agents = 0
	} else if req.Agents > 10 {
		req.Agents = 10
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Escape literal '@' in owner email so k3d doesn't treat it as a node filter separator
	escapedOwner := strings.ReplaceAll(owner, "@", "\\@")
	ownerLabel := labelOwner + "=" + escapedOwner
	args := []string{
		"cluster", "create", req.Name,
		"--servers", fmt.Sprintf("%d", req.Servers),
		"--agents", fmt.Sprintf("%d", req.Agents),
		"--runtime-label", ownerLabel + "@server:*",
		"--runtime-label", ownerLabel + "@agent:*",
		"--runtime-label", ownerLabel + "@loadbalancer:*",
	}

	if req.Image != "" {
		args = append(args, "--image", req.Image)
	}

	// Always disable k3s built-in traefik — the host Traefik + nginx ingress controller handle routing
	args = append(args, "--k3s-arg", "--disable=traefik@server:*")

	cmd := exec.CommandContext(ctx, "k3d", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("k3d create error: %v: %s", err, string(output))
		http.Error(w, fmt.Sprintf("cluster create failed: %s", string(output)), 500)
		return
	}

	log.Printf("created k3d cluster: %s (owner: %s, servers: %d, agents: %d)", req.Name, owner, req.Servers, req.Agents)
	clusterIdle.touch(req.Name)

	// Extract kubeconfig and deploy ingress controller in background
	go func() {
		ensureOnK3dNetwork(cli, req.Name)
		if err := extractKubeconfig(cli, req.Name); err != nil {
			log.Printf("post-create kubeconfig extraction error for %s: %v", req.Name, err)
			return
		}
		if err := deployIngressController(req.Name); err != nil {
			log.Printf("ingress controller deploy error for %s: %v", req.Name, err)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(map[string]string{"name": req.Name, "status": "running"})
}

func handleStartCluster(w http.ResponseWriter, r *http.Request, cli *client.Client, wsHub *hub, idle *idleTracker, clusterIdle *idleTracker) {
	name := strings.TrimPrefix(r.URL.Path, "/api/clusters/")
	name = strings.TrimSuffix(name, "/start")
	if !clusterNameRe.MatchString(name) {
		http.Error(w, "invalid cluster name", 400)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "k3d", "cluster", "start", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("k3d start error: %v: %s", err, string(output))
		http.Error(w, fmt.Sprintf("cluster start failed: %s", string(output)), 500)
		return
	}

	clusterIdle.touch(name)
	log.Printf("started k3d cluster: %s", name)
	w.WriteHeader(204)
}

func handleStopCluster(w http.ResponseWriter, r *http.Request, cli *client.Client, wsHub *hub, idle *idleTracker) {
	name := strings.TrimPrefix(r.URL.Path, "/api/clusters/")
	name = strings.TrimSuffix(name, "/stop")
	if !clusterNameRe.MatchString(name) {
		http.Error(w, "invalid cluster name", 400)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "k3d", "cluster", "stop", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("k3d stop error: %v: %s", err, string(output))
		http.Error(w, fmt.Sprintf("cluster stop failed: %s", string(output)), 500)
		return
	}

	log.Printf("stopped k3d cluster: %s", name)
	w.WriteHeader(204)
}

func handleExtendCluster(w http.ResponseWriter, r *http.Request, clusterIdle *idleTracker, wsHub *hub, cli *client.Client, idle *idleTracker) {
	name := strings.TrimPrefix(r.URL.Path, "/api/clusters/")
	name = strings.TrimSuffix(name, "/extend")
	if !clusterNameRe.MatchString(name) {
		http.Error(w, "invalid cluster name", 400)
		return
	}

	clusterIdle.touch(name)
	log.Printf("extended cluster idle timer: %s", name)
	wsHub.broadcastAll(cli, idle, clusterIdle)
	w.WriteHeader(204)
}

func handleDeleteCluster(w http.ResponseWriter, r *http.Request, cli *client.Client, name string, wsHub *hub, idle *idleTracker) {
	if !clusterNameRe.MatchString(name) {
		http.Error(w, "invalid cluster name", 400)
		return
	}

	ctx := context.Background()

	// Remove terminal container if it exists
	terminalName := "terminal-" + name
	if _, err := cli.ContainerInspect(ctx, terminalName); err == nil {
		cli.ContainerStop(ctx, terminalName, container.StopOptions{})
		cli.ContainerRemove(ctx, terminalName, container.RemoveOptions{Force: true})
		log.Printf("removed terminal container for cluster: %s", name)
	}

	// Remove bridge container and traefik config
	removeBridge(cli, name)

	// Disconnect code-hub from k3d network
	disconnectFromK3dNetwork(cli, name)

	// Clean up kubeconfig
	kcPath := filepath.Join(kubeconfigDir, name+".yaml")
	os.Remove(kcPath)

	// Delete the k3d cluster
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "k3d", "cluster", "delete", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("k3d delete error: %v: %s", err, string(output))
		http.Error(w, fmt.Sprintf("cluster delete failed: %s", string(output)), 500)
		return
	}

	log.Printf("deleted k3d cluster: %s", name)
	w.WriteHeader(204)
}

// ensureOnK3dNetwork connects the code-hub container to a k3d cluster network
// so that kubectl commands can reach the API server via Docker DNS.
func ensureOnK3dNetwork(cli *client.Client, clusterName string) {
	ctx := context.Background()
	k3dNetwork := "k3d-" + clusterName
	codeContainer := "code"

	// Check if already connected
	inspect, err := cli.ContainerInspect(ctx, codeContainer)
	if err != nil {
		return
	}
	if _, ok := inspect.NetworkSettings.Networks[k3dNetwork]; ok {
		return
	}

	if err := cli.NetworkConnect(ctx, k3dNetwork, codeContainer, nil); err != nil {
		log.Printf("warning: failed to connect code-hub to %s: %v", k3dNetwork, err)
	} else {
		log.Printf("connected code-hub to network %s", k3dNetwork)
	}
}

// disconnectFromK3dNetwork removes the code-hub container from a k3d cluster network.
func disconnectFromK3dNetwork(cli *client.Client, clusterName string) {
	ctx := context.Background()
	k3dNetwork := "k3d-" + clusterName
	codeContainer := "code"

	if err := cli.NetworkDisconnect(ctx, k3dNetwork, codeContainer, false); err != nil {
		// Ignore errors — network may already be gone
		log.Printf("warning: disconnect from %s: %v", k3dNetwork, err)
	} else {
		log.Printf("disconnected code-hub from network %s", k3dNetwork)
	}
}

func extractKubeconfig(cli *client.Client, clusterName string) error {
	ctx := context.Background()
	serverContainer := "k3d-" + clusterName + "-server-0"

	reader, _, err := cli.CopyFromContainer(ctx, serverContainer, "/output/kubeconfig.yaml")
	if err != nil {
		return fmt.Errorf("copy from container: %w", err)
	}
	defer reader.Close()

	tr := tar.NewReader(reader)
	var content []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if hdr.Name == "kubeconfig.yaml" || strings.HasSuffix(hdr.Name, "/kubeconfig.yaml") {
			content, err = io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("read kubeconfig: %w", err)
			}
			break
		}
	}

	if content == nil {
		return fmt.Errorf("kubeconfig.yaml not found in tar archive")
	}

	// Rewrite server address to use Docker DNS (k3d network)
	// Replace https://0.0.0.0:<port> with https://k3d-<name>-server-0:6443
	rewritten := bytes.ReplaceAll(content,
		[]byte("server: https://0.0.0.0"),
		[]byte("server: https://"+serverContainer))

	// Also handle any localhost references
	rewritten = bytes.ReplaceAll(rewritten,
		[]byte("server: https://127.0.0.1"),
		[]byte("server: https://"+serverContainer))

	// Replace any port with 6443 (the internal API server port)
	// The kubeconfig has the mapped host port, but inside Docker network we use 6443
	lines := bytes.Split(rewritten, []byte("\n"))
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("server: https://"+serverContainer)) {
			// Normalize to port 6443
			lines[i] = []byte(strings.Split(string(line), serverContainer)[0] + serverContainer + ":6443")
		}
	}
	rewritten = bytes.Join(lines, []byte("\n"))

	if err := os.MkdirAll(kubeconfigDir, 0755); err != nil {
		return fmt.Errorf("mkdir kubeconfigs: %w", err)
	}

	kcPath := filepath.Join(kubeconfigDir, clusterName+".yaml")
	if err := os.WriteFile(kcPath, rewritten, 0644); err != nil {
		return fmt.Errorf("write kubeconfig: %w", err)
	}

	log.Printf("extracted kubeconfig for cluster %s to %s", clusterName, kcPath)
	return nil
}

func handleLaunchTerminal(w http.ResponseWriter, r *http.Request, cli *client.Client, clusterName string, wsHub *hub, idle *idleTracker, clusterIdle *idleTracker) {
	owner := getOwner(r)
	ctx := context.Background()

	if !clusterNameRe.MatchString(clusterName) {
		http.Error(w, "invalid cluster name", 400)
		return
	}

	// Verify cluster is running
	clusters := getClusters(cli, owner, clusterIdle)
	var cluster *clusterInfo
	for i := range clusters {
		if clusters[i].Name == clusterName {
			cluster = &clusters[i]
			break
		}
	}
	if cluster == nil {
		http.Error(w, "cluster not found", 404)
		return
	}
	if cluster.Status != "running" {
		http.Error(w, "cluster is not running", 400)
		return
	}

	terminalName := "terminal-" + clusterName
	url := "https://" + terminalName + ".arlint.dev"

	// If terminal container already exists, just start it
	if inspect, err := cli.ContainerInspect(ctx, terminalName); err == nil {
		if !inspect.State.Running {
			if err := cli.ContainerStart(ctx, terminalName, container.StartOptions{}); err != nil {
				log.Printf("terminal start error: %v", err)
				http.Error(w, "failed to start terminal: "+err.Error(), 500)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"url": url})
		return
	}

	// Extract kubeconfig
	if err := extractKubeconfig(cli, clusterName); err != nil {
		log.Printf("kubeconfig extraction error: %v", err)
		http.Error(w, "failed to extract kubeconfig: "+err.Error(), 500)
		return
	}

	labels := map[string]string{
		labelTerminalManaged: "true",
		labelTerminalCluster: clusterName,
		labelOwner:           owner,
		"traefik.enable":         "true",
		"traefik.docker.network": networkName,
		"traefik.http.routers." + terminalName + ".rule":                          "Host(`" + terminalName + ".arlint.dev`)",
		"traefik.http.routers." + terminalName + ".middlewares":                   terminalName + "-auth",
		"traefik.http.middlewares." + terminalName + "-auth.forwardauth.address":  "http://code:8080/api/auth",
		"traefik.http.middlewares." + terminalName + "-auth.forwardauth.authResponseHeaders": "Cf-Access-Authenticated-User-Email",
		"traefik.http.services." + terminalName + ".loadbalancer.server.port":    terminalPort,
	}

	cfg := &container.Config{
		Image:  terminalImage,
		Env:    []string{"KUBECONFIG=/root/.kube/" + clusterName + ".yaml"},
		Labels: labels,
	}

	hostCfg := &container.HostConfig{
		Binds:         []string{"kubeconfigs:/root/.kube:ro"},
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}

	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: {},
		},
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, terminalName)
	if err != nil {
		log.Printf("terminal create error: %v", err)
		http.Error(w, "create failed: "+err.Error(), 500)
		return
	}

	// Connect to the k3d network for API server access
	k3dNetwork := "k3d-" + clusterName
	if err := cli.NetworkConnect(ctx, k3dNetwork, resp.ID, nil); err != nil {
		log.Printf("warning: failed to connect terminal to k3d network %s: %v", k3dNetwork, err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		log.Printf("terminal start error: %v", err)
		http.Error(w, "start failed: "+err.Error(), 500)
		return
	}

	log.Printf("launched terminal for cluster %s: %s", clusterName, url)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(map[string]string{"url": url})
}

func handleRemoveTerminal(w http.ResponseWriter, r *http.Request, cli *client.Client, clusterName string, wsHub *hub, idle *idleTracker) {
	ctx := context.Background()

	terminalName := "terminal-" + clusterName
	if _, err := cli.ContainerInspect(ctx, terminalName); err != nil {
		http.Error(w, "terminal container not found", 404)
		return
	}

	cli.ContainerStop(ctx, terminalName, container.StopOptions{})
	if err := cli.ContainerRemove(ctx, terminalName, container.RemoveOptions{Force: true}); err != nil {
		log.Printf("terminal remove error: %v", err)
		http.Error(w, "remove failed: "+err.Error(), 500)
		return
	}

	log.Printf("removed terminal for cluster: %s", clusterName)
	w.WriteHeader(204)
}

// ===== INGRESS BRIDGE =====

// ensureIngressController checks if the ingress controller is deployed and deploys it if not.
func ensureIngressController(clusterName string) {
	kcPath := filepath.Join(kubeconfigDir, clusterName+".yaml")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kcPath,
		"get", "deployment", "ingress-nginx-controller", "-n", "ingress-nginx",
		"--no-headers")
	if err := cmd.Run(); err == nil {
		return // already deployed
	}

	log.Printf("ingress controller missing in cluster %s, deploying...", clusterName)
	if err := deployIngressController(clusterName); err != nil {
		log.Printf("ingress controller deploy error for %s: %v", clusterName, err)
	}
}

// deployIngressController deploys the nginx ingress controller inside a k3d cluster.
func deployIngressController(clusterName string) error {
	kcPath := filepath.Join(kubeconfigDir, clusterName+".yaml")

	// Wait for the cluster API to be ready (up to 60s)
	for i := 0; i < 12; i++ {
		cmd := exec.Command("kubectl", "--kubeconfig", kcPath, "cluster-info")
		if err := cmd.Run(); err == nil {
			break
		}
		if i == 11 {
			return fmt.Errorf("cluster API not ready after 60s")
		}
		time.Sleep(5 * time.Second)
	}

	manifests := `
apiVersion: v1
kind: Namespace
metadata:
  name: ingress-nginx
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ingress-nginx-controller
  namespace: ingress-nginx
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: ingress-nginx
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ingress-nginx
    spec:
      containers:
      - name: controller
        image: registry.k8s.io/ingress-nginx/controller:v1.12.1
        args:
        - /nginx-ingress-controller
        - --publish-status-address=localhost
        - --election-id=ingress-nginx-leader
        - --controller-class=k8s.io/ingress-nginx
        - --ingress-class=nginx
        - --watch-ingress-without-class=true
        ports:
        - name: http
          containerPort: 80
        - name: https
          containerPort: 443
        env:
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
      serviceAccountName: ingress-nginx
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ingress-nginx
  namespace: ingress-nginx
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ingress-nginx
rules:
- apiGroups: [""]
  resources: ["configmaps","endpoints","nodes","pods","secrets","namespaces"]
  verbs: ["list","watch","get"]
- apiGroups: [""]
  resources: ["services"]
  verbs: ["list","watch","get"]
- apiGroups: ["networking.k8s.io"]
  resources: ["ingresses","ingressclasses"]
  verbs: ["list","watch","get"]
- apiGroups: ["networking.k8s.io"]
  resources: ["ingresses/status"]
  verbs: ["update"]
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["list","watch","get","create","update"]
- apiGroups: [""]
  resources: ["events"]
  verbs: ["create","patch"]
- apiGroups: ["discovery.k8s.io"]
  resources: ["endpointslices"]
  verbs: ["list","watch","get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ingress-nginx
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ingress-nginx
subjects:
- kind: ServiceAccount
  name: ingress-nginx
  namespace: ingress-nginx
---
apiVersion: networking.k8s.io/v1
kind: IngressClass
metadata:
  name: nginx
  annotations:
    ingressclass.kubernetes.io/is-default-class: "true"
spec:
  controller: k8s.io/ingress-nginx
---
apiVersion: v1
kind: Service
metadata:
  name: ingress-nginx-controller
  namespace: ingress-nginx
spec:
  type: NodePort
  ports:
  - name: http
    port: 80
    targetPort: http
    nodePort: 30080
  - name: https
    port: 443
    targetPort: https
    nodePort: 30443
  selector:
    app.kubernetes.io/name: ingress-nginx
`

	cmd := exec.Command("kubectl", "--kubeconfig", kcPath, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifests)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl apply: %s: %w", string(output), err)
	}

	log.Printf("deployed ingress controller in cluster %s", clusterName)
	return nil
}

// launchBridge creates an nginx bridge container that joins both traefik-proxy
// and the k3d cluster network, proxying traffic to the ingress controller NodePort.
func launchBridge(cli *client.Client, clusterName, owner string) error {
	ctx := context.Background()
	bridgeName := "bridge-" + clusterName
	serverName := "k3d-" + clusterName + "-server-0"
	k3dNetwork := "k3d-" + clusterName

	// If it already exists, just make sure it's running
	if inspect, err := cli.ContainerInspect(ctx, bridgeName); err == nil {
		if !inspect.State.Running {
			return cli.ContainerStart(ctx, bridgeName, container.StartOptions{})
		}
		return nil
	}

	// Nginx config to proxy everything to the ingress controller.
	// Rewrites Host header: "myapp-{cluster}.arlint.dev" → "myapp"
	// so the in-cluster Ingress resource matches on the original app name.
	// Escape dots for nginx regex
	nginxSuffix := `-` + clusterName + `\.arlint\.dev`
	nginxConf := fmt.Sprintf(`resolver 127.0.0.11 valid=10s;

server {
    listen 80;
    server_name _;

    set $backend http://%s:30080;

    location / {
        set $app_host $host;
        if ($host ~ "^(.+)%s$") {
            set $app_host $1;
        }
        proxy_pass $backend;
        proxy_set_header Host $app_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host $host;
    }
}`, serverName, nginxSuffix)

	labels := map[string]string{
		labelBridgeManaged:   "true",
		labelBridgeCluster:   clusterName,
		labelOwner:           owner,
		"traefik.enable":     "false",
	}

	cfg := &container.Config{
		Image:  bridgeImage,
		Labels: labels,
	}

	hostCfg := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}

	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: {},
		},
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, bridgeName)
	if err != nil {
		return fmt.Errorf("create bridge: %w", err)
	}

	// Connect to k3d network
	if err := cli.NetworkConnect(ctx, k3dNetwork, resp.ID, nil); err != nil {
		log.Printf("warning: failed to connect bridge to k3d network %s: %v", k3dNetwork, err)
	}

	// Copy nginx config into the container before starting
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	confBytes := []byte(nginxConf)
	tw.WriteHeader(&tar.Header{
		Name: "default.conf",
		Mode: 0644,
		Size: int64(len(confBytes)),
	})
	tw.Write(confBytes)
	tw.Close()

	if err := cli.CopyToContainer(ctx, resp.ID, "/etc/nginx/conf.d/", &buf, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copy nginx config: %w", err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start bridge: %w", err)
	}

	log.Printf("launched bridge container %s for cluster %s", bridgeName, clusterName)
	return nil
}

// removeBridge removes the bridge container and its traefik config for a cluster.
func removeBridge(cli *client.Client, clusterName string) {
	ctx := context.Background()
	bridgeName := "bridge-" + clusterName

	if _, err := cli.ContainerInspect(ctx, bridgeName); err == nil {
		cli.ContainerStop(ctx, bridgeName, container.StopOptions{})
		cli.ContainerRemove(ctx, bridgeName, container.RemoveOptions{Force: true})
		log.Printf("removed bridge container for cluster: %s", clusterName)
	}

	// Remove the traefik dynamic config file
	configPath := filepath.Join(traefikDynamicDir, "bridge-"+clusterName+".yml")
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		log.Printf("warning: failed to remove traefik config %s: %v", configPath, err)
	}
}

// writeTraefikConfig writes a Traefik dynamic config file for a cluster's ingress routes.
func writeTraefikConfig(clusterName string, hostnames []string) error {
	bridgeName := "bridge-" + clusterName
	configPath := filepath.Join(traefikDynamicDir, "bridge-"+clusterName+".yml")

	if len(hostnames) == 0 {
		// No ingresses — remove config file if it exists
		if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	// Build routers section
	var routers strings.Builder
	for _, h := range hostnames {
		routerName := strings.ReplaceAll(h, ".", "-")
		fmt.Fprintf(&routers, `    %s:
      rule: "Host(%s)"
      service: %s
      middlewares:
        - %s-auth
`, routerName, "`"+h+".arlint.dev`", bridgeName, bridgeName)
	}

	config := fmt.Sprintf(`http:
  routers:
%s
  middlewares:
    %s-auth:
      forwardAuth:
        address: "http://code:8080/api/auth"
        authResponseHeaders:
          - "Cf-Access-Authenticated-User-Email"

  services:
    %s:
      loadBalancer:
        servers:
          - url: "http://%s:80"
`, routers.String(), bridgeName, bridgeName, bridgeName)

	if err := os.MkdirAll(traefikDynamicDir, 0755); err != nil {
		return fmt.Errorf("mkdir traefik dynamic: %w", err)
	}

	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		return fmt.Errorf("write traefik config: %w", err)
	}

	return nil
}

// watchIngresses polls Ingress resources in all running k3d clusters and
// maintains bridge containers + Traefik dynamic configs.
func watchIngresses(cli *client.Client, idle *idleTracker, clusterIdle *idleTracker, wsHub *hub) {
	ticker := time.NewTicker(ingressPollInterval)
	defer ticker.Stop()

	// Track previous state to detect changes
	prevIngresses := make(map[string]string) // cluster -> comma-joined hostnames

	for range ticker.C {
		// Find all running clusters with kubeconfigs
		entries, err := os.ReadDir(kubeconfigDir)
		if err != nil {
			continue
		}

		activeConfigs := make(map[string]bool)

		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			clusterName := strings.TrimSuffix(e.Name(), ".yaml")
			kcPath := filepath.Join(kubeconfigDir, e.Name())

			// Check if cluster is actually running by looking for a running server container
			serverName := "k3d-" + clusterName + "-server-0"
			inspect, err := cli.ContainerInspect(context.Background(), serverName)
			if err != nil || !inspect.State.Running {
				continue
			}

			// Get the owner from the k3d container labels
			owner := inspect.Config.Labels[labelOwner]

			// Ensure code-hub is on the k3d network so kubectl can reach the API
			ensureOnK3dNetwork(cli, clusterName)

			// Ensure ingress controller is deployed
			ensureIngressController(clusterName)

			// Query ingresses
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kcPath,
				"get", "ingress", "-A", "-o", "json")
			output, err := cmd.Output()
			cancel()
			if err != nil {
				continue
			}

			var result struct {
				Items []struct {
					Spec struct {
						Rules []struct {
							Host string `json:"host"`
						} `json:"rules"`
					} `json:"spec"`
				} `json:"items"`
			}
			if err := json.Unmarshal(output, &result); err != nil {
				continue
			}

			// Collect hostnames: each rule host becomes {host}-{cluster}
			var hostnames []string
			seen := make(map[string]bool)
			for _, item := range result.Items {
				for _, rule := range item.Spec.Rules {
					h := rule.Host
					if h == "" {
						continue
					}
					fqdn := h + "-" + clusterName
					if !seen[fqdn] {
						seen[fqdn] = true
						hostnames = append(hostnames, fqdn)
					}
				}
			}

			activeConfigs["bridge-"+clusterName+".yml"] = true
			key := strings.Join(hostnames, ",")

			if len(hostnames) > 0 {
				// Ensure bridge container exists
				if err := launchBridge(cli, clusterName, owner); err != nil {
					log.Printf("bridge launch error for cluster %s: %v", clusterName, err)
					continue
				}

				// Write/update traefik config if changed
				if prevIngresses[clusterName] != key {
					if err := writeTraefikConfig(clusterName, hostnames); err != nil {
						log.Printf("traefik config error for cluster %s: %v", clusterName, err)
					} else {
						log.Printf("updated ingress routes for cluster %s: %v", clusterName, hostnames)
						wsHub.broadcastAll(cli, idle, clusterIdle)
					}
				}
			} else if prevIngresses[clusterName] != "" {
				// Had ingresses before, now none — clean up config but keep bridge
				writeTraefikConfig(clusterName, nil)
				wsHub.broadcastAll(cli, idle, clusterIdle)
			}

			prevIngresses[clusterName] = key
		}

		// Clean up configs for clusters that no longer exist
		dynamicEntries, err := os.ReadDir(traefikDynamicDir)
		if err != nil {
			continue
		}
		for _, e := range dynamicEntries {
			if strings.HasPrefix(e.Name(), "bridge-") && strings.HasSuffix(e.Name(), ".yml") {
				if !activeConfigs[e.Name()] {
					clusterName := strings.TrimPrefix(strings.TrimSuffix(e.Name(), ".yml"), "bridge-")
					removeBridge(cli, clusterName)
					delete(prevIngresses, clusterName)
					wsHub.broadcastAll(cli, idle, clusterIdle)
				}
			}
		}
	}
}

// getExposedApps reads the traefik dynamic config to find exposed app hostnames for a cluster.
func getExposedApps(clusterName string) []string {
	configPath := filepath.Join(traefikDynamicDir, "bridge-"+clusterName+".yml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}

	// Parse hostnames from the Host() rules in the config
	var apps []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "rule:") && strings.Contains(line, "Host(") {
			// Extract hostname from: rule: "Host(`myapp-dev.arlint.dev`)"
			start := strings.Index(line, "`")
			end := strings.LastIndex(line, "`")
			if start >= 0 && end > start {
				apps = append(apps, strings.TrimSuffix(line[start+1:end], ".arlint.dev"))
			}
		}
	}
	return apps
}
