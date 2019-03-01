package client

import (
	"bytes"
	"encoding/json"
	"istio.io/cni/pkg/istioproxyagent/api"
	"net/http"
)

type proxyAgentClient struct {
	httpClient *http.Client
	URL        string
}

func NewProxyAgentClient(URL string) (*proxyAgentClient, error) {
	return &proxyAgentClient{
		httpClient: http.DefaultClient,
		URL:        URL,
	}, nil
}

func (p *proxyAgentClient) StartProxy(podName, podNamespace, podUID, podIP, infraContainerID string, secretData map[string][]byte, labels, annotations map[string]string) error {
	return p.callAgent("/start", api.StartRequest{
		podName,
		podNamespace,
		podIP,
		podUID,
		infraContainerID,
		secretData,
		labels,
		annotations,
	})
}

func (p *proxyAgentClient) StopProxy(podName, podSandboxID string) error {
	return p.callAgent("/stop", api.StopRequest{
		podName,
		podSandboxID,
	})
}

func (p *proxyAgentClient) callAgent(path string, request interface{}) error {
	b, err := json.Marshal(request)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, p.URL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}

	response, err := p.httpClient.Do(req)
	defer response.Body.Close()
	if err != nil {
		return err
	}

	return nil

}
