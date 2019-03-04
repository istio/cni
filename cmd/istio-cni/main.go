// Copyright 2018 Istio Authors
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
package main

import (
	"encoding/json"
	"fmt"
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/projectcalico/libcalico-go/lib/logutils"
	"github.com/sirupsen/logrus"
	proxyagentclient "istio.io/cni/pkg/istioproxyagent/client"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const agentURL = "http://localhost:22222"

var (
	nsSetupBinDir = "/opt/cni/bin"
	nsSetupProg   = "istio-iptables.sh"

	injectAnnotationKey = "sidecar.istio.io/inject"
	sidecarStatusKey    = "sidecar.istio.io/status"
)

// setupRedirect is a unit test override variable.
var setupRedirect func(string, []string) error
var setupProxy func(string, []string) error

// Kubernetes a K8s specific struct to hold config
type Kubernetes struct {
	K8sAPIRoot        string   `json:"k8s_api_root"`
	Kubeconfig        string   `json:"kubeconfig"`
	NodeName          string   `json:"node_name"`
	ExcludeNamespaces []string `json:"exclude_namespaces"`
	CniBinDir         string   `json:"cni_bin_dir"`
}

// PluginConf is whatever you expect your configuration json to be. This is whatever
// is passed in on stdin. Your plugin may wish to expose its functionality via
// runtime args, see CONVENTIONS.md in the CNI spec.
type PluginConf struct {
	types.NetConf // You may wish to not nest this type
	RuntimeConfig *struct {
		// SampleConfig map[string]interface{} `json:"sample"`
	} `json:"runtimeConfig"`

	// This is the previous result, when called in the context of a chained
	// plugin. Because this plugin supports multiple versions, we'll have to
	// parse this in two passes. If your plugin is not chained, this can be
	// removed (though you may wish to error if a non-chainable plugin is
	// chained.
	// If you need to modify the result before returning it, you will need
	// to actually convert it to a concrete versioned struct.
	RawPrevResult *map[string]interface{} `json:"prevResult"`
	PrevResult    *current.Result         `json:"-"`

	// Add plugin-specific flags here
	LogLevel   string     `json:"log_level"`
	Kubernetes Kubernetes `json:"kubernetes"`
}

// K8sArgs is the valid CNI_ARGS used for Kubernetes
// The field names need to match exact keys in kubelet args for unmarshalling
type K8sArgs struct {
	types.CommonArgs
	IP                         net.IP
	K8S_POD_NAME               types.UnmarshallableString // nolint: golint
	K8S_POD_NAMESPACE          types.UnmarshallableString // nolint: golint
	K8S_POD_INFRA_CONTAINER_ID types.UnmarshallableString // nolint: golint
}

// parseConfig parses the supplied configuration (and prevResult) from stdin.
func parseConfig(stdin []byte) (*PluginConf, error) {
	conf := PluginConf{}

	if err := json.Unmarshal(stdin, &conf); err != nil {
		return nil, fmt.Errorf("failed to parse network configuration: %v", err)
	}

	// Parse previous result. Remove this if your plugin is not chained.
	if conf.RawPrevResult != nil {
		resultBytes, err := json.Marshal(conf.RawPrevResult)
		if err != nil {
			return nil, fmt.Errorf("could not serialize prevResult: %v", err)
		}
		res, err := version.NewResult(conf.CNIVersion, resultBytes)
		if err != nil {
			return nil, fmt.Errorf("could not parse prevResult: %v", err)
		}
		conf.RawPrevResult = nil
		conf.PrevResult, err = current.NewResultFromResult(res)
		if err != nil {
			return nil, fmt.Errorf("could not convert result to current version: %v", err)
		}
	}
	// End previous result parsing

	return &conf, nil
}

// ConfigureLogging sets up logging using the provided log level,
func ConfigureLogging(logLevel string) {
	if strings.EqualFold(logLevel, "debug") {
		logrus.SetLevel(logrus.DebugLevel)
	} else if strings.EqualFold(logLevel, "info") {
		logrus.SetLevel(logrus.InfoLevel)
	} else {
		// Default level
		logrus.SetLevel(logrus.WarnLevel)
	}

	logrus.SetOutput(os.Stderr)
}

// cmdAdd is called for ADD requests
func cmdAdd(args *skel.CmdArgs) error {
	logrus.Info("istio-cni cmdAdd parsing config")
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}
	ConfigureLogging(conf.LogLevel)

	if conf.PrevResult == nil {
		logrus.Error("must be called as chained plugin")
		return fmt.Errorf("must be called as chained plugin")
	}

	logrus.WithFields(logrus.Fields{
		"version":    conf.CNIVersion,
		"prevResult": conf.PrevResult,
	}).Info("cmdAdd config parsed")

	// Determine if running under k8s by checking the CNI args
	k8sArgs := K8sArgs{}
	if err := types.LoadArgs(args.Args, &k8sArgs); err != nil {
		return err
	}
	logrus.Infof("Getting identifiers with arguments: %s", args.Args)
	logrus.Infof("Loaded k8s arguments: %v", k8sArgs)
	if conf.Kubernetes.CniBinDir != "" {
		nsSetupBinDir = conf.Kubernetes.CniBinDir
	}

	podName := string(k8sArgs.K8S_POD_NAME)
	podNamespace := string(k8sArgs.K8S_POD_NAMESPACE)
	podIP := conf.PrevResult.IPs[0].Address.IP.String()
	podSandboxID := string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID)

	logger := logrus.WithFields(logrus.Fields{
		"ContainerID":  args.ContainerID,
		"Pod":          podName,
		"Namespace":    podNamespace,
		"PodIP":        podIP,
		"PodSandboxID": podSandboxID,
	})

	// Check if the workload is running under Kubernetes.
	if podNamespace != "" && podName != "" {
		excludePod := false
		for _, excludeNs := range conf.Kubernetes.ExcludeNamespaces {
			if podNamespace == excludeNs {
				excludePod = true
				break
			}
		}
		if !excludePod {
			client, err := newKubeClient(*conf, logger)
			if err != nil {
				return err
			}
			logrus.WithField("client", client).Debug("Created Kubernetes client")
			containers, podUID, labels, annotations, ports, k8sErr := getKubePodInfo(client, podName, podNamespace)
			if k8sErr != nil {
				logger.Warnf("Error geting Pod data %v", k8sErr)
			}
			logger.Infof("Found containers %v", containers)

			logrus.WithFields(logrus.Fields{
				"ContainerID": args.ContainerID,
				"netns":       args.Netns,
				"pod":         podName,
				"Namespace":   podNamespace,
				"ports":       ports,
				"annotations": annotations,
			}).Infof("Checking annotations prior to redirect for Istio proxy")
			if val, ok := annotations[injectAnnotationKey]; ok {
				logrus.Infof("Pod %s contains inject annotation: %s", podName, val)
				if injectEnabled, err := strconv.ParseBool(val); err == nil {
					if !injectEnabled {
						logrus.Infof("Pod excluded due to inject-disabled annotation")
						excludePod = true
					}
				}
			}
			if !excludePod {
				logrus.Infof("setting up redirect")
				if redirect, redirErr := NewRedirect(ports, annotations, logger); redirErr != nil {
					logger.Errorf("Pod redirect failed due to bad params: %v", redirErr)
				} else {
					if setupRedirect != nil {
						_ = setupRedirect(args.Netns, ports)
					} else if err := redirect.doRedirect(args.Netns); err != nil {
						return err
					}
				}

				logger.Infof("Geting Secret %s in namespace %s", "istio.default", podNamespace)
				secretData, k8sErr := getKubeSecret(client, "istio.default", podNamespace) // TODO: get secret name
				if k8sErr != nil {
					logger.Warnf("Error geting Secret data %v", k8sErr)
				}

				logger.Info("Creating Proxy")
				if proxyAgent, redirErr := proxyagentclient.NewProxyAgentClient(agentURL); redirErr != nil {
					logger.Errorf("Creating proxy agent client failed: %v", redirErr)
				} else {
					logger.Info("Starting Proxy")
					if err := proxyAgent.StartProxy(podName, podNamespace, podUID, podIP, podSandboxID, secretData, labels, annotations); err != nil {
						logger.Errorf("Starting proxy failed: %v", err)
						return err
					}

					//ready := false
					//for !ready {
					//	ready, err = isReady(logger, podName, podNamespace, podIP, args.Netns)
					//	if err != nil {
					//		logger.Errorf("Could not perform readiness check: %v", err)
					//		return err
					//	}
					//	time.Sleep(2 * time.Second) // TODO: give up after some time
					//}
				}
			}
		} else {
			logger.Infof("Pod excluded")
		}
	} else {
		logger.Infof("No Kubernetes Data")
	}

	// Pass through the result for the next plugin
	return types.PrintResult(conf.PrevResult, conf.CNIVersion)
}

func isReady(logger *logrus.Entry, podName, podNamespace, podIP, netNS string) (bool, error) {
	ready := false

	err := ns.WithNetNSPath(netNS, func(hostNS ns.NetNS) error {
		//url := "http://" + request.PodIP + ":" + "15000" + "/server_info" // TODO: make port & path configurable
		url := "http://" + "127.0.0.1" + ":" + "15000" + "/server_info" // TODO: make port & path configurable
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return err
		}

		httpClient := http.Client{
			Timeout: 1 * time.Second,
		}
		response, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()

		if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusBadRequest {
			logger.Infof("Readiness probe succeeded for %s", podName)
			ready = true
			return nil
		}
		logger.Infof("Readiness probe failed for %s (%s): %v %s", podName, url, response.StatusCode, response.Status)
		return nil
	})

	return ready, err
}

func cmdGet(args *skel.CmdArgs) error {
	logrus.Info("cmdGet not implemented")
	// TODO: implement
	return fmt.Errorf("not implemented")
}

// cmdDel is called for DELETE requests
func cmdDel(args *skel.CmdArgs) error {
	logrus.Info("istio-cni cmdDel parsing config")
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}
	ConfigureLogging(conf.LogLevel)

	// Determine if running under k8s by checking the CNI args
	k8sArgs := K8sArgs{}
	if err := types.LoadArgs(args.Args, &k8sArgs); err != nil {
		return err
	}
	podName := string(k8sArgs.K8S_POD_NAME)
	podSandboxID := string(k8sArgs.K8S_POD_INFRA_CONTAINER_ID)

	// TODO: do we need to delete the proxy container or will the kubelet's GC delete it?
	if proxy, redirErr := proxyagentclient.NewProxyAgentClient(agentURL); redirErr != nil {
		logrus.Errorf("Creating proxy agent client failed: %v", redirErr)
	} else {
		logrus.Info("Stopping Proxy")
		if err := proxy.StopProxy(podName, podSandboxID); err != nil {
			logrus.Errorf("Stopping proxy failed: %v", err)
			return err
		}
	}

	// Do your delete here

	return nil
}

func main() {
	// Set up logging formatting.
	logrus.SetFormatter(&logutils.Formatter{})

	// Install a hook that adds file/line no information.
	logrus.AddHook(&logutils.ContextHook{})

	// TODO: implement plugin version
	skel.PluginMain(cmdAdd, cmdGet, cmdDel, version.All, "istio-cni")
}
