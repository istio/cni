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
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/sirupsen/logrus"
	"io"
	"istio.io/cni/pkg/istioproxyagent/api"
	"net/http"
)

type proxyAgentClient struct {
	httpClient *http.Client
	URL        string
	log        *logrus.Entry
}

func NewProxyAgentClient(URL string, log *logrus.Entry) (*proxyAgentClient, error) {
	return &proxyAgentClient{
		httpClient: http.DefaultClient,
		URL:        URL,
		log:        log,
	}, nil
}

func (p *proxyAgentClient) StartProxy(podName, podNamespace, podIP, infraContainerID string) error {
	url := fmt.Sprintf("/sidecars/%s/%s", podNamespace, podName)
	httpResponse, err := p.callAgent(http.MethodPut, url, api.StartRequest{
		podIP,
		infraContainerID,
	}, nil)

	if err != nil {
		return err
	}

	if httpResponse.StatusCode != http.StatusOK {
		return fmt.Errorf("Agent returned an error: %v", httpResponse.Status)
	}

	return nil
}

func (p *proxyAgentClient) StopProxy(podName, podNamespace, podSandboxID string) error {
	url := fmt.Sprintf("/sidecars/%s/%s?podSandboxID=%s", podNamespace, podName, podSandboxID)
	httpResponse, err := p.callAgent(http.MethodDelete, url, nil, nil)

	if err != nil {
		return err
	}

	if httpResponse.StatusCode != http.StatusOK && httpResponse.StatusCode != http.StatusGone {
		return fmt.Errorf("Agent returned an error: %v", httpResponse.Status)
	}

	return nil
}

func (p *proxyAgentClient) IsReady(podName string, podNamespace string, podIP string, netNS string) (bool, error) {

	readinessResponse := api.ReadinessResponse{}

	url := fmt.Sprintf("/sidecars/%s/%s/readiness?podIP=%s&netNS=%s", podNamespace, podName, podIP, netNS)
	httpResponse, err := p.callAgent(http.MethodGet, url, nil, &readinessResponse)

	if err != nil || httpResponse.StatusCode != http.StatusOK {
		return false, err
	}

	return readinessResponse.Ready, nil
}

func (p *proxyAgentClient) callAgent(method, path string, request interface{}, responseObj interface{}) (*http.Response, error) {
	var requestBody io.Reader
	if request != nil {
		b, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		requestBody = bytes.NewReader(b)
	}

	url := p.URL + path
	p.log.Debugf("Calling agent URL %s", url)

	req, err := http.NewRequest(method, url, requestBody)
	if err != nil {
		return nil, err
	}

	response, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if responseObj != nil {
		p.log.Debug("Decoding JSON response")
		decoder := json.NewDecoder(response.Body)
		err := decoder.Decode(responseObj)
		if err != nil {
			return nil, fmt.Errorf("Could not decode response: %v", err)
		}
	}

	p.log.Debugf("Agent returned status: %v", response.Status)

	return response, nil
}
