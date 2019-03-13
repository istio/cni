package api

type StartRequest struct {
	PodIP           string
	PodSandboxID    string
	SecretData      map[string][]byte
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
