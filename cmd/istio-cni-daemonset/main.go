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

// A simple daemonset binary to repair pods that are crashlooping
// after winning a race condition against istio-cni
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"istio.io/pkg/log"
	"k8s.io/api/core/v1"
	client "k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	loggingOptions = log.DefaultOptions()
)

type Filters struct {
	NodeName                        string            `json:"node_name"`
	InitContainerName               string            `json:"init_container_name"`
	InitContainerTerminationMessage string            `json:"init_container_termination_message"`
	InitContainerExitCode           int               `json:"init_container_exit_code"`
	FieldSelectors                  *FieldSelectorSet `json:"field_selectors"`
}

type ControllerOptions struct {
	DeletePods       bool   `json:"delete_pods"`
	KubeConfigPath   string `json:"kube_config_path"`
	LabelPods        bool   `json:"label_pods"`
	PodLabelKey      string `json:"pod_label_key"`
	PodLabelValue    string `json:"pod_label_value"`
	RunAsDaemon      bool   `json:"run_as_daemon"`
	DaemonPollPeriod int    `json:"daemon_poll_period"`
}

func parseFlags() (filter Filters, controlopts ControllerOptions) {
	// Parse command line flags
	// Filter Options
	flag.String("node-name", "", "The name of the node we are managing (will manage all nodes if unset)")
	flag.String("init-container-name", "istio-init", "The name of the istio init container (will crash-loop if CNI is not configured for the pod)")
	flag.String("init-container-termination-message", "", "The expected termination message for the init container when crash-looping because of CNI misconfiguration")
	flag.Int("init-container-exit-code", 126, "Expected exit code for the init container when crash-looping because of CNI misconfiguration")

	// Repair Options
	flag.Bool("delete-pods", false, "Controller will delete pods")
	flag.Bool("label-pods", false, "Controller will label pods")
	flag.Bool("run-as-daemon", false, "Controller will run in a loop")
	flag.Int("daemon-poll-period", 10, "Polling period for daemon (in seconds)")
	flag.String("broken-pod-label-key", "cni.istio.io/uninitialized", "The key portion of the label which will be set by the reconciler if --label-pods is true")
	flag.String("broken-pod-label-value", "true", "The value portion of the label which will be set by the reconciler if --label-pods is true")

	// Get kubernetes config
	if home := homedir.HomeDir(); home != "" {
		flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(option) absolute path to the kubeconfig file")
	} else {
		flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	if err := viper.BindPFlags(pflag.CommandLine); err != nil {
		log.Fatal("Error parsing command line args: %+v")
	}

	// Pull runtime args into structs
	filter = Filters{
		InitContainerName:               viper.GetString("init-container-name"),
		InitContainerTerminationMessage: viper.GetString("init-container-termination-message"),
		InitContainerExitCode:           viper.GetInt("init-container-exit-code"),
		FieldSelectors:                  &FieldSelectorSet{},
	}
	controlopts = ControllerOptions{
		DeletePods:       viper.GetBool("delete-pods"),
		RunAsDaemon:      viper.GetBool("run-as-daemon"),
		DaemonPollPeriod: viper.GetInt("daemon-poll-period"),
		LabelPods:        viper.GetBool("label-pods"),
		PodLabelKey:      viper.GetString("broken-pod-label-key"),
		PodLabelValue:    viper.GetString("broken-pod-label-value"),
		KubeConfigPath:   viper.GetString("kubeconfig"),
	}

	filter.FieldSelectors.addFieldSelectorIfNotEmpty("spec.nodeName", viper.GetString("node_name"))

	return
}

func clientSetup(options ControllerOptions) (clientset *client.Clientset, err error) {
	var kubeConfig *rest.Config
	// Set up client
	if options.KubeConfigPath == "" {
		kubeConfig, err = rest.InClusterConfig()
		if err != nil {
			return
		}
	} else {
		kubeConfig, err = clientcmd.BuildConfigFromFlags("", options.KubeConfigPath)
		if err != nil {
			return
		}
	}

	clientset, err = client.NewForConfig(kubeConfig)
	return
}

func main() {
	var (
		filters = Filters{}
		options = ControllerOptions{}
	)

	loggingOptions.OutputPaths = []string{"stderr"}
	loggingOptions.JSONEncoding = true
	if err := log.Configure(loggingOptions); err != nil {
		os.Exit(1)
	}

	filters, options = parseFlags()
	logCurrentOptions(filters, options)

	// TODO:(stewartbutler) This should probably use client-go/tools/cache at some
	//  point, but I'm not sure yet if it is needed.
	clientSet, err := clientSetup(options)
	if err != nil {
		log.Fatalf("Could not construct clientSet: %s", err)
	}
	podFixer := brokenPodReconciler{client: *clientSet}

	if options.RunAsDaemon {
		var wg sync.WaitGroup
		wg.Add(1)
		go func(wg sync.WaitGroup) {
			defer wg.Done()
			for {
				if err := podFixer.Reconcile(filters, options); err != nil {
					log.Errorf("Error encountered in reconcile loop: %s", err)
				}
				time.Sleep(time.Second * time.Duration(options.DaemonPollPeriod))
			}
		}(wg)
		wg.Wait()
	} else {
		if err := podFixer.Reconcile(filters, options); err != nil {
			log.Fatalf("Error encountered in reconcile: %s", err)
		}
	}

}

// Log all current settings
func logCurrentOptions(filters Filters, options ControllerOptions) {

	if options.RunAsDaemon {
		log.Infof("Controller Option: Running as a Daemon; will sleep %d seconds between passes", options.DaemonPollPeriod)
	}
	if options.LabelPods {
		log.Infof(
			"Controller Option: Labeling broken pods with label %s=%s",
			options.PodLabelKey,
			options.PodLabelValue,
		)
	}
	if options.DeletePods {
		log.Info("Controller Option: Deleting broken pods")
	}

	if filters.NodeName != "" {
		log.Infof("Filter option: Only managing pods on node %s", filters.NodeName)
	}
	if filters.InitContainerName != "" {
		log.Infof("Filter option: Only managing pods where init container is named %s", filters.InitContainerName)
	}
	if filters.InitContainerTerminationMessage != "" {
		log.Infof("Filter option: Only managing pods where init container termination message is %s", filters.InitContainerTerminationMessage)
	}
	if filters.InitContainerExitCode != 0 {
		log.Infof("Filter option: Only managing pods where init container exit status is %d", filters.InitContainerExitCode)
	}
}

type brokenPodReconciler struct {
	client client.Clientset
}

func (bpr brokenPodReconciler) Reconcile(filters Filters, options ControllerOptions) (err error) {

	if options.LabelPods {
		log.Info("Labeling broken pods")
		if err := bpr.labelBrokenPods(filters, options); err != nil {
			log.Errorf("Failed to label pods: %s", err)
			return err
		}
	}

	if options.DeletePods {
		log.Info("Deleting broken pods")
		if err := bpr.deleteBrokenPods(filters, options); err != nil {
			log.Errorf("Failed to delete pods: %s", err)
			return err
		}
	}

	return
}

// Label all pods detected as broken by ListPods with a customizable label
func (bpr brokenPodReconciler) labelBrokenPods(filters Filters, options ControllerOptions) (err error) {
	// Get a list of all broken pods
	podList, err := bpr.listBrokenPods(filters, options)
	if err != nil {
		return
	}

	for _, pod := range podList.Items {
		labels := pod.GetLabels()
		if _, ok := labels[options.PodLabelKey]; ok {
			log.Infof("Pod %s/%s already has label with key %s, skipping", pod.Namespace, pod.Name, options.PodLabelKey)
			continue
		} else {
			log.Infof("Labeling pod %s/%s with label %s=%s", pod.Namespace, pod.Name, options.PodLabelKey, options.PodLabelValue)
			labels[options.PodLabelKey] = options.PodLabelValue
			pod.SetLabels(labels)
		}
		if _, err = bpr.client.CoreV1().Pods(pod.Namespace).Update(&pod); err != nil {
			return
		}
	}
	return
}

// Delete all pods detected as broken by ListPods
func (bpr brokenPodReconciler) deleteBrokenPods(filters Filters, options ControllerOptions) error {
	// Get a list of all broken pods
	var podList, err = bpr.listBrokenPods(filters, options)
	if err != nil {
		return err
	}

	for _, pod := range podList.Items {
		log.Infof("Deleting broken pod: %s/%s", pod.Namespace, pod.Name)
		if err := bpr.client.CoreV1().Pods(pod.Namespace).Delete(pod.Name, nil); err != nil {
			return err
		}
	}
	return nil
}

func (bpr brokenPodReconciler) listBrokenPods(filters Filters, options ControllerOptions) (list v1.PodList, err error) {

	var rawList *v1.PodList
	rawList, err = bpr.client.CoreV1().Pods("").List(metav1.ListOptions{
		TypeMeta:            metav1.TypeMeta{},
		LabelSelector:       "",
		FieldSelector:       fmt.Sprintf("%s", filters.FieldSelectors),
		Watch:               false,
		AllowWatchBookmarks: false,
		ResourceVersion:     "",
		TimeoutSeconds:      nil,
		Limit:               0,
		Continue:            "",
	})
	if err != nil {
		return
	}

	list.Items = []v1.Pod{}
	for _, pod := range rawList.Items {
		if detectPod(pod, filters) {
			list.Items = append(list.Items, pod)
		}
	}

	return
}

// After initial filtering, detect whether this pod is broken due to the CNI
// race condition.
func detectPod(pod v1.Pod, filters Filters) bool {
	// Only check pods that have the sidecar annotation; the rest can be
	// ignored.
	if _, ok := pod.ObjectMeta.Annotations["sidecar.istio.io/status"]; ok {
		// For each candidate pod, iterate across all init containers searching for
		// crashlooping init containers that match our criteria
		for _, container := range pod.Status.InitContainerStatuses {
			// First check that the init container name matches our expected value
			if container.Name == filters.InitContainerName {
				// Next check that the container terminated
				if state := container.LastTerminationState.Terminated; state != nil {
					// Finally, check that the container state matches our filter criteria
					// (namely, that the exit code and termination message look right)
					if checkTerminationState(filters, *state) {
						return true
					}
				}
			}
		}
	}
	return false
}

// This func checks the state of a container in a pod and returns true if the container
// matches the defined filter criteria
func checkTerminationState(filter Filters, state v1.ContainerStateTerminated) bool {
	// If we are filtering on init container termination message and the termination message of 'state' does not match, exit
	if s := strings.TrimSpace(filter.InitContainerTerminationMessage); s != "" && s != strings.TrimSpace(state.Message) {
		return false
	}

	// If we are filtering on init container exit code and the termination message does not match, exit
	if ec := filter.InitContainerExitCode; ec != 0 && ec != int(state.ExitCode) {
		return false
	}

	return true
}

// A struct wrapping a slice of field selector strings, used for convenience in
// constructing a list of field selectors
type FieldSelectorSet struct {
	FieldSelectors []string
}

func (a FieldSelectorSet) addFieldSelector(field string, value string) {
	a.FieldSelectors = append(a.FieldSelectors, fmt.Sprintf("%s=%s", field, value))
	return
}

func (a FieldSelectorSet) addFieldSelectorIfNotEmpty(field string, value string) {
	if value != "" {
		a.addFieldSelector(field, value)
	}
}

func (a FieldSelectorSet) String() string {
	return strings.Join(a.FieldSelectors, ",")
}
