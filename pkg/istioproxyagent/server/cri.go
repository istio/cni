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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/containernetworking/plugins/pkg/ns"
	"io/ioutil"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/kubelet/apis/cri"
	criapi "k8s.io/kubernetes/pkg/kubelet/apis/cri/runtime/v1alpha2"
	"k8s.io/kubernetes/pkg/kubelet/remote"
	"k8s.io/kubernetes/third_party/forked/golang/expansion"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	volumesBaseDir        = "/tmp/istio-proxy-volumes/"
	sidecarLabelKey       = "istio-sidecar"
	sidecarLabelValue     = "true"
	containerNameLabelKey = "io.kubernetes.container.name"
	podNameLabelKey       = "io.kubernetes.pod.name"
	podNamespaceLabelKey  = "io.kubernetes.pod.namespace"
	podUIDLabelKey        = "io.kubernetes.pod.uid"

	terminationMessagePathAnnotation   = "io.kubernetes.container.terminationMessagePath"
	terminationMessagePolicyAnnotation = "io.kubernetes.container.terminationMessagePolicy"
	containerHashAnnotation            = "io.kubernetes.container.hash"
	restartCountAnnotation             = "io.kubernetes.container.restartCount"
	terminationGracePeriodAnnotation   = "io.kubernetes.pod.terminationGracePeriod"

	defaultTerminationGracePeriodSeconds = 30
)

type CRIRuntime struct {
	config         ProxyAgentConfig
	kubeClient     *KubernetesClient
	runtimeService cri.RuntimeService
	imageService   cri.ImageManagerService
	httpClient     http.Client
}

func NewCRIRuntime(kubeclient *KubernetesClient, config ProxyAgentConfig) (*CRIRuntime, error) {
	runtimeService, err := remote.NewRemoteRuntimeService(getRemoteRuntimeEndpoint(), 2*time.Minute)
	if err != nil {
		return nil, err
	}

	imageService, err := remote.NewRemoteImageService(getRemoteImageEndpoint(), 2*time.Minute)
	if err != nil {
		return nil, err
	}

	return &CRIRuntime{
		config:         config,
		kubeClient:     kubeclient,
		runtimeService: runtimeService,
		imageService:   imageService,
		httpClient:     http.Client{},
	}, nil
}

func (p *CRIRuntime) StartProxy(podSandboxID string, pod *v1.Pod, sidecar *v1.Container, volumes []v1.Volume) error {
	err := p.pullImageIfNecessary(sidecar.Image)
	if err != nil {
		return fmt.Errorf("Could not pull image %s: %v", sidecar.Image, err)
	}

	status, err := p.runtimeService.PodSandboxStatus(podSandboxID)
	if err != nil {
		return fmt.Errorf("Error getting pod sandbox status: %v", err)
	}

	klog.Info("Creating volumes")
	mounts, err := p.createVolumeMounts(pod, sidecar, volumes)
	if err != nil {
		return fmt.Errorf("Error creating volumes: %v", err)
	}

	envs, err := convertEnvs(pod, sidecar.Env, sidecar.EnvFrom)
	if err != nil {
		return fmt.Errorf("Error converting env vars: %v", err)
	}

	expandVars(sidecar.Command, envs)
	expandVars(sidecar.Args, envs)

	restartCount := 0 // TODO
	terminationGracePeriodSeconds := int64(defaultTerminationGracePeriodSeconds)
	if pod.Spec.TerminationGracePeriodSeconds != nil {
		terminationGracePeriodSeconds = *pod.Spec.TerminationGracePeriodSeconds
	}

	containerConfig := criapi.ContainerConfig{
		Metadata: &criapi.ContainerMetadata{
			Name: sidecar.Name,
		},
		Image: &criapi.ImageSpec{
			Image: sidecar.Image,
		},
		Command: sidecar.Command,
		Args:    sidecar.Args,
		LogPath: filepath.Join(sidecar.Name, fmt.Sprintf("%d.log", restartCount)),
		Linux: &criapi.LinuxContainerConfig{
			Resources: &criapi.LinuxContainerResources{
				CpuShares:          getCPUShares(sidecar),
				MemoryLimitInBytes: sidecar.Resources.Limits.Memory().Value(),
			},
			SecurityContext: &criapi.LinuxContainerSecurityContext{
				RunAsUser:          &criapi.Int64Value{*sidecar.SecurityContext.RunAsUser},
				SupplementalGroups: []int64{0}, // all containers get ROOT GID by default
				Privileged:         *sidecar.SecurityContext.Privileged,
				ReadonlyRootfs:     *sidecar.SecurityContext.ReadOnlyRootFilesystem,
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
		Envs:   envs,
		Mounts: mounts,
		Labels: map[string]string{
			sidecarLabelKey:       sidecarLabelValue,
			containerNameLabelKey: sidecar.Name,
			podNameLabelKey:       pod.Name,
			podNamespaceLabelKey:  pod.Namespace,
			podUIDLabelKey:        string(pod.UID),
		},
		Annotations: map[string]string{
			terminationMessagePathAnnotation:   "/dev/termination-log",
			terminationMessagePolicyAnnotation: "File",
			containerHashAnnotation:            "0", // TODO
			restartCountAnnotation:             strconv.Itoa(restartCount),
			terminationGracePeriodAnnotation:   strconv.FormatInt(terminationGracePeriodSeconds, 10),
		},
	}

	podSandboxConfig := criapi.PodSandboxConfig{
		Metadata:     status.GetMetadata(),
		LogDirectory: filepath.Join("/var/log/pods", string(pod.UID)),
	}

	klog.Infof("podSandboxConfig: %v", toDebugJSON(podSandboxConfig))
	klog.Infof("containerConfig: %v", toDebugJSON(containerConfig))

	klog.Infof("Creating proxy sidecar container for pod %s", pod.Name)
	containerID, err := p.runtimeService.CreateContainer(podSandboxID, &containerConfig, &podSandboxConfig)
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

func getCPUShares(container *v1.Container) int64 {
	cpuRequest := container.Resources.Requests.Cpu()
	cpuLimit := container.Resources.Limits.Cpu()
	if cpuRequest.IsZero() && !cpuLimit.IsZero() {
		return milliCPUToShares(cpuLimit.MilliValue())
	} else {
		return milliCPUToShares(cpuRequest.MilliValue())
	}
}

func milliCPUToShares(milliCPU int64) int64 {
	const (
		// Taken from lmctfy https://github.com/google/lmctfy/blob/master/lmctfy/controllers/cpu_controller.cc
		minShares     = 2
		sharesPerCPU  = 1024
		milliCPUToCPU = 1000
	)

	if milliCPU == 0 {
		// Return 2 here to really match kernel default for zero milliCPU.
		return minShares
	}
	// Conceptually (milliCPU / milliCPUToCPU) * sharesPerCPU, but factored to improve rounding.
	shares := (milliCPU * sharesPerCPU) / milliCPUToCPU
	if shares < minShares {
		return minShares
	}
	return shares
}

func expandVars(strings []string, envVars []*criapi.KeyValue) {
	mappingFunc := expansion.MappingFuncFor(EnvVarsToMap(envVars))

	for i, s := range strings {
		strings[i] = expansion.Expand(s, mappingFunc)
	}
}

func EnvVarsToMap(envs []*criapi.KeyValue) map[string]string {
	result := map[string]string{}
	for _, env := range envs {
		result[env.Key] = env.Value
	}

	return result
}

func sidecarTemplateVersionHash(in string) string {
	hash := sha256.Sum256([]byte(in))
	return hex.EncodeToString(hash[:])
}

func convertEnvs(pod *v1.Pod, env []v1.EnvVar, envFromSources []v1.EnvFromSource) ([]*criapi.KeyValue, error) {
	if len(envFromSources) > 0 {
		return nil, fmt.Errorf("EnvFrom not supported")
	}

	r := []*criapi.KeyValue{}

	tmpEnv := make(map[string]string)
	mappingFunc := expansion.MappingFuncFor(tmpEnv)

	for _, e := range env {
		value := e.Value

		if e.ValueFrom != nil && e.ValueFrom.FieldRef != nil {
			fieldRef := e.ValueFrom.FieldRef
			switch {
			case fieldRef.FieldPath == "metadata.uid":
				value = string(pod.UID)
			case fieldRef.FieldPath == "metadata.name":
				value = pod.Name
			case fieldRef.FieldPath == "metadata.namespace":
				value = pod.Namespace
			case fieldRef.FieldPath == "status.podIP":
				value = pod.Status.PodIP
			}
		}

		value = expansion.Expand(value, mappingFunc)

		tmpEnv[e.Name] = value
		r = append(r, &criapi.KeyValue{
			Key:   e.Name,
			Value: value,
		})
	}

	return r, nil
}

func (p *CRIRuntime) pullImageIfNecessary(image string) error {
	klog.Infof("Checking if image %s is available locally", image)

	imageSpec := criapi.ImageSpec{
		Image: image,
	}
	imageStatus, err := p.imageService.ImageStatus(&imageSpec)
	if err != nil {
		return fmt.Errorf("Error getting image status: %v", err)
	}

	if imageStatus == nil {
		klog.Infof("Pulling image %s is available locally", image)
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

func (p *CRIRuntime) StopProxy(podSandboxID string) error {
	container, err := p.findProxyContainer(podSandboxID)
	if err != nil {
		return err
	}

	if container == nil {
		return SidecarNotFoundError{}
	}

	terminationGracePeriodMillis := int64(defaultTerminationGracePeriodSeconds * 1000)
	terminationGracePeriodStr := container.Annotations[terminationGracePeriodAnnotation]
	if terminationGracePeriodStr != "" {
		terminationGracePeriodMillis, err = strconv.ParseInt(terminationGracePeriodStr, 10, 64)
		if err != nil {
			klog.Warningf("Could not parse terminationGracePeriod annotation. Defaulting to %ds. Error: %v", defaultTerminationGracePeriodSeconds, err)
			terminationGracePeriodMillis = int64(defaultTerminationGracePeriodSeconds * 1000)
		}
	}
	err = p.runtimeService.StopContainer(container.Id, terminationGracePeriodMillis)
	if err != nil {
		return err
	}

	err = p.runtimeService.RemoveContainer(container.Id)
	if err != nil {
		return err
	}

	return nil
}

func (p *CRIRuntime) IsReady(podName, podNamespace, podIP, netNS string) (bool, error) {
	ready := false

	netNS = strings.Replace(netNS, "/proc/", "/hostproc/", 1) // we're running in a container; host's /proc/ is mapped to /hostproc/

	err := ns.WithNetNSPath(netNS, func(hostNS ns.NetNS) error {
		//url := "http://" + podIP + ":" + "15000" + "/server_info" // TODO: make port & path configurable
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
			klog.Infof("Readiness probe succeeded for %s", podName)
			ready = true
			return nil
		}
		klog.Infof("Readiness probe failed for %s (%s): %v %s", podName, url, response.StatusCode, response.Status)
		return nil
	})

	return ready, err
}

func (p *CRIRuntime) findProxyContainer(podSandboxId string) (*criapi.Container, error) {
	containers, err := p.runtimeService.ListContainers(&criapi.ContainerFilter{
		PodSandboxId: podSandboxId,
		LabelSelector: map[string]string{
			sidecarLabelKey: sidecarLabelValue,
		},
	})
	if err != nil {
		return nil, err
	}

	if len(containers) == 0 {
		return nil, nil
	}

	return containers[0], nil
}

type SidecarNotFoundError struct {
	Dir  string
	Name string
}

func (e SidecarNotFoundError) Error() string {
	return "Could not find proxy sidecar among pod's containers"
}

func (p *CRIRuntime) createVolumeMounts(pod *v1.Pod, sidecar *v1.Container, volumes []v1.Volume) ([]*criapi.Mount, error) {

	hostPaths := make(map[string]string)

	for _, v := range volumes {
		switch {
		case v.EmptyDir != nil:
			hostDir, err := createEmptyDirVolume(pod.UID, v.Name)
			if err != nil {
				return nil, err
			}
			hostPaths[v.Name] = hostDir
		case v.Secret != nil:
			hostDir, err := createEmptyDirVolume(pod.UID, v.Name)
			if err != nil {
				return nil, err
			}
			hostPaths[v.Name] = hostDir

			klog.Infof("Geting Secret %s in namespace %s", v.Secret.SecretName, pod.Namespace)
			secretData, err := p.kubeClient.getSecret(v.Secret.SecretName, pod.Namespace)
			if errors.IsNotFound(err) && v.Secret.Optional != nil && *v.Secret.Optional {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("Error geting Secret data %v", err)
			}

			klog.Infof("Writing secret data to %s", hostDir)
			err = writeSecret(hostDir, secretData)
			if err != nil {
				return nil, fmt.Errorf("Error writing secret data: %v", err)
			}
		default:
			return nil, fmt.Errorf("Unsupported volume type in volume %s", v.Name)
		}
	}

	mounts := []*criapi.Mount{}
	for _, m := range sidecar.VolumeMounts {
		mounts = append(mounts, &criapi.Mount{
			HostPath:      hostPaths[m.Name],
			ContainerPath: m.MountPath,
			Readonly:      m.ReadOnly,
			Propagation:   convertMountPropagation(m.MountPropagation),
		})
	}

	return mounts, nil
}

func (p *CRIRuntime) ListPodSandboxes() ([]*criapi.PodSandbox, error) {
	return p.runtimeService.ListPodSandbox(&criapi.PodSandboxFilter{})
}

func createEmptyDirVolume(podUID types.UID, volumeName string) (string, error) {
	dir := filepath.Join(volumesBaseDir, string(podUID), volumeName)
	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		return "", err
	}

	// ensure the dir is world writable, so it's accessible from within container (might not be fully writable if umask is set)
	err = os.Chmod(dir, 0777)
	if err != nil {
		return "", err
	}

	return dir, nil
}

func convertMountPropagation(mode *v1.MountPropagationMode) criapi.MountPropagation {
	if mode == nil {
		return criapi.MountPropagation_PROPAGATION_PRIVATE
	}

	switch *mode {
	default:
		fallthrough
	case v1.MountPropagationNone:
		return criapi.MountPropagation_PROPAGATION_PRIVATE
	case v1.MountPropagationHostToContainer:
		return criapi.MountPropagation_PROPAGATION_HOST_TO_CONTAINER
	case v1.MountPropagationBidirectional:
		return criapi.MountPropagation_PROPAGATION_BIDIRECTIONAL
	}
}

func writeSecret(dir string, secretData map[string][]byte) error {
	for k, v := range secretData {
		err := ioutil.WriteFile(filepath.Join(dir, k), v, os.ModePerm)
		if err != nil {
			return err
		}
	}
	return nil
}
