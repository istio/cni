package api

type StartRequest struct {
	PodName      string
	PodNamespace string
	PodIP        string
	PodSandboxID string
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
