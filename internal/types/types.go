package types

// InstanceInfo represents a workspace instance (Deployment-backed).
type InstanceInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	State      string `json:"state"`
	Status     string `json:"status"`
	URL        string `json:"url"`
	Owner      string `json:"owner"`
	Cluster    string `json:"cluster,omitempty"`
	LastAccess *int64 `json:"lastAccess,omitempty"`
}

// ClusterInfo represents a vCluster instance.
type ClusterInfo struct {
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	TerminalURL   string   `json:"terminalUrl,omitempty"`
	TerminalState string   `json:"terminalState,omitempty"`
	ExposedApps   []string `json:"exposedApps,omitempty"`
	LastStart     *int64   `json:"lastStart,omitempty"`
}

// TypeStats tracks workspace counts per type.
type TypeStats struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Stopped int `json:"stopped"`
}

// GlobalStats tracks workspace counts across all users.
type GlobalStats struct {
	Total   int                  `json:"total"`
	Running int                  `json:"running"`
	Stopped int                  `json:"stopped"`
	ByType  map[string]TypeStats `json:"byType"`
}

// ListResponse is the JSON payload sent to WebSocket clients and GET /api/instances.
type ListResponse struct {
	Instances          []InstanceInfo `json:"instances"`
	Global             GlobalStats    `json:"global"`
	Clusters           []ClusterInfo  `json:"clusters"`
	ClusterIdleTimeout int64          `json:"clusterIdleTimeout"`
}
