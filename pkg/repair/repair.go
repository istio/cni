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

package repair

import (
	"fmt"
	"strings"

	"istio.io/pkg/log"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	client "k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

type Options struct {
	PodLabelKey   string `json:"pod_label_key"`
	PodLabelValue string `json:"pod_label_value"`
}

type Filters struct {
	NodeName                        string               `json:"node_name"`
	SidecarAnnotation               string               `json:"sidecar_annotation"`
	InitContainerName               string               `json:"init_container_name"`
	InitContainerTerminationMessage string               `json:"init_container_termination_message"`
	InitContainerExitCode           int                  `json:"init_container_exit_code"`
	FieldSelectors                  *KeyValueSelectorSet `json:"field_selectors"`
	LabelSelectors                  *KeyValueSelectorSet `json:"label_selectors"`
}

// A safe getter for the FieldSelectors field of a Filters struct.
// If the FieldSelectors struct is nil, it will create an empty one.
func (f Filters) GetFieldSelectors() *KeyValueSelectorSet {
	if f.FieldSelectors == nil {
		f.FieldSelectors = &KeyValueSelectorSet{}
	}
	return f.FieldSelectors
}

// A safe getter for the LabelSelectors field of a Filters struct.
// If the LabelSelectors struct is nil, it will create an empty one.
func (f Filters) GetLabelSelectors() *KeyValueSelectorSet {
	if f.LabelSelectors == nil {
		f.LabelSelectors = &KeyValueSelectorSet{}
	}
	return f.LabelSelectors
}

// A struct wrapping a slice of field selector strings, used for convenience in
// constructing a list of field selectors
type KeyValueSelectorSet struct {
	KeyValueSelectors []string
}

// Adds one or more selectors in format key=value to a KeyValueSelectorSet
func (a *KeyValueSelectorSet) AddSelectors(selectors ...string) {
	for _, selector := range selectors {
		if selector != "" {
			a.KeyValueSelectors = append(a.KeyValueSelectors, selector)
		}
	}
	return
}

// Returns a stringified KeyValueSelectorSet
func (a *KeyValueSelectorSet) String() string {
	return strings.Join(a.KeyValueSelectors, ",")
}

// The pod reconciler struct. Contains state used to reconcile broken pods.
type BrokenPodReconciler struct {
	client  client.Interface
	Filters *Filters
	Options *Options
}

// Constructs a new BrokenPodReconciler struct.
func NewBrokenPodReconciler(client client.Interface, filters *Filters, options *Options) (bpr BrokenPodReconciler) {
	bpr = BrokenPodReconciler{
		client:  client,
		Filters: filters,
		Options: options,
	}
	return
}

// Label all pods detected as broken by ListPods with a customizable label
func (bpr BrokenPodReconciler) LabelBrokenPods() (err error) {
	// Get a list of all broken pods
	podList, err := bpr.ListBrokenPods()
	if err != nil {
		return
	}

	for _, pod := range podList.Items {
		labels := pod.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		if _, ok := labels[bpr.Options.PodLabelKey]; ok {
			log.Infof("Pod %s/%s already has label with key %s, skipping", pod.Namespace, pod.Name, bpr.Options.PodLabelKey)
			continue
		} else {
			log.Infof("Labeling pod %s/%s with label %s=%s", pod.Namespace, pod.Name, bpr.Options.PodLabelKey, bpr.Options.PodLabelValue)
			labels[bpr.Options.PodLabelKey] = bpr.Options.PodLabelValue
			pod.SetLabels(labels)
		}
		if _, err = bpr.client.CoreV1().Pods(pod.Namespace).Update(&pod); err != nil {
			return
		}
	}
	return
}

// Delete all pods detected as broken by ListPods
func (bpr BrokenPodReconciler) DeleteBrokenPods() error {
	// Get a list of all broken pods
	podList, err := bpr.ListBrokenPods()
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

// Lists all pods identified as broken by our Filter criteria
func (bpr BrokenPodReconciler) ListBrokenPods() (list v1.PodList, err error) {

	var rawList *v1.PodList
	rawList, err = bpr.client.CoreV1().Pods("").List(metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s", bpr.Filters.GetLabelSelectors()),
		FieldSelector: fmt.Sprintf("%s", bpr.Filters.GetFieldSelectors()),
	})
	if err != nil {
		return
	}

	list.Items = []v1.Pod{}
	for _, pod := range rawList.Items {
		if bpr.detectPod(pod) {
			list.Items = append(list.Items, pod)
		}
	}

	return
}

// Given a pod, returns 'true' if the pod is a match to the BrokenPodReconciler filter criteria.
func (bpr BrokenPodReconciler) detectPod(pod v1.Pod) bool {
	// Helper function; checks that a container's termination message matches filter
	matchTerminationMessage := func(state *v1.ContainerStateTerminated) bool {
		// If we are filtering on init container termination message and the termination message of 'state' does not match, exit
		if s := strings.TrimSpace(bpr.Filters.InitContainerTerminationMessage); s == "" || s == strings.TrimSpace(state.Message) {
			return true
		} else {
			return false
		}
	}
	// Helper function; checks that container exit code matches filter
	matchExitCode := func(state *v1.ContainerStateTerminated) bool {
		// If we are filtering on init container exit code and the termination message does not match, exit
		if ec := bpr.Filters.InitContainerExitCode; ec == 0 || ec == int(state.ExitCode) {
			return true
		} else {
			return false
		}
	}

	// Only check pods that have the sidecar annotation; the rest can be
	// ignored.
	if bpr.Filters.SidecarAnnotation != "" {
		if _, ok := pod.ObjectMeta.Annotations[bpr.Filters.SidecarAnnotation]; !ok {
			return false
		}
	}

	// For each candidate pod, iterate across all init containers searching for
	// crashlooping init containers that match our criteria
	for _, container := range pod.Status.InitContainerStatuses {
		// Skip the container if the InitContainerName is not a match
		if bpr.Filters.InitContainerName != "" && container.Name != bpr.Filters.InitContainerName {
			continue
		}

		// Check the containers *current* status. If it is *currently* passing, skip.
		// If it is currently in a matching failed state , return true.
		if state := container.State.Terminated; state != nil {
			if state.Reason == "Completed" || state.ExitCode == 0 {
				continue
			} else {
				// Check if other conditions match
				if matchTerminationMessage(state) && matchExitCode(state) {
					return true
				}
			}
		}
		// Next check that the container previously terminated due to our error
		if state := container.LastTerminationState.Terminated; state != nil {
			// Finally, check that the container state matches our filter criteria
			// (namely, that the exit code and termination message look right)
			if matchTerminationMessage(state) &&
				matchExitCode(state) {
				return true
			}
		}
	}
	return false
}
