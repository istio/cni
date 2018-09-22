// Copyright 2017 Istio authors
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

package main

import (
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	kapiv1 "k8s.io/api/core/v1"
)

func NewK8sClient(conf PluginConf, logger *logrus.Entry) (*kubernetes.Clientset, error) {
	// Some config can be passed in a kubeconfig file
	kubeconfig := conf.Kubernetes.Kubeconfig

	// Config can be overridden by config passed in explicitly in the network config.
	configOverrides := &clientcmd.ConfigOverrides{}

	// Use the kubernetes client code to load the kubeconfig file and combine it with the overrides.
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		configOverrides).ClientConfig()
	if err != nil {
		logger.Debugf("Failed setting up kubernetes client with kubeconfig %s", kubeconfig)
		return nil, err
	}

	logger.Debugf("Set up kubernetes client with kubeconfig %s", kubeconfig)
	logger.Debugf("Kubernetes config %v", config)

	// Create the clientset
	return kubernetes.NewForConfig(config)
}

func GetK8sPodInfo(client *kubernetes.Clientset, podName, podNamespace string) (labels map[string]string, annotations map[string]string, ports []kapiv1.ContainerPort, err error) {
	pod, err := client.CoreV1().Pods(string(podNamespace)).Get(podName, metav1.GetOptions{})
	logrus.Infof("pod info %+v", pod)
	if err != nil {
		return nil, nil, nil, err
	}

	for _, container := range pod.Spec.Containers {
		logrus.WithFields(logrus.Fields{
			"pod": podName,
			"container": container.Name,
		}).Info("Inspecting container")
		for _, containerPort := range container.Ports {
			logrus.WithFields(logrus.Fields{
				"pod": podName,
				"container": container.Name,
				"port": containerPort,
			}).Info("Added pod port")
			ports = append(ports, containerPort)
		}
	}

	return pod.Labels, pod.Annotations, ports, nil
}
