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
)

type server struct {
	kubernetes *KubernetesClient
	bindAddr   string
	runtime    *CRIRuntime
}

func NewProxyAgent(bindAddr string) (*server, error) {
	runtime, err := NewCRIRuntime()
	if err != nil {
		return nil, err
	}

	kube, err := NewKubernetesClient()
	if err != nil {
		return nil, err
	}

	return &server{
		kubernetes: kube,
		bindAddr:   bindAddr,
		runtime:    runtime,
	}, nil
}

func (p *server) Run() error {
	klog.Infof("Starting server...")
	http.HandleFunc("/start", p.startHandler)
	http.HandleFunc("/stop", p.stopHandler)
	http.HandleFunc("/readiness", p.readinessHandler)
	klog.Infof("Listening on %s", p.bindAddr)
	err := http.ListenAndServe(p.bindAddr, nil)
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

	pod, err := p.kubernetes.getPod(request.PodName, request.PodNamespace)
	if err != nil {
		klog.Warningf("Error geting ConfigMap data %v", err)
		return
	}
	pod.Status.PodIP = request.PodIP // we set it, because it's not set in the YAML yet

	sidecar, err := p.getSidecar(pod)
	if err != nil {
		klog.Warningf("Could not obtain sidecar: %v", err)
		return
	}

	klog.Infof("Geting Secret %s in namespace %s", "istio.default", request.PodNamespace)
	secretData, k8sErr := p.kubernetes.getSecret("istio.default", request.PodNamespace) // TODO: get secret name
	if k8sErr != nil {
		klog.Warningf("Error geting Secret data %v", k8sErr)
		return
	}

	err = p.runtime.StartProxy(request.PodSandboxID, pod, secretData, sidecar)
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

func (p *server) getSidecar(pod *v1.Pod) (*v1.Container, error) {
	controlPlaneNamespace := "istio-system" // TODO make all these configurable
	meshConfigMapName := "istio"
	meshConfigMapKey := "mesh"
	injectConfigMapName := "istio-sidecar-injector"
	injectConfigMapKey := "config"

	klog.Infof("Geting ConfigMap %s in namespace %s", meshConfigMapName, controlPlaneNamespace)
	meshConfigMapData, err := p.kubernetes.getConfigMap(meshConfigMapName, controlPlaneNamespace)
	if err != nil {
		return nil, fmt.Errorf("Error geting ConfigMap data %v", err)
	}
	meshConfig := meshConfigMapData[meshConfigMapKey]

	klog.Infof("Geting ConfigMap %s in namespace %s", injectConfigMapName, controlPlaneNamespace)
	injectorConfigMapData, err := p.kubernetes.getConfigMap(injectConfigMapName, controlPlaneNamespace)
	if err != nil {
		return nil, fmt.Errorf("Error geting ConfigMap data %v", err)
	}

	injectData, exists := injectorConfigMapData[injectConfigMapKey]
	if !exists {
		return nil, fmt.Errorf("missing configuration map key %q in %q", injectConfigMapKey, injectConfigMapName)
	}
	var injectConfig inject.Config
	if err := yaml.Unmarshal([]byte(injectData), &injectConfig); err != nil {
		return nil, fmt.Errorf("unable to convert data from configmap %q: %v", injectConfigMapName, err)
	}
	sidecarTemplate := injectConfig.Template

	klog.Infof("Mesh config: %v", meshConfig)
	klog.Infof("Sidecar template: %v", sidecarTemplate)

	meshConf, err := model.ApplyMeshConfigDefaults(meshConfig)
	if err != nil {
		return nil, fmt.Errorf("Could not apply mesh config defaults: %v", err)
	}

	sidecarInjectionSpec, _, err := inject.InjectionData(sidecarTemplate, sidecarTemplateVersionHash(sidecarTemplate), &pod.ObjectMeta, &pod.Spec, &pod.ObjectMeta, meshConf.DefaultConfig, meshConf)
	if err != nil {
		return nil, fmt.Errorf("Could not get injection data: %v", err)
	}

	klog.Infof("sidecarInjectionSpec: %v", toDebugJSON(sidecarInjectionSpec))

	if len(sidecarInjectionSpec.Containers) == 0 {
		return nil, fmt.Errorf("No sidecar container in sidecarInjectionSpec")
	}
	return &sidecarInjectionSpec.Containers[0], nil
}
