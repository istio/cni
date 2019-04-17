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

package server

import (
	"fmt"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"time"
)

const (
	pollInterval = 100 * time.Millisecond
	pollTimeout  = 60 * time.Second
)

type KubernetesClient struct {
	client *kubernetes.Clientset
}

func NewKubernetesClient() (*KubernetesClient, error) {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &KubernetesClient{
		client: kube,
	}, nil

}

func (k *KubernetesClient) getPod(podName, podNamespace string) (pod *v1.Pod, err error) {
	return k.client.CoreV1().Pods(string(podNamespace)).Get(podName, metav1.GetOptions{})
}

func (k *KubernetesClient) getSecret(secretName, secretNamespace string) (data map[string][]byte, err error) {
	secret, err := k.client.CoreV1().Secrets(string(secretNamespace)).Get(secretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return secret.Data, nil
}

func (k *KubernetesClient) getConfigMap(configMapName, configMapNamespace string) (data map[string]string, err error) {
	configMap, err := k.client.CoreV1().ConfigMaps(string(configMapNamespace)).Get(configMapName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return configMap.Data, nil
}

type UpdatePodFunc func(d *v1.Pod)

func (k *KubernetesClient) updatePodWithRetries(namespace, name string, applyUpdate UpdatePodFunc) (*v1.Pod, error) {
	var pod *v1.Pod
	var updateErr error
	pollErr := wait.PollImmediate(pollInterval, pollTimeout, func() (bool, error) {
		var err error
		if pod, err = k.client.CoreV1().Pods(namespace).Get(name, metav1.GetOptions{}); err != nil {
			return false, err
		}
		applyUpdate(pod)
		if pod, err = k.client.CoreV1().Pods(namespace).Update(pod); err == nil {
			return true, nil
		}
		updateErr = err
		return false, nil
	})
	if pollErr == wait.ErrWaitTimeout {
		pollErr = fmt.Errorf("couldn't apply the provided update to pod %q: %v", name, updateErr)
	}
	return pod, pollErr
}
