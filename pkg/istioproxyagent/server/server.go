package server

import (
	"encoding/json"
	"istio.io/cni/pkg/istioproxyagent/api"
	"k8s.io/klog"
	"net/http"
)

type server struct {
	runtime AgentRuntime
}

type AgentRuntime interface {
	StartProxy(request *api.StartRequest) error
	StopProxy(request *api.StopRequest) error
	IsReady(request *api.ReadinessRequest) (bool, error)
}

func NewProxyAgent() (*server, error) {
	//runtime, err := NewDockerRuntime()
	runtime, err := NewCRIRuntime()
	if err != nil {
		return nil, err
	}
	return &server{
		runtime: runtime,
	}, nil
}

func (p *server) Run() error {
	klog.Infof("Starting server...")
	http.HandleFunc("/start", p.startHandler)
	http.HandleFunc("/stop", p.stopHandler)
	http.HandleFunc("/readiness", p.readinessHandler)
	klog.Infof("Listening on :22222")
	err := http.ListenAndServe(":22222", nil)
	if err != nil {
		return err
	}

	return nil
}

func (p *server) startHandler(w http.ResponseWriter, r *http.Request) {
	klog.Infof("Handling proxy start request")
	request := api.StartRequest{}
	err := p.decodeRequest(r, &request)
	if err != nil {
		return
	}

	err = p.runtime.StartProxy(&request)
	if err != nil {
		klog.Errorf("Error starting proxy: %s", err)
	}
}

func (p *server) stopHandler(w http.ResponseWriter, r *http.Request) {
	klog.Infof("Handling proxy stop request")
	request := api.StopRequest{}
	err := p.decodeRequest(r, &request)
	if err != nil {
		return
	}

	err = p.runtime.StopProxy(&request)
	if err != nil {
		klog.Errorf("Error stopping proxy: %s", err)
	}
}

func (p *server) readinessHandler(w http.ResponseWriter, r *http.Request) {
	klog.Infof("Handling readiness request")
	request := api.ReadinessRequest{}
	err := p.decodeRequest(r, &request)
	if err != nil {
		return
	}

	ready, err := p.runtime.IsReady(&request)
	if err != nil {
		klog.Errorf("Error checking readiness: %s", err)
	}

	response := api.ReadinessResponse{
		Ready: ready,
	}

	encoder := json.NewEncoder(w)
	err = encoder.Encode(response)
	if err != nil {
		klog.Errorf("Error encoding readiness response: %s", err)
	}
}

func (p *server) decodeRequest(r *http.Request, obj interface{}) error {
	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(&obj)
	if err != nil {
		klog.Errorf("Error decoding request: %s", err)
	}
	return err
}
