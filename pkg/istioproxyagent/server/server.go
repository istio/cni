package server

import (
	"encoding/json"
	"fmt"
	"github.com/ghodss/yaml"
	"istio.io/cni/pkg/istioproxyagent/api"
	"istio.io/istio/pilot/pkg/kube/inject"
	"istio.io/istio/pilot/pkg/model"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
	"net/http"
)

type server struct {
	kubeClient *kubernetes.Clientset
	bindAddr   string
	runtime    *CRIRuntime
}

func NewProxyAgent(bindAddr string) (*server, error) {
	runtime, err := NewCRIRuntime()
	if err != nil {
		return nil, err
	}

	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &server{
		kubeClient: kube,
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

	pod, err := getPod(p.kubeClient, request.PodName, request.PodNamespace)
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
	secretData, k8sErr := getKubeSecret(p.kubeClient, "istio.default", request.PodNamespace) // TODO: get secret name
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

func getPod(client *kubernetes.Clientset, podName, podNamespace string) (pod *v1.Pod, err error) {
	return client.CoreV1().Pods(string(podNamespace)).Get(podName, metav1.GetOptions{})
}

func getKubeSecret(client *kubernetes.Clientset, secretName, secretNamespace string) (data map[string][]byte, err error) {
	secret, err := client.CoreV1().Secrets(string(secretNamespace)).Get(secretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	klog.Infof("secret info: %+v", secret)
	return secret.Data, nil
}

func getKubeConfigMap(client *kubernetes.Clientset, configMapName, configMapNamespace string) (data map[string]string, err error) {
	configMap, err := client.CoreV1().ConfigMaps(string(configMapNamespace)).Get(configMapName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	klog.Infof("config map info: %+v", configMap)
	return configMap.Data, nil
}

func (p *server) getSidecar(pod *v1.Pod) (*v1.Container, error) {
	controlPlaneNamespace := "istio-system" // TODO make all these configurable
	meshConfigMapName := "istio"
	meshConfigMapKey := "mesh"
	injectConfigMapName := "istio-sidecar-injector"
	injectConfigMapKey := "config"

	klog.Infof("Geting ConfigMap %s in namespace %s", meshConfigMapName, controlPlaneNamespace)
	meshConfigMapData, err := getKubeConfigMap(p.kubeClient, meshConfigMapName, controlPlaneNamespace)
	if err != nil {
		return nil, fmt.Errorf("Error geting ConfigMap data %v", err)
	}
	meshConfig := meshConfigMapData[meshConfigMapKey]

	klog.Infof("Geting ConfigMap %s in namespace %s", injectConfigMapName, controlPlaneNamespace)
	injectorConfigMapData, err := getKubeConfigMap(p.kubeClient, injectConfigMapName, controlPlaneNamespace)
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
