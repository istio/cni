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
	"net"
	"os"
	"strconv"

	"istio.io/api/annotation"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/sbezverk/nftableslib"
	"go.uber.org/zap"

	"istio.io/pkg/log"
)

var (
	nsSetupBinDir        = "/opt/cni/bin"
	injectAnnotationKey  = annotation.SidecarInject.Name
	sidecarStatusKey     = annotation.SidecarStatus.Name
	interceptRuleMgrType = defInterceptRuleMgrType
	loggingOptions       = log.DefaultOptions()
)

// Kubernetes a K8s specific struct to hold config
type Kubernetes struct {
	K8sAPIRoot           string   `json:"k8s_api_root"`
	Kubeconfig           string   `json:"kubeconfig"`
	InterceptRuleMgrType string   `json:"intercept_type"`
	NodeName             string   `json:"node_name"`
	ExcludeNamespaces    []string `json:"exclude_namespaces"`
	CniBinDir            string   `json:"cni_bin_dir"`
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
	K8S_POD_NAME               types.UnmarshallableString // nolint: golint, stylecheck
	K8S_POD_NAMESPACE          types.UnmarshallableString // nolint: golint, stylecheck
	K8S_POD_INFRA_CONTAINER_ID types.UnmarshallableString // nolint: golint, stylecheck
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

// cmdAdd is called for ADD requests
func cmdAdd(args *skel.CmdArgs) error {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		log.Errorf("istio-cni cmdAdd parsing config %v", err)
		return err
	}

	if conf.PrevResult == nil {
		log.Error("must be called as chained plugin")
		return fmt.Errorf("must be called as chained plugin")
	}

	log.Info("CmdAdd config parsed",
		zap.String("version", conf.CNIVersion),
		zap.Reflect("prevResult", conf.PrevResult))

	// Determine if running under k8s by checking the CNI args
	k8sArgs := K8sArgs{}
	if err := types.LoadArgs(args.Args, &k8sArgs); err != nil {
		return err
	}
	log.Infof("Getting identifiers with arguments: %s", args.Args)
	log.Infof("Loaded k8s arguments: %v", k8sArgs)
	if conf.Kubernetes.CniBinDir != "" {
		nsSetupBinDir = conf.Kubernetes.CniBinDir
	}
	if conf.Kubernetes.InterceptRuleMgrType != "" {
		interceptRuleMgrType = conf.Kubernetes.InterceptRuleMgrType
	}

	log.Info("",
		zap.String("ContainerID", args.ContainerID),
		zap.String("Pod", string(k8sArgs.K8S_POD_NAME)),
		zap.String("Namespace", string(k8sArgs.K8S_POD_NAMESPACE)),
		zap.String("InterceptType", interceptRuleMgrType))

	// Check if the workload is running under Kubernetes.
	if string(k8sArgs.K8S_POD_NAMESPACE) != "" && string(k8sArgs.K8S_POD_NAME) != "" {
		excludePod := false
		for _, excludeNs := range conf.Kubernetes.ExcludeNamespaces {
			if string(k8sArgs.K8S_POD_NAMESPACE) == excludeNs {
				excludePod = true
				break
			}
		}
		if !excludePod {
			client, err := newKubeClient(*conf)
			if err != nil {
				return err
			}
			log.Debug("Created Kubernetes client",
				zap.Reflect("client", client))
			containers, _, annotations, ports, k8sErr := getKubePodInfo(client, string(k8sArgs.K8S_POD_NAME), string(k8sArgs.K8S_POD_NAMESPACE))
			if k8sErr != nil {
				log.Warnf("Error getting Pod data %v", k8sErr)
			}
			log.Infof("Found containers %v", containers)
			if len(containers) > 1 {
				log.Info("Checking annotations prior to redirect for Istio proxy",
					zap.String("ContainerID", args.ContainerID),
					zap.String("netns", args.Netns),
					zap.String("pod", string(k8sArgs.K8S_POD_NAME)),
					zap.String("Namespace", string(k8sArgs.K8S_POD_NAMESPACE)),
					zap.Strings("ports", ports),
					zap.Reflect("annotations", annotations))
				if val, ok := annotations[injectAnnotationKey]; ok {
					log.Infof("Pod %s contains inject annotation: %s", string(k8sArgs.K8S_POD_NAME), val)
					if injectEnabled, err := strconv.ParseBool(val); err == nil {
						if !injectEnabled {
							log.Infof("Pod excluded due to inject-disabled annotation")
							excludePod = true
						}
					}
				}
				if _, ok := annotations[sidecarStatusKey]; !ok {
					log.Infof("Pod %s excluded due to not containing sidecar annotation", string(k8sArgs.K8S_POD_NAME))
					excludePod = true
				}
				if !excludePod {
					log.Infof("setting up redirect")
					if redirect, redirErr := NewRedirect(ports, annotations); redirErr != nil {
						log.Errorf("Pod redirect failed due to bad params: %v", redirErr)
					} else {
						// Check if nftables interception is selected, if it is the case, then must
						// verify if compute node supports. If it is not supported, fall back to iptables interception.
						if interceptRuleMgrType == "nftables" {
							if !nftableslib.IsNFTablesOn() {
								// Falling back to default intercepting mechanism
								interceptRuleMgrType = defInterceptRuleMgrType
							}
						}
						// Get the constructor for the configured type of InterceptRuleMgr
						interceptMgrCtor := GetInterceptRuleMgrCtor(interceptRuleMgrType)
						if interceptMgrCtor == nil {
							log.Errorf("Pod redirect failed due to unavailable InterceptRuleMgr of type %s",
								interceptRuleMgrType)
						} else {
							rulesMgr := interceptMgrCtor()
							if err := rulesMgr.Program(args.Netns, redirect); err != nil {
								return err
							}
						}
					}
				}
			}
		} else {
			log.Infof("Pod excluded")
		}
	} else {
		log.Infof("No Kubernetes Data")
	}

	// Pass through the result for the next plugin
	return types.PrintResult(conf.PrevResult, conf.CNIVersion)
}

func cmdGet(args *skel.CmdArgs) error {
	log.Info("cmdGet not implemented")
	// TODO: implement
	return fmt.Errorf("not implemented")
}

// cmdDel is called for DELETE requests
func cmdDel(args *skel.CmdArgs) error {
	log.Info("istio-cni cmdDel parsing config")
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}
	_ = conf

	// Do your delete here

	return nil
}

func main() {
	loggingOptions.OutputPaths = []string{"stderr"}
	loggingOptions.JSONEncoding = true
	if err := log.Configure(loggingOptions); err != nil {
		os.Exit(1)
	}
	// TODO: implement plugin version
	skel.PluginMain(cmdAdd, cmdGet, cmdDel, version.All, "istio-cni")
}
