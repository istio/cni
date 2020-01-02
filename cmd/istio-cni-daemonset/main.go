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
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	client "k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"istio.io/cni/pkg/repair"
	"istio.io/pkg/log"
)

type ControllerOptions struct {
	KubeConfigPath   string          `json:"kube_config_path"`
	DaemonPollPeriod int             `json:"daemon_poll_period"`
	RepairOptions    *repair.Options `json:"repair_options"`
	DeletePods       bool            `json:"delete_pods"`
	LabelPods        bool            `json:"label_pods"`
	RunAsDaemon      bool            `json:"run_as_daemon"`
}

var (
	loggingOptions = log.DefaultOptions()
)

// Parse command line options
func parseFlags() (filters *repair.Filters, options *ControllerOptions) {
	// Parse command line flags
	// Filter Options
	pflag.String("node-name", "", "The name of the node we are managing (will manage all nodes if unset)")
	pflag.String(
		"sidecar-annotation",
		"sidecar.istio.io/status",
		"An annotation key that indicates this pod contains an istio sidecar. All pods without this annotation will be ignored."+
			"The value of the annotation is ignored.")
	pflag.String(
		"init-container-name",
		"istio-init",
		"The name of the istio init container (will crash-loop if CNI is not configured for the pod)")
	pflag.String(
		"init-container-termination-message",
		"",
		"The expected termination message for the init container when crash-looping because of CNI misconfiguration")
	pflag.Int(
		"init-container-exit-code",
		126,
		"Expected exit code for the init container when crash-looping because of CNI misconfiguration")

	pflag.StringSlice("label-selectors", []string{}, "A set of label selectors in label=value format that will be added to the pod list filters")
	pflag.StringSlice("field-selectors", []string{}, "A set of field selectors in label=value format that will be added to the pod list filters")

	// Repair Options
	pflag.Bool("delete-pods", false, "Controller will delete pods")
	pflag.Bool("label-pods", false, "Controller will label pods")
	pflag.Bool("run-as-daemon", false, "Controller will run in a loop")
	pflag.Int("daemon-poll-period", 10, "Polling period for daemon (in seconds)")
	pflag.String(
		"broken-pod-label-key",
		"cni.istio.io/uninitialized",
		"The key portion of the label which will be set by the reconciler if --label-pods is true")
	pflag.String(
		"broken-pod-label-value",
		"true",
		"The value portion of the label which will be set by the reconciler if --label-pods is true")

	// Get kubernetes config
	if home := homedir.HomeDir(); home != "" {
		pflag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(option) absolute path to the kubeconfig file")
	} else {
		pflag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}

	pflag.Bool("help", false, "Print usage information")

	pflag.Parse()
	if err := viper.BindPFlags(pflag.CommandLine); err != nil {
		log.Fatal("Error parsing command line args: %+v")
	}

	if viper.GetBool("help") {
		pflag.Usage()
		os.Exit(0)
	}

	// Pull runtime args into structs
	filters = &repair.Filters{
		InitContainerName:               viper.GetString("init-container-name"),
		InitContainerTerminationMessage: viper.GetString("init-container-termination-message"),
		InitContainerExitCode:           viper.GetInt("init-container-exit-code"),
		SidecarAnnotation:               viper.GetString("sidecar-annotation"),
		FieldSelectors:                  &repair.KeyValueSelectorSet{},
		LabelSelectors:                  &repair.KeyValueSelectorSet{},
	}
	options = &ControllerOptions{
		DeletePods:       viper.GetBool("delete-pods"),
		RunAsDaemon:      viper.GetBool("run-as-daemon"),
		DaemonPollPeriod: viper.GetInt("daemon-poll-period"),
		LabelPods:        viper.GetBool("label-pods"),
		KubeConfigPath:   viper.GetString("kubeconfig"),
		RepairOptions: &repair.Options{
			PodLabelKey:   viper.GetString("broken-pod-label-key"),
			PodLabelValue: viper.GetString("broken-pod-label-value"),
		},
	}

	if nodeName := viper.GetString("node_name"); nodeName != "" {
		filters.FieldSelectors.AddSelectors(fmt.Sprintf("%s=%s", "spec.nodeName", nodeName))
	}
	for _, fieldselector := range viper.GetStringSlice("field-selectors") {
		filters.FieldSelectors.AddSelectors(fieldselector)
	}
	for _, labelselector := range viper.GetStringSlice("label-selectors") {
		filters.LabelSelectors.AddSelectors(labelselector)
	}

	return
}

// Set up Kubernetes client using kubeconfig (or in-cluster config if no file provided)
func clientSetup(options *ControllerOptions) (clientset *client.Clientset, err error) {
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

// Log human-readable output describing the current filter and option selection
func logCurrentOptions(bpr *repair.BrokenPodReconciler, options *ControllerOptions) {
	if options.RunAsDaemon {
		log.Infof("Controller Option: Running as a Daemon; will sleep %d seconds between passes", options.DaemonPollPeriod)
	}
	if options.LabelPods {
		log.Infof(
			"Controller Option: Labeling broken pods with label %s=%s",
			bpr.Options.PodLabelKey,
			bpr.Options.PodLabelValue,
		)
	}
	if options.DeletePods {
		log.Info("Controller Option: Deleting broken pods")
	}

	if bpr.Filters.SidecarAnnotation != "" {
		log.Infof("Filter option: Only managing pods with an annotation with key %s", bpr.Filters.SidecarAnnotation)
	}
	if bpr.Filters.FieldSelectors != nil {
		for _, fieldSelector := range bpr.Filters.FieldSelectors.KeyValueSelectors {
			log.Infof("Filter option: Only managing pods with field selector %s", fieldSelector)
		}
	}
	if bpr.Filters.LabelSelectors != nil {
		for _, labelSelector := range bpr.Filters.LabelSelectors.KeyValueSelectors {
			log.Infof("Filter option: Only managing pods with label selector %s", labelSelector)
		}
	}
	if bpr.Filters.InitContainerName != "" {
		log.Infof("Filter option: Only managing pods where init container is named %s", bpr.Filters.InitContainerName)
	}
	if bpr.Filters.InitContainerTerminationMessage != "" {
		log.Infof("Filter option: Only managing pods where init container termination message is %s", bpr.Filters.InitContainerTerminationMessage)
	}
	if bpr.Filters.InitContainerExitCode != 0 {
		log.Infof("Filter option: Only managing pods where init container exit status is %d", bpr.Filters.InitContainerExitCode)
	}
}

func main() {
	loggingOptions.OutputPaths = []string{"stderr"}
	loggingOptions.JSONEncoding = true
	if err := log.Configure(loggingOptions); err != nil {
		os.Exit(1)
	}

	filters, options := parseFlags()

	// TODO:(stewartbutler) This should probably use client-go/tools/cache at some
	//  point, but I'm not sure yet if it is needed.
	clientSet, err := clientSetup(options)
	if err != nil {
		log.Fatalf("Could not construct clientSet: %s", err)
	}

	podFixer := repair.NewBrokenPodReconciler(clientSet, filters, options.RepairOptions)
	logCurrentOptions(&podFixer, options)

	reconcile := func(bpr repair.BrokenPodReconciler) error {
		if options.LabelPods {
			if err := bpr.LabelBrokenPods(); err != nil {
				return err
			}
		}
		if options.DeletePods {
			if err := bpr.DeleteBrokenPods(); err != nil {
				return err
			}
		}
		return nil
	}

	if options.RunAsDaemon {
		for {
			if err := reconcile(podFixer); err != nil {
				log.Errorf("Error encountered in reconcile loop: %s", err)
			}
			time.Sleep(time.Second * time.Duration(options.DaemonPollPeriod))
		}
	} else if err := reconcile(podFixer); err != nil {
		log.Fatalf("Error encountered in reconcile: %s", err)
	}
}
