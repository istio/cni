package server

import (
	"encoding/json"
	"fmt"
	"github.com/ghodss/yaml"
	"istio.io/cni/pkg/istioproxyagent/api"
	"istio.io/istio/pilot/pkg/kube/inject"
	"istio.io/istio/pilot/pkg/model"
	"k8s.io/api/core/v1"
	"k8s.io/klog"
	"net/http"
	"time"
)

type server struct {
	kubernetes *KubernetesClient
	config     ProxyAgentConfig
	runtime    *CRIRuntime
}

type ProxyAgentConfig struct {
	BindAddr string

	ControlPlaneNamespace string
	MeshConfigMapName     string
	MeshConfigMapKey      string
	InjectConfigMapName   string
	InjectConfigMapKey    string

	SidecarContainerName string
}

func NewProxyAgent(config ProxyAgentConfig) (*server, error) {
	kube, err := NewKubernetesClient()
	if err != nil {
		return nil, err
	}

	runtime, err := NewCRIRuntime(kube, config)
	if err != nil {
		return nil, err
	}

	return &server{
		kubernetes: kube,
		config:     config,
		runtime:    runtime,
	}, nil
}

func (p *server) Run() error {
	syncChan := time.NewTicker(5 * time.Second)
	go p.RunPodSyncLoop(syncChan.C)

	klog.Infof("Starting server...")
	http.HandleFunc("/start", p.startHandler)
	http.HandleFunc("/stop", p.stopHandler)
	http.HandleFunc("/readiness", p.readinessHandler)
	klog.Infof("Listening on %s", p.config.BindAddr)
	err := http.ListenAndServe(p.config.BindAddr, nil)
	if err != nil {
		return err
	}

	return nil
}

func (p *server) RunPodSyncLoop(syncChan <-chan time.Time) {
	for {
		select {
		case <-syncChan:
			err := p.SyncPods()
			if err != nil {
				klog.Warningf("Could not sync pods: %v", err)
			}
		}
	}
}

func (p *server) SyncPods() error {
	return p.runtime.RestartStoppedSidecars()
}

// TODO: return HTTP error code on errors & handle them in client
func (p *server) startHandler(w http.ResponseWriter, r *http.Request) {
	klog.Infof("Handling proxy start request")
	request := api.StartRequest{}
	err := p.decodeRequest(r, &request)
	if err != nil {
		return
	}

	pod, err := p.kubernetes.getPod(request.PodName, request.PodNamespace)
	if err != nil {
		klog.Warningf("Error geting ConfigMap data %v", err)
		return
	}
	pod.Status.PodIP = request.PodIP // we set it, because it's not set in the YAML yet

	sidecarInjectionSpec, err := p.getSidecar(pod)
	if err != nil {
		klog.Warningf("Could not obtain sidecar: %v", err)
		return
	}

	klog.Infof("Starting proxy for pod %s/%s", request.PodNamespace, request.PodName)
	err = p.runtime.StartProxy(request.PodSandboxID, pod, sidecarInjectionSpec)
	if err != nil {
		klog.Errorf("Error starting proxy: %s", err)
	}
}

func (p *server) stopHandler(w http.ResponseWriter, r *http.Request) {
	klog.Info("Handling proxy stop request")
	request := api.StopRequest{}
	err := p.decodeRequest(r, &request)
	if err != nil {
		return
	}

	klog.Infof("Stopping proxy for pod %s/%s", request.PodNamespace, request.PodName)
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

func (p *server) getSidecar(pod *v1.Pod) (*inject.SidecarInjectionSpec, error) {

	klog.Infof("Geting ConfigMap %s in namespace %s", p.config.MeshConfigMapName, p.config.ControlPlaneNamespace)
	meshConfigMapData, err := p.kubernetes.getConfigMap(p.config.MeshConfigMapName, p.config.ControlPlaneNamespace)
	if err != nil {
		return nil, fmt.Errorf("Error geting ConfigMap data %v", err)
	}
	meshConfig := meshConfigMapData[p.config.MeshConfigMapKey]

	klog.Infof("Geting ConfigMap %s in namespace %s", p.config.InjectConfigMapName, p.config.ControlPlaneNamespace)
	injectorConfigMapData, err := p.kubernetes.getConfigMap(p.config.InjectConfigMapName, p.config.ControlPlaneNamespace)
	if err != nil {
		return nil, fmt.Errorf("Error geting ConfigMap data %v", err)
	}

	injectData, exists := injectorConfigMapData[p.config.InjectConfigMapKey]
	if !exists {
		return nil, fmt.Errorf("missing configuration map key %q in %q", p.config.InjectConfigMapKey, p.config.InjectConfigMapName)
	}
	var injectConfig inject.Config
	if err := yaml.Unmarshal([]byte(injectData), &injectConfig); err != nil {
		return nil, fmt.Errorf("unable to convert data from configmap %q: %v", p.config.InjectConfigMapName, err)
	}
	sidecarTemplate := injectConfig.Template

	klog.V(5).Infof("Mesh config: %v", meshConfig)
	klog.V(5).Infof("Sidecar template: %v", sidecarTemplate)

	meshConf, err := model.ApplyMeshConfigDefaults(meshConfig)
	if err != nil {
		return nil, fmt.Errorf("Could not apply mesh config defaults: %v", err)
	}

	sidecarInjectionSpec, _, err := inject.InjectionData(sidecarTemplate, sidecarTemplateVersionHash(sidecarTemplate), &pod.ObjectMeta, &pod.Spec, &pod.ObjectMeta, meshConf.DefaultConfig, meshConf)
	if err != nil {
		return nil, fmt.Errorf("Could not get injection data: %v", err)
	}

	klog.Infof("sidecarInjectionSpec: %v", toDebugJSON(sidecarInjectionSpec))

	return sidecarInjectionSpec, nil
}
