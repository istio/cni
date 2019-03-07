package api

type StartRequest struct {
	PodName         string
	PodNamespace    string
	PodIP           string
	PodUID          string
	PodSandboxID    string
	SecretData      map[string][]byte
	Labels          map[string]string
	Annotations     map[string]string
	PodJSON         string
	MeshConfig      string
	SidecarTemplate string
}

type StopRequest struct {
	PodName      string
	PodSandboxID string
}

type ReadinessRequest struct {
	PodName      string
	PodNamespace string
	PodIP        string
	NetNS        string
}

type ReadinessResponse struct {
	Ready bool
}
