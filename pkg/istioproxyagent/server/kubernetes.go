package server

import (
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
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

	klog.Infof("secret info: %+v", secret)
	return secret.Data, nil
}

func (k *KubernetesClient) getConfigMap(configMapName, configMapNamespace string) (data map[string]string, err error) {
	configMap, err := k.client.CoreV1().ConfigMaps(string(configMapNamespace)).Get(configMapName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	klog.Infof("config map info: %+v", configMap)
	return configMap.Data, nil
}
