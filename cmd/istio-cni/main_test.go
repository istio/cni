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

package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/plugins/pkg/testutils"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
)

var (
	ifname           = "eth0"
	sandboxDirectory = "/tmp"
	currentVersion   = "0.3.0"
	k8Args           = "K8S_POD_NAMESPACE=istio-system;K8S_POD_NAME=testPodName"
	invalidVersion   = "0.1.0"

	getKubePodInfoCalled = false
	nsenterFuncCalled    = false

	testContainers  = []string{"mockContainer"}
	testLabels      = map[string]string{}
	testAnnotations = map[string]string{}
	testPorts       = []string{"9080"}
)

var conf = `{
    "cniVersion": "%s",
	"name": "istio-plugin-sample-test",
	"type": "sample",
    "capabilities": {
        "testCapability": false
    },
    "ipam": {
        "type": "testIPAM"
    },
    "dns": {
        "nameservers": ["testNameServer"],
        "domain": "testDomain",
        "search": ["testSearch"],
        "options": ["testOption"]
    },
    "prevResult": {
        "cniversion": "0.3.0",
        "interfaces": [
            {
                "name": "%s",
                "sandbox": "%s"
            }
        ],
        "ips": [
            {
                "version": "4",
                "address": "10.0.0.2/24",
                "gateway": "10.0.0.1",
                "interface": 0
            }
        ],
        "routes": []

    },
    "log_level": "debug",
    "kubernetes": {
        "k8s_api_root": "APIRoot",
        "kubeconfig": "testK8sConfig",
        "node_name": "testNodeName",
        "exclude_namespaces": ["testExcludeNS"],
        "cni_bin_dir": "/testDirectory"
    }
    }`

func mockNsenterRedirect(netns string, ports []string) error {
	nsenterFuncCalled = true
	return nil
}

func mocknewK8sClient(conf PluginConf, logger *logrus.Entry) (*kubernetes.Clientset, error) {
	var cs kubernetes.Clientset

	getKubePodInfoCalled = true

	return &cs, nil
}

func mockgetK8sPodInfo(client *kubernetes.Clientset, podName, podNamespace string) (containers []string,
	labels map[string]string, annotations map[string]string, ports []string, err error) {

	containers = testContainers
	labels = testLabels
	annotations = testAnnotations
	ports = testPorts

	return containers, labels, annotations, ports, nil
}

func resetGlobalTestVariables() {
	getKubePodInfoCalled = false
	nsenterFuncCalled = false

	testContainers = []string{"mockContainer"}
	testLabels = map[string]string{}
	testAnnotations = map[string]string{}
	testPorts = []string{"9080"}

	setupRedirect = nil
	testAnnotations[sidecarStatusKey] = "true"
}

func testSetArgs(stdinData string) *skel.CmdArgs {
	return &skel.CmdArgs{
		ContainerID: "testContainerID",
		Netns:       sandboxDirectory,
		IfName:      ifname,
		Args:        k8Args,
		Path:        "/tmp",
		StdinData:   []byte(stdinData),
	}
}

func testCmdInvalidVersion(t *testing.T, f func(args *skel.CmdArgs) error) {
	cniConf := fmt.Sprintf(conf, invalidVersion, ifname, sandboxDirectory)
	args := testSetArgs(cniConf)

	err := f(args)
	if err != nil {
		if !strings.Contains(err.Error(), "could not convert result to current version") {
			t.Fatalf("expected substring error 'could not convert result to current version', got: %v", err)
		}
	} else {
		t.Fatalf("expected failed CNI version, got: no error")
	}
}

func testCmdAdd(t *testing.T) {
	newKubeClient = mocknewK8sClient
	getKubePodInfo = mockgetK8sPodInfo

	cniConf := fmt.Sprintf(conf, currentVersion, ifname, sandboxDirectory)
	args := testSetArgs(cniConf)

	result, _, err := testutils.CmdAddWithResult(
		sandboxDirectory, ifname, []byte(cniConf), func() error { return cmdAdd(args) })

	if err != nil {
		t.Fatalf("failed with error: %v", err)
	}

	if result.Version() != current.ImplementedSpecVersion {
		t.Fatalf("failed with invalid version, expected: %v got:%v",
			current.ImplementedSpecVersion, result.Version())
	}
}

// Validate k8sArgs struct works for unmarshalling kubelet args
func TestLoadArgs(t *testing.T) {
	kubeletArgs := "IgnoreUnknown=1;K8S_POD_NAMESPACE=istio-system;" +
		"K8S_POD_NAME=istio-sidecar-injector-8489cf78fb-48pvg;" +
		"K8S_POD_INFRA_CONTAINER_ID=3c41e946cf17a32760ff86940a73b06982f1815e9083cf2f4bfccb9b7605f326"

	k8sArgs := K8sArgs{}
	if err := types.LoadArgs(kubeletArgs, &k8sArgs); err != nil {
		t.Fatalf("LoadArgs failed with error: %v", err)
	}

	if string(k8sArgs.K8S_POD_NAMESPACE) == "" || string(k8sArgs.K8S_POD_NAME) == "" {
		t.Fatalf("LoadArgs didn't convert args properly, K8S_POD_NAME=\"%s\";K8S_POD_NAMESPACE=\"%s\"",
			string(k8sArgs.K8S_POD_NAME), string(k8sArgs.K8S_POD_NAMESPACE))
	}
}

func TestCmdAdd(t *testing.T) {
	defer resetGlobalTestVariables()

	testCmdAdd(t)
}

func TestCmdAddTwoContainersWithAnnotation(t *testing.T) {
	defer resetGlobalTestVariables()

	testContainers = []string{"mockContainer", "mockContainer2"}
	testAnnotations[injectAnnotationKey] = "false"

	testCmdAdd(t)
}

func TestCmdAddTwoContainers(t *testing.T) {
	defer resetGlobalTestVariables()

	setupRedirect = mockNsenterRedirect
	testAnnotations[injectAnnotationKey] = "true"
	testContainers = []string{"mockContainer", "mockContainer2"}

	testCmdAdd(t)

	if !nsenterFuncCalled {
		t.Fatalf("expected nsenterFunc to be called")
	}
}

func TestCmdAddTwoContainersWithoutSideCar(t *testing.T) {
	defer resetGlobalTestVariables()

	delete(testAnnotations, sidecarStatusKey)
	testContainers = []string{"mockContainer", "mockContainer2"}
	testCmdAdd(t)

	if nsenterFuncCalled {
		t.Fatalf("Didnt Expect nsenterFunc to be called because this pod does not contain a sidecar")
	}
}

func TestCmdAddExcludePod(t *testing.T) {
	defer resetGlobalTestVariables()

	k8Args = "K8S_POD_NAMESPACE=testExcludeNS;K8S_POD_NAME=testPodName"
	getKubePodInfoCalled = false

	testCmdAdd(t)

	if getKubePodInfoCalled == true {
		t.Fatalf("failed to exclude pod")
	}
}

func TestCmdAddInvalidK8sArgsKeyword(t *testing.T) {
	defer resetGlobalTestVariables()

	k8Args = "K8S_POD_NAMESPACE_InvalidKeyword=istio-system"

	cniConf := fmt.Sprintf(conf, currentVersion, ifname, sandboxDirectory)
	args := testSetArgs(cniConf)

	err := cmdAdd(args)
	if err != nil {
		if !strings.Contains(err.Error(), "unknown args [\"K8S_POD_NAMESPACE_InvalidKeyword") {
			t.Fatalf(`expected substring "unknown args ["K8S_POD_NAMESPACE_InvalidKeyword, got: %v`, err)
		}
	} else {
		t.Fatalf("expected a failed response for an invalid K8sArgs setting, got: no error")
	}
}

func TestCmdAddInvalidVersion(t *testing.T) {
	testCmdInvalidVersion(t, cmdAdd)
}

func TestCmdAddNoPrevResult(t *testing.T) {
	var confNoPrevResult = `{
    "cniVersion": "0.3.0",
	"name": "istio-plugin-sample-test",
	"type": "sample",
    "runtimeconfig": {
         "sampleconfig": []
    },
    "loglevel": "debug",
    "kubernetes": {
        "k8sapiroot": "APIRoot",
        "kubeconfig": "testK8sConfig",
        "nodename": "testNodeName",
        "excludenamespaces": "testNS",
        "cnibindir": "/testDirectory"
    }
    }`

	args := testSetArgs(confNoPrevResult)

	err := cmdAdd(args)
	if err != nil {
		if !strings.Contains(err.Error(), "must be called as chained plugin") {
			t.Fatalf("expected substring error 'must be called as chained plugin', got: %v", err)
		}
	} else {
		t.Fatalf("expected failed no PrevResult, got: no error")
	}
}

func TestCmdDel(t *testing.T) {
	cniConf := fmt.Sprintf(conf, currentVersion, ifname, sandboxDirectory)
	args := testSetArgs(cniConf)

	err := cmdDel(args)
	if err != nil {
		t.Fatalf("failed with error: %v", err)
	}
}

func TestCmdDelInvalidVersion(t *testing.T) {
	testCmdInvalidVersion(t, cmdDel)
}
