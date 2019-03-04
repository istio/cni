package server

import (
	"fmt"
	"github.com/containernetworking/plugins/pkg/ns"
	"istio.io/cni/pkg/istioproxyagent/api"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/kubelet/apis/cri"
	criapi "k8s.io/kubernetes/pkg/kubelet/apis/cri/runtime/v1alpha2"
	"k8s.io/kubernetes/pkg/kubelet/remote"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const (
	containerName = "istio-proxy"
)

type CRIRuntime struct {
	runtimeService cri.RuntimeService
	imageService   cri.ImageManagerService
	httpClient     http.Client
}

func NewCRIRuntime() (*CRIRuntime, error) {
	runtimeService, err := remote.NewRemoteRuntimeService(getRemoteRuntimeEndpoint(), 2*time.Minute)
	if err != nil {
		return nil, err
	}

	imageService, err := remote.NewRemoteImageService(getRemoteImageEndpoint(), 2*time.Minute)
	if err != nil {
		return nil, err
	}

	return &CRIRuntime{
		runtimeService: runtimeService,
		imageService:   imageService,
		httpClient:     http.Client{},
	}, nil
}

func (p *CRIRuntime) StartProxy(request *api.StartRequest) error {

	config := NewDefaultProxyConfig() // TODO: get this from configmap (or from client?)

	err := p.pullImageIfNecessary(config)
	if err != nil {
		return err
	}

	status, err := p.runtimeService.PodSandboxStatus(request.PodSandboxID)
	if err != nil {
		return fmt.Errorf("Error getting pod sandbox status: %v", err)
	}

	podSandboxConfig := criapi.PodSandboxConfig{
		Metadata: status.GetMetadata(),
	}

	klog.Info("Creating volumes")
	secretDir, confDir, err := createVolumes()
	if err != nil {
		return fmt.Errorf("Error creating volumes: %v", err)
	}

	klog.Infof("Writing secret data to %s", secretDir)
	err = writeSecret(secretDir, request.SecretData)
	if err != nil {
		return fmt.Errorf("Error writing secret data: %v", err)
	}

	annotationsJSON, err := toJSON(request.Annotations)
	if err != nil {
		return fmt.Errorf("Error converting annotations to JSON: %v", err)
	}

	labelsJSON, err := toJSON(request.Labels)
	if err != nil {
		return fmt.Errorf("Error converting labels to JSON: %v", err)
	}

	containerConfig := criapi.ContainerConfig{
		Metadata: &criapi.ContainerMetadata{
			Name: containerName,
		},
		Image: &criapi.ImageSpec{
			Image: config.image,
		},
		//Command:[]string{"sleep", "9999999"},
		Args: config.args,
		Linux: &criapi.LinuxContainerConfig{
			Resources: &criapi.LinuxContainerResources{
				// TODO
			},
			SecurityContext: &criapi.LinuxContainerSecurityContext{
				RunAsUser:          &criapi.Int64Value{config.runAsUser},
				SupplementalGroups: []int64{0},
				Privileged:         true,
			},
		},
		Windows: &criapi.WindowsContainerConfig{
			Resources: &criapi.WindowsContainerResources{
				// TODO
			},
			SecurityContext: &criapi.WindowsContainerSecurityContext{
				RunAsUsername: "NotImplemented", // TODO
			},
		},
		Envs: []*criapi.KeyValue{
			{
				Key:   "POD_NAME",
				Value: request.PodName,
			},
			{
				Key:   "POD_NAMESPACE",
				Value: request.PodNamespace,
			},
			{
				Key:   "INSTANCE_IP",
				Value: request.PodIP,
			},
			{
				Key:   "ISTIO_META_POD_NAME",
				Value: request.PodName,
			},
			{
				Key:   "ISTIO_META_CONFIG_NAMESPACE",
				Value: request.PodNamespace,
			},
			{
				Key:   "ISTIO_META_INTERCEPTION_MODE",
				Value: config.interceptionMode,
			},
			{
				Key:   "ISTIO_METAJSON_ANNOTATIONS",
				Value: annotationsJSON,
			},
			{
				Key:   "ISTIO_METAJSON_LABELS",
				Value: labelsJSON,
			},
		},
		Mounts: []*criapi.Mount{
			{
				ContainerPath: "/etc/istio/proxy/",
				HostPath:      confDir,
				Readonly:      false,
			},
			{
				ContainerPath: "/etc/certs/",
				HostPath:      secretDir,
				Readonly:      true,
			},
		},
		Labels: map[string]string{
			"io.kubernetes.container.name": containerName,
			"io.kubernetes.pod.name":       request.PodName,
			"io.kubernetes.pod.namespace":  request.PodNamespace,
			"io.kubernetes.pod.uid":        request.PodUID,
		},
		Annotations: map[string]string{
			"io.kubernetes.container.terminationMessagePath":   "/dev/termination-log",
			"io.kubernetes.container.terminationMessagePolicy": "File",
			"io.kubernetes.container.hash":                     "0", // TODO
			"io.kubernetes.container.restartCount":             "0", // TODO
		},
	}

	klog.Infof("Creating proxy sidecar container for pod %s", request.PodName)
	containerID, err := p.runtimeService.CreateContainer(request.PodSandboxID, &containerConfig, &podSandboxConfig)
	if err != nil {
		return fmt.Errorf("Error creating sidecar container: %v", err)
	}
	klog.Infof("Created proxy sidecar container: %s", containerID)

	err = p.runtimeService.StartContainer(containerID)
	if err != nil {
		return fmt.Errorf("Error starting sidecar container: %v", err)
	}
	klog.Infof("Started proxy sidecar container: %s", containerID)

	return nil
}

func (p *CRIRuntime) pullImageIfNecessary(config ProxyConfig) error {
	klog.Infof("Checking if image %s is available locally", config.image)

	imageSpec := criapi.ImageSpec{
		Image: config.image,
	}
	imageStatus, err := p.imageService.ImageStatus(&imageSpec)
	if err != nil {
		return fmt.Errorf("Error getting image status: %v", err)
	}

	if imageStatus == nil {
		klog.Infof("Pulling image %s is available locally", config.image)
		var authConfig *criapi.AuthConfig = nil // TODO: implement image pull authentication
		imageRef, err := p.imageService.PullImage(&imageSpec, authConfig)
		if err != nil {
			return fmt.Errorf("Error pulling image: %v", err)
		}
		klog.Infof("Successfully pulled image. Image ref: %s", imageRef)
	} else {
		klog.Info("Image is available locally. No need to pull it.")
	}

	return nil
}

func getRemoteImageEndpoint() string {
	return getRemoteRuntimeEndpoint()
}

func getRemoteRuntimeEndpoint() string {
	if runtime.GOOS == "linux" {
		return "unix:///var/run/dockershim.sock"
	} else if runtime.GOOS == "windows" {
		return "npipe:////./pipe/dockershim"
	}
	return ""
}

func (p *CRIRuntime) StopProxy(request *api.StopRequest) error {

	containerID, err := p.findProxyContainerID(request.PodSandboxID)
	if err != nil {
		return err
	}

	err = p.runtimeService.StopContainer(containerID, 30000) // TODO: make timeout configurable
	if err != nil {
		return err
	}

	return nil
}

func (p *CRIRuntime) IsReady(request *api.ReadinessRequest) (bool, error) {
	ready := false

	netNS := strings.Replace(request.NetNS, "/proc/", "/hostproc/", 1) // we're running in a container; host's /proc/ is mapped to /hostproc/

	err := ns.WithNetNSPath(netNS, func(hostNS ns.NetNS) error {
		//url := "http://" + request.PodIP + ":" + "15000" + "/server_info" // TODO: make port & path configurable
		url := "http://" + "localhost" + ":" + "15000" + "/server_info" // TODO: make port & path configurable
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return err
		}

		response, err := p.httpClient.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()

		if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusBadRequest {
			klog.Infof("Readiness probe succeeded for %s", request.PodName)
			ready = true
			return nil
		}
		klog.Infof("Readiness probe failed for %s (%s): %v %s", request.PodName, url, response.StatusCode, response.Status)
		return nil
	})

	return ready, err
}

func (p *CRIRuntime) findProxyContainerID(podSandboxId string) (string, error) {
	containers, err := p.runtimeService.ListContainers(&criapi.ContainerFilter{
		PodSandboxId: podSandboxId,
	})
	if err != nil {
		return "", err
	}

	container, err := p.findContainerByName(containerName, containers)
	if err != nil {
		return "", err
	}

	return container.Id, nil
}

func (p *CRIRuntime) findContainerByName(name string, containers []*criapi.Container) (*criapi.Container, error) {
	for _, c := range containers {
		if c.Metadata.Name == name {
			return c, nil
		}
	}
	return nil, fmt.Errorf("Could not find container %q in list of containers", containerName)
}
