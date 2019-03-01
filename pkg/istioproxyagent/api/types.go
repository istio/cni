package api

type StartRequest struct {
	PodName      string
	PodNamespace string
	PodIP        string
	PodUID       string
	PodSandboxID string
	SecretData   map[string][]byte
	Labels       map[string]string
	Annotations  map[string]string
}

type StopRequest struct {
	PodName      string
	PodSandboxID string
}
