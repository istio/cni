package api

type StartRequest struct {
	PodIP        string
	PodSandboxID string
}

type ReadinessResponse struct {
	Ready bool
}
