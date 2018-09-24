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

// This is a sample chained plugin that supports multiple CNI versions. It
// parses prevResult according to the cniVersion
package main

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/cni/pkg/version"

	"github.com/sirupsen/logrus"
	"github.com/projectcalico/libcalico-go/lib/logutils"
	"strings"
	"os"
	"os/exec"
)

var (
	NsSetupBinDir = "/opt/cni/bin"
	NsSetupProg = "istio-iptables.sh"
	RedirectToPort = "15001"
	NoRedirectUID = "1337"
	RedirectMode = "REDIRECT" // other Option TPROXY
	RedirectIpCidr = "*"
	RedirectExcludeIpCidr = ""
	RedirectExcludePort = "15020"
)

// Kubernetes a K8s specific struct to hold config
type Kubernetes struct {
	K8sAPIRoot string `json:"k8s_api_root"`
	Kubeconfig string `json:"kubeconfig"`
	NodeName   string `json:"node_name"`
}

// PluginConf is whatever you expect your configuration json to be. This is whatever
// is passed in on stdin. Your plugin may wish to expose its functionality via
// runtime args, see CONVENTIONS.md in the CNI spec.
type PluginConf struct {
	types.NetConf // You may wish to not nest this type
	RuntimeConfig *struct {
		SampleConfig map[string]interface{} `json:"sample"`
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

	// Add plugin-specifc flags here
	LogLevel             string            `json:"log_level"`
	Kubernetes           Kubernetes        `json:"kubernetes"`
}

// K8sArgs is the valid CNI_ARGS used for Kubernetes
type K8sArgs struct {
	types.CommonArgs
	IP                         net.IP
	K8S_POD_NAME               types.UnmarshallableString
	K8S_POD_NAMESPACE          types.UnmarshallableString
	K8S_POD_INFRA_CONTAINER_ID types.UnmarshallableString
}

func setupRedirect(netns string, ports []string) error {
	netnsArg := fmt.Sprintf("--net=%s", netns)
	nsSetupExecutable := fmt.Sprintf("%s/%s", NsSetupBinDir, NsSetupProg)
	nsenterArgs := []string{
		netnsArg,
		nsSetupExecutable,
		"-p", RedirectToPort,
		"-u", NoRedirectUID,
		"-m", RedirectMode,
		"-i", RedirectIpCidr,
		"-b", strings.Join(ports, ","),
		"-d", RedirectExcludePort,
		"-x", RedirectExcludeIpCidr,
	}
	logrus.WithFields(logrus.Fields{
		"nsenterArgs": nsenterArgs,
	}).Info("nsenter args")
	out, err := exec.Command("nsenter", nsenterArgs...).CombinedOutput()
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"out": out,
			"err": err,
		}).Errorf("nsenter failed: %v", err)
		logrus.Debugf("nsenter out: %s", out)
	} else {
		logrus.Debugf("nsenter done: %s", out)
	}
	return err
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

// Set up logging using the provided log level,
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
		"version": conf.CNIVersion,
		"prevResult": conf.PrevResult,
	}).Info("cmdAdd config parsed")

	// Determine if running under k8s by checking the CNI args
	k8sArgs := K8sArgs{}
	if err := types.LoadArgs(args.Args, &k8sArgs); err != nil {
		return err
	}
	logrus.Infof("Getting WEP identifiers with arguments: %s", args.Args)
	logrus.Infof("Loaded k8s arguments: %v", k8sArgs)

	var logger *logrus.Entry
	logger = logrus.WithFields(logrus.Fields{
			"ContainerID":      args.ContainerID,
			"Pod":              string(k8sArgs.K8S_POD_NAME),
			"Namespace":        string(k8sArgs.K8S_POD_NAMESPACE),
	})

	// Check if the workload is running under Kubernetes.
	if string(k8sArgs.K8S_POD_NAMESPACE) != "" && string(k8sArgs.K8S_POD_NAME) != "" {
		client, err := NewK8sClient(*conf, logger)
		if err != nil {
			return err
		}
		logrus.WithField("client", client).Debug("Created Kubernetes client")
		containers, _, _, ports, k8sErr := GetK8sPodInfo(client, string(k8sArgs.K8S_POD_NAME), string(k8sArgs.K8S_POD_NAMESPACE))
		if k8sErr != nil {
			logger.Warnf("Error geting Pod data %v", k8sErr)
		}
		for _, container := range containers {
			if container == "istio-proxy" {
				logger.Info("Found an istio-proxy container")
				if len(containers) > 1 {
					logrus.WithFields(logrus.Fields{
						"ContainerID":      args.ContainerID,
						"netns": args.Netns,
						"pod": string(k8sArgs.K8S_POD_NAME),
						"Namespace":        string(k8sArgs.K8S_POD_NAMESPACE),
						"ports": ports,
					}).Info("Updating iptables redirect for Istio proxy")

					_ = setupRedirect(args.Netns, ports)
				}
			}
		}
	} else {
		logger.Info("No Kubernetes Data")
	}

	// Pass through the result for the next plugin
	return types.PrintResult(conf.PrevResult, conf.CNIVersion)
}

// cmdDel is called for DELETE requests
func cmdDel(args *skel.CmdArgs) error {
	logrus.Info("istio-cni cmdDel parsing config")
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}
	ConfigureLogging(conf.LogLevel)
	_ = conf

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

func cmdGet(args *skel.CmdArgs) error {
	logrus.Info("cmdGet not implemented")
	// TODO: implement
	return fmt.Errorf("not implemented")
}
