// Copyright 2019 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// This is a sample chained plugin that supports multiple CNI versions. It
// parses prevResult according to the cniVersion
package server

import (
	"encoding/json"
	"fmt"
	"github.com/ghodss/yaml"
	"github.com/gorilla/mux"
	"istio.io/api/mesh/v1alpha1"
	"istio.io/cni/pkg/istioproxyagent/api"
	"istio.io/istio/pilot/pkg/kube/inject"
	"istio.io/istio/pilot/pkg/model"
	"k8s.io/api/core/v1"
	"k8s.io/klog"
	"net/http"
	"sync"
	"time"
)

const (
	annotationStatus = "sidecar.istio.io/status"
)

type server struct {
	kubernetes *KubernetesClient
	config     ProxyAgentConfig
	runtime    *CRIRuntime
	mux        sync.Mutex
}

type ProxyAgentConfig struct {
	BindAddr string

	ControlPlaneNamespace string
	MeshConfigMapName     string
	MeshConfigMapKey      string
	InjectConfigMapName   string
	InjectConfigMapKey    string
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

	router := mux.NewRouter()
	router.HandleFunc("/sidecars/{podNamespace}/{podName}", wrap(p.startHandler)).Methods(http.MethodPut)
	router.HandleFunc("/sidecars/{podNamespace}/{podName}", wrap(p.stopHandler)).Methods(http.MethodDelete)
	router.HandleFunc("/sidecars/{podNamespace}/{podName}/readiness", wrap(p.readinessHandler)).Methods(http.MethodGet)
	http.Handle("/", router)

	klog.Infof("Starting server...")
	klog.Infof("Listening on %s", p.config.BindAddr)
	err := http.ListenAndServe(p.config.BindAddr, nil)
	if err != nil {
		return err
	}
	return nil
}

func wrap(handler func(w http.ResponseWriter, r *http.Request) error) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		err := handler(w, r)
		if err != nil {
			klog.Errorf("Internal error when handling request: %v", err)
			handleError(http.StatusInternalServerError, err, w, r)
		}
	}
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
	p.mux.Lock()
	defer p.mux.Unlock()

	return p.runtime.RestartStoppedSidecars()
}

func (p *server) startHandler(w http.ResponseWriter, r *http.Request) error {
	klog.Infof("Handling proxy start request")

	params := mux.Vars(r)
	podName := params["podName"]
	podNamespace := params["podNamespace"]

	request := api.StartRequest{}
	err := p.decodeRequest(r, &request)
	if err != nil {
		handleError(http.StatusBadRequest, fmt.Errorf("Could not decode request body: %v", err), w, r)
		return nil
	}

	if podName == "" || podNamespace == "" || request.PodSandboxID == "" || request.PodIP == "" {
		handleError(http.StatusBadRequest, fmt.Errorf("Fields missing"), w, r)
		return nil
	}

	pod, err := p.kubernetes.getPod(podName, podNamespace)
	if err != nil {
		return fmt.Errorf("Error geting ConfigMap data %v", err)
	}
	pod.Status.PodIP = request.PodIP // we set it, because it's not set in the YAML yet

	sidecarInjectionSpec, status, err := p.getSidecarInjectionSpec(pod)
	if err != nil {
		return fmt.Errorf("Could not obtain sidecar: %v", err)
	}

	if len(sidecarInjectionSpec.Containers) == 0 {
		return fmt.Errorf("No sidecar container in sidecarInjectionSpec")
	}

	p.mux.Lock()
	defer p.mux.Unlock()

	klog.Infof("Starting proxy for pod %s/%s", podNamespace, podName)
	err = p.runtime.StartProxy(request.PodSandboxID, pod, &sidecarInjectionSpec.Containers[0], sidecarInjectionSpec.Volumes)
	if err != nil {
		return fmt.Errorf("Error starting proxy: %s", err)
	}

	klog.Infof("Adding annotation %s to pod %s/%s", annotationStatus, podNamespace, podName)
	pod, err = p.kubernetes.updatePodWithRetries(podNamespace, podName, func(pod *v1.Pod) {
		pod.Annotations[annotationStatus] = status
	})

	if err != nil {
		return fmt.Errorf("Error adding annotation to pod: %s", err)
	}

	return err
}

func (p *server) stopHandler(w http.ResponseWriter, r *http.Request) error {
	klog.Info("Handling proxy stop request")

	params := mux.Vars(r)
	podName := params["podName"]
	podNamespace := params["podNamespace"]

	podSandboxID := r.FormValue("podSandboxID")

	if podName == "" || podNamespace == "" || podSandboxID == "" {
		handleError(http.StatusBadRequest, fmt.Errorf("Fields missing"), w, r)
		return nil
	}

	if podSandboxID == "" {
		return fmt.Errorf("PodSandboxID missing from request")
	}

	p.mux.Lock()
	defer p.mux.Unlock()

	klog.Infof("Stopping proxy for pod %s/%s", podNamespace, podName)
	err := p.runtime.StopProxy(podSandboxID)
	if err != nil {
		if _, ok := err.(SidecarNotFoundError); ok {
			klog.Errorf("Sidecar container for pod %s/%s not found", podNamespace, podName)
			handleError(http.StatusGone, err, w, r)
			return nil
		}
		return fmt.Errorf("Error stopping proxy: %s", err)
	}
	klog.Info("Proxy stopped")
	return nil
}

func handleError(statusCode int, err error, w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(statusCode)
	w.Header().Set("Content-Type", "text/plain")
	_, err2 := fmt.Fprintln(w, "Error: %v\n", err)
	if err2 != nil {
		klog.Warningf("Could not print response: %v", err2)
	}
}

func (p *server) readinessHandler(w http.ResponseWriter, r *http.Request) error {
	klog.Infof("Handling readiness request")

	params := mux.Vars(r)
	podName := params["podName"]
	podNamespace := params["podNamespace"]

	podIP := r.FormValue("podIP")
	netNS := r.FormValue("netNS")

	if podName == "" || podNamespace == "" || podIP == "" || netNS == "" {
		handleError(http.StatusBadRequest, fmt.Errorf("Fields missing"), w, r)
		return nil
	}

	ready, err := p.runtime.IsReady(podName, podNamespace, podIP, netNS)
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
	return nil
}

func (p *server) decodeRequest(r *http.Request, obj interface{}) error {
	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(&obj)
	if err != nil {
		klog.Infof("Error decoding request: %s", err)
	}
	return err
}

func (p *server) getSidecarInjectionSpec(pod *v1.Pod) (*inject.SidecarInjectionSpec, string, error) {
	sidecarTemplate, err := p.getSidecarTemplate()
	if err != nil {
		return nil, "", err
	}
	klog.V(5).Infof("Sidecar template: %v", sidecarTemplate)

	meshConfig, err := p.getMeshConfig()
	if err != nil {
		return nil, "", err
	}
	klog.V(5).Infof("Mesh config: %v", meshConfig)

	sidecarInjectionSpec, status, err := inject.InjectionData(sidecarTemplate, sidecarTemplateVersionHash(sidecarTemplate), &pod.ObjectMeta, &pod.Spec, &pod.ObjectMeta, meshConfig.DefaultConfig, meshConfig)
	if err != nil {
		return nil, "", fmt.Errorf("Could not get injection data: %v", err)
	}
	klog.Infof("sidecarInjectionSpec: %v", toDebugJSON(sidecarInjectionSpec))

	return sidecarInjectionSpec, status, nil
}

func (p *server) getSidecarTemplate() (string, error) {
	klog.Infof("Geting ConfigMap %s in namespace %s", p.config.InjectConfigMapName, p.config.ControlPlaneNamespace)
	injectorConfigMapData, err := p.kubernetes.getConfigMap(p.config.InjectConfigMapName, p.config.ControlPlaneNamespace)
	if err != nil {
		return "", fmt.Errorf("Error geting ConfigMap data %v", err)
	}

	injectData, exists := injectorConfigMapData[p.config.InjectConfigMapKey]
	if !exists {
		return "", fmt.Errorf("missing configuration map key %q in %q", p.config.InjectConfigMapKey, p.config.InjectConfigMapName)
	}
	var injectConfig inject.Config
	if err := yaml.Unmarshal([]byte(injectData), &injectConfig); err != nil {
		return "", fmt.Errorf("unable to convert data from configmap %q: %v", p.config.InjectConfigMapName, err)
	}
	return injectConfig.Template, nil
}

func (p *server) getMeshConfig() (*v1alpha1.MeshConfig, error) {
	klog.Infof("Geting ConfigMap %s in namespace %s", p.config.MeshConfigMapName, p.config.ControlPlaneNamespace)
	meshConfigMapData, err := p.kubernetes.getConfigMap(p.config.MeshConfigMapName, p.config.ControlPlaneNamespace)
	if err != nil {
		return nil, fmt.Errorf("Error geting ConfigMap data %v", err)
	}
	meshConfigYAML := meshConfigMapData[p.config.MeshConfigMapKey]

	meshConf, err := model.ApplyMeshConfigDefaults(meshConfigYAML)
	if err != nil {
		return nil, fmt.Errorf("Could not apply mesh config defaults: %v", err)
	}
	return meshConf, nil
}
