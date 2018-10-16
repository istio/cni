// Copyright 2018 Istio authors
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

// This tests the k8s installation.  It validates the CNI plugin configuration
// and the existence of the CNI plugin binary locations.
package install_test

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	//"github.com/nsf/jsondiff"
)

var (
	PreConfDir      = "data/pre"
	ExpectedConfDir = "data/expected"
	TestWorkDir, _  = os.Getwd()
	Hub             = "docker.io/tiswanso"
	Tag             = "v0.1-cleanup"
)

type testCase struct {
	name                  string
	preConfFile           string
	resultFileName        string
	expectedOutputFile    string
	expectedPostCleanFile string
}

func doTest(tc testCase, t *testing.T) {
	cmd := exec.Command(TestWorkDir+"/../deployments/kubernetes/install/test/test-install-cni.sh",
		"1", tc.preConfFile, tc.resultFileName, tc.expectedOutputFile, tc.expectedPostCleanFile)
	cmd.Env = append(os.Environ(), fmt.Sprintf("HUB=%s", Hub), fmt.Sprintf("TAG=%s", Tag))
	output, err := cmd.Output()
	if err != nil {
		t.Errorf("Error code: %v", err)
		t.Errorf("Failed test result: %s", output)
		t.Fail()
	}
}

func TestInstall(t *testing.T) {
	envHub := os.Getenv("HUB")
	if envHub != "" {
		Hub = envHub
	}
	envTag := os.Getenv("TAG")
	if envTag != "" {
		Tag = envTag
	}
	testDataDir := TestWorkDir + "/../deployments/kubernetes/install/test/data"
	cases := []testCase{
		{
			name:                  "First file with pre-plugins",
			preConfFile:           "NONE",
			resultFileName:        "10-calico.conflist",
			expectedOutputFile:    testDataDir + "/expected/10-calico.conflist-istioconfig",
			expectedPostCleanFile: "",
		},
		{
			name:                  "File with pre-plugins",
			preConfFile:           "10-calico.conflist",
			resultFileName:        "10-calico.conflist",
			expectedOutputFile:    testDataDir + "/expected/10-calico.conflist-istioconfig",
			expectedPostCleanFile: "",
		},
		{
			name:                  "File without pre-plugins",
			preConfFile:           "minikube_cni.conf",
			resultFileName:        "minikube_cni.conflist",
			expectedOutputFile:    testDataDir + "/expected/minikube_cni.conflist.expected",
			expectedPostCleanFile: testDataDir + "/expected/minikube_cni.conflist.clean",
		},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("case %d %s", i, c.name), func(t *testing.T) {
			t.Logf("Test preconf %s, expected %s", c.preConfFile, c.expectedOutputFile)
			doTest(c, t)
		})
	}
}
