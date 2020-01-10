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

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	client "k8s.io/client-go/kubernetes"

	"istio.io/pkg/log"
)

type Options struct {
	PodLabelKey   string `json:"pod_label_key"`
	PodLabelValue string `json:"pod_label_value"`
}

type Filters struct {
	NodeName                        string `json:"node_name"`
	SidecarAnnotation               string `json:"sidecar_annotation"`
	InitContainerName               string `json:"init_container_name"`
	InitContainerTerminationMessage string `json:"init_container_termination_message"`
	InitContainerExitCode           int    `json:"init_container_exit_code"`
	FieldSelectors                  string `json:"field_selectors"`
	LabelSelectors                  string `json:"label_selectors"`
}

// The pod reconciler struct. Contains state used to reconcile broken pods.
type BrokenPodReconciler struct {
	client  client.Interface
	Filters *Filters
	Options *Options
}

// Constructs a new BrokenPodReconciler struct.
func NewBrokenPodReconciler(client client.Interface, filters *Filters, options *Options) BrokenPodReconciler {
	return BrokenPodReconciler{
		client:  client,
		Filters: filters,
		Options: options,
	}
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
		if _, ok := labels[bpr.Options.PodLabelKey]; ok {
			log.Infof("Pod %s/%s already has label with key %s, skipping", pod.Namespace, pod.Name, bpr.Options.PodLabelKey)
			continue
		}

		if labels == nil {
			labels = map[string]string{}
		}

		log.Infof("Labeling pod %s/%s with label %s=%s", pod.Namespace, pod.Name, bpr.Options.PodLabelKey, bpr.Options.PodLabelValue)
		labels[bpr.Options.PodLabelKey] = bpr.Options.PodLabelValue
		pod.SetLabels(labels)

		if _, err = bpr.client.CoreV1().Pods(pod.Namespace).Update(&pod); err != nil {
			return
		}
	}
	return
}

func (bpr BrokenPodReconciler) CreateEventsForBrokenPods() error {
	// Get a list of all broken pods
	podList, err := bpr.ListBrokenPods()
	if err != nil {
		return err
	}

	eventList, err := bpr.ListEvents()
	if err != nil {
		return err
	}

	var loopErrors []error
	for _, pod := range podList.Items {
		if event, ok := eventList[pod.UID]; ok {
			log.Infof("Updating existing event for broken pod: %s/%s", pod.Namespace, pod.Name)
			event.LastTimestamp = metav1.Now()
			event.Count++
			_, err := bpr.client.CoreV1().Events(pod.Namespace).Update(&event)
			if err != nil {
				loopErrors = append(loopErrors, err)
			}
		} else {
			log.Infof("Creating event for broken pod: %s/%s", pod.Namespace, pod.Name)
			_, err := bpr.client.CoreV1().Events(pod.Namespace).Create(&v1.Event{
				Type: "Warning",
				ObjectMeta: metav1.ObjectMeta{
					GenerateName:      pod.Name,
					Namespace:         pod.Namespace,
					Generation:        0,
					CreationTimestamp: metav1.Now(),
					ClusterName:       pod.ClusterName,
					Labels: map[string]string{
						"istio-cni-daemonset-event": "true",
					},
				},
				InvolvedObject: v1.ObjectReference{
					Kind:            "pod",
					Namespace:       pod.Namespace,
					Name:            pod.Name,
					UID:             pod.UID,
					APIVersion:      pod.APIVersion,
					ResourceVersion: pod.ResourceVersion,
				},
				Reason:         "BrokenIstioCNI",
				Message:        "Pod detected with broken Istio CNI configuration.",
				FirstTimestamp: metav1.Now(),
				LastTimestamp:  metav1.Now(),
			})
			if err != nil {
				loopErrors = append(loopErrors, err)
			}
		}
	}

	if len(loopErrors) > 0 {
		lerr := loopErrors[0].Error()
		for _, err := range loopErrors[1:] {
			lerr = fmt.Sprintf("%s,%s", lerr, err.Error())
		}
		return fmt.Errorf("%s", lerr)
	}
	return nil
}

// Delete all pods detected as broken by ListPods
func (bpr BrokenPodReconciler) DeleteBrokenPods() error {
	// Get a list of all broken pods
	podList, err := bpr.ListBrokenPods()
	if err != nil {
		return err
	}

	var loopErrors []error
	for _, pod := range podList.Items {
		log.Infof("Deleting broken pod: %s/%s", pod.Namespace, pod.Name)
		if err := bpr.client.CoreV1().Pods(pod.Namespace).Delete(pod.Name, nil); err != nil {
			loopErrors = append(loopErrors, err)
		}
	}
	if len(loopErrors) > 0 {
		lerr := loopErrors[0].Error()
		for _, err := range loopErrors[1:] {
			lerr = fmt.Sprintf("%s,%s", lerr, err.Error())
		}
		return fmt.Errorf("%s", lerr)
	}
	return nil
}

func (bpr BrokenPodReconciler) ListEvents() (list map[types.UID]v1.Event, err error) {
	list = map[types.UID]v1.Event{}

	var rawList *v1.EventList
	rawList, err = bpr.client.CoreV1().Events("").List(metav1.ListOptions{
		LabelSelector: "istio-cni-daemonset-event=true",
	})
	if err != nil {
		return
	}
	for _, event := range rawList.Items {
		list[event.InvolvedObject.UID] = event
	}
	return
}

// Lists all pods identified as broken by our Filter criteria
func (bpr BrokenPodReconciler) ListBrokenPods() (list v1.PodList, err error) {

	var rawList *v1.PodList
	rawList, err = bpr.client.CoreV1().Pods("").List(metav1.ListOptions{
		LabelSelector: bpr.Filters.LabelSelectors,
		FieldSelector: bpr.Filters.FieldSelectors,
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
		trimmedTerminationMessage := strings.TrimSpace(bpr.Filters.InitContainerTerminationMessage)
		return trimmedTerminationMessage == "" || trimmedTerminationMessage == strings.TrimSpace(state.Message)
	}
	// Helper function; checks that container exit code matches filter
	matchExitCode := func(state *v1.ContainerStateTerminated) bool {
		// If we are filtering on init container exit code and the termination message does not match, exit
		if ec := bpr.Filters.InitContainerExitCode; ec == 0 || ec == int(state.ExitCode) {
			return true
		}
		return false
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
		// Skip the container if the InitContainerName is not a match and our
		// InitContainerName filter is non-empty.
		if bpr.Filters.InitContainerName != "" && container.Name != bpr.Filters.InitContainerName {
			continue
		}

		// For safety, check the containers *current* status. If the container
		// successfully exited, we NEVER want to identify this pod as broken.
		// If the pod is going to fail, the failure state will show up in
		// LastTerminationState eventually.
		if state := container.State.Terminated; state != nil {
			if state.Reason == "Completed" || state.ExitCode == 0 {
				continue
			}
		}

		// Check the LastTerminationState struct for information about why the container
		// last exited. If a pod is using the CNI configuration check init container,
		// it will start crashlooping and populate this struct.
		if state := container.LastTerminationState.Terminated; state != nil {
			// Verify the container state matches our filter criteria
			if matchTerminationMessage(state) && matchExitCode(state) {
				return true
			}
		}
	}
	return false
}
