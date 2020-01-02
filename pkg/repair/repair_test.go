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
	"reflect"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

type makePodArgs struct {
	PodName             string
	Namespace           string
	Labels              map[string]string
	Annotations         map[string]string
	InitContainerName   string
	InitContainerStatus *v1.ContainerStatus
	NodeName            string
}

func makePod(args makePodArgs) *v1.Pod {
	pod := &v1.Pod{
		TypeMeta: v12.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: v12.ObjectMeta{
			Name:        args.PodName,
			Namespace:   args.Namespace,
			Labels:      args.Labels,
			Annotations: args.Annotations,
		},
		Spec: v1.PodSpec{
			NodeName: args.NodeName,
			Volumes:  nil,
			InitContainers: []v1.Container{
				{
					Name: args.InitContainerName,
				},
			},
			Containers: []v1.Container{
				{
					Name: "payload-container",
				},
			},
		},
		Status: v1.PodStatus{
			InitContainerStatuses: []v1.ContainerStatus{
				*args.InitContainerStatus,
			},
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name: "payload-container",
					State: v1.ContainerState{
						Waiting: &v1.ContainerStateWaiting{
							Reason: "PodInitializing",
						},
					},
				},
			},
		},
	}
	return pod
}

// Container specs
var (
	brokenInitContainerWaiting = v1.ContainerStatus{
		Name: "istio-init",
		State: v1.ContainerState{
			Waiting: &v1.ContainerStateWaiting{
				Reason:  "CrashLoopBackOff",
				Message: "Back-off 5m0s restarting failed blah blah blah",
			},
		},
		LastTerminationState: v1.ContainerState{
			Terminated: &v1.ContainerStateTerminated{
				ExitCode: 126,
				Reason:   "Error",
				Message:  "Died for some reason",
			},
		},
	}

	brokenInitContainerTerminating = v1.ContainerStatus{
		Name: "istio-init",
		State: v1.ContainerState{
			Terminated: &v1.ContainerStateTerminated{
				ExitCode: 126,
				Reason:   "Error",
				Message:  "Died for some reason",
			},
		},
		LastTerminationState: v1.ContainerState{
			Terminated: &v1.ContainerStateTerminated{
				ExitCode: 126,
				Reason:   "Error",
				Message:  "Died for some reason",
			},
		},
	}

	workingInitContainerDiedPreviously = v1.ContainerStatus{
		Name: "istio-init",
		State: v1.ContainerState{
			Terminated: &v1.ContainerStateTerminated{
				ExitCode: 0,
				Reason:   "Completed",
			},
		},
		LastTerminationState: v1.ContainerState{
			Terminated: &v1.ContainerStateTerminated{
				ExitCode: 126,
				Reason:   "Error",
				Message:  "Died for some reason",
			},
		},
	}

	workingInitContainer = v1.ContainerStatus{
		Name: "istio-init",
		State: v1.ContainerState{
			Terminated: &v1.ContainerStateTerminated{
				ExitCode: 0,
				Reason:   "Completed",
			},
		},
	}
)

// Pod specs
var (
	brokenPodTerminating = *makePod(makePodArgs{
		PodName: "BrokenPodTerminating",
		Annotations: map[string]string{
			"sidecar.istio.io/status": "something",
		},
		Labels: map[string]string{
			"testlabel": "true",
		},
		NodeName:            "TestNode",
		InitContainerStatus: &brokenInitContainerTerminating,
	})

	brokenPodWaiting = *makePod(makePodArgs{
		PodName: "BrokenPodWaiting",
		Annotations: map[string]string{
			"sidecar.istio.io/status": "something",
		},
		InitContainerStatus: &brokenInitContainerWaiting,
	})

	brokenPodNoAnnotation = *makePod(makePodArgs{
		PodName:             "BrokenPodNoAnnotation",
		InitContainerStatus: &brokenInitContainerWaiting,
	})

	workingPod = *makePod(makePodArgs{
		PodName: "WorkingPod",
		Annotations: map[string]string{
			"sidecar.istio.io/status": "something",
		},
		InitContainerStatus: &workingInitContainer,
	})

	workingPodDiedPreviously = *makePod(makePodArgs{
		PodName: "WorkingPodDiedPreviously",
		Annotations: map[string]string{
			"sidecar.istio.io/status": "something",
		},
		InitContainerStatus: &workingInitContainerDiedPreviously,
	})
)

func TestBrokenPodReconciler_detectPod(t *testing.T) {
	makeDetectPod := func(name string, terminationMessage string, exitCode int) *v1.Pod {
		return makePod(makePodArgs{
			PodName:     name,
			Annotations: map[string]string{"sidecar.istio.io/status": "something"},
			InitContainerStatus: &v1.ContainerStatus{
				Name: "istio-init",
				State: v1.ContainerState{
					Waiting: &v1.ContainerStateWaiting{
						Reason:  "CrashLoopBackOff",
						Message: "Back-off 5m0s restarting failed blah blah blah",
					},
				},
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{
						Message:  terminationMessage,
						ExitCode: int32(exitCode),
					},
				},
			},
		})
	}

	type fields struct {
		Filters *Filters
		Options *Options
	}
	type args struct {
		pod v1.Pod
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   bool
	}{
		{
			"Testing OK pod with only ExitCode check",
			fields{
				&Filters{
					SidecarAnnotation:     "sidecar.istio.io/status",
					InitContainerName:     "istio-init",
					InitContainerExitCode: 126,
				},
				&Options{},
			},
			args{pod: workingPod},
			false,
		},
		{
			"Testing working pod (that previously died) with only ExitCode check",
			fields{
				&Filters{
					SidecarAnnotation:     "sidecar.istio.io/status",
					InitContainerName:     "istio-init",
					InitContainerExitCode: 126,
				},
				&Options{},
			},
			args{pod: workingPodDiedPreviously},
			false,
		},
		{
			"Testing broken pod (in waiting state) with only ExitCode check",
			fields{
				&Filters{
					SidecarAnnotation:     "sidecar.istio.io/status",
					InitContainerName:     "istio-init",
					InitContainerExitCode: 126,
				},
				&Options{},
			},
			args{pod: brokenPodWaiting},
			true,
		},
		{
			"Testing broken pod (in terminating state) with only ExitCode check",
			fields{
				&Filters{
					SidecarAnnotation:     "sidecar.istio.io/status",
					InitContainerName:     "istio-init",
					InitContainerExitCode: 126,
				},
				&Options{},
			},
			args{pod: brokenPodTerminating},
			true,
		},
		{
			"Testing broken pod with wrong ExitCode",
			fields{
				&Filters{
					SidecarAnnotation:     "sidecar.istio.io/status",
					InitContainerName:     "istio-init",
					InitContainerExitCode: 55,
				},
				&Options{},
			},
			args{pod: brokenPodWaiting},
			false,
		},
		{
			"Testing broken pod with no annotation (should be ignored)",
			fields{
				&Filters{
					SidecarAnnotation:     "sidecar.istio.io/status",
					InitContainerName:     "istio-init",
					InitContainerExitCode: 126,
				},
				&Options{},
			},
			args{pod: brokenPodNoAnnotation},
			false,
		},
		{
			"Check termination message match false",
			fields{
				&Filters{
					SidecarAnnotation:               "sidecar.istio.io/status",
					InitContainerName:               "istio-init",
					InitContainerTerminationMessage: "Termination Message",
				},
				&Options{},
			},
			args{
				pod: *makeDetectPod(
					"TerminationMessageMatchFalse",
					"This Does Not Match",
					0),
			},
			false,
		},
		{
			"Check termination message match true",
			fields{
				&Filters{
					SidecarAnnotation:               "sidecar.istio.io/status",
					InitContainerName:               "istio-init",
					InitContainerTerminationMessage: "Termination Message",
				},
				&Options{},
			},
			args{
				pod: *makeDetectPod(
					"TerminationMessageMatchTrue",
					"Termination Message",
					0),
			},
			true,
		},
		{
			"Check termination message match true for trailing and leading space",
			fields{
				&Filters{
					SidecarAnnotation:               "sidecar.istio.io/status",
					InitContainerName:               "istio-init",
					InitContainerTerminationMessage: "            Termination Message",
				},
				&Options{},
			},
			args{
				pod: *makeDetectPod(
					"TerminationMessageMatchTrueLeadingSpace",
					"Termination Message              ",
					0),
			},
			true,
		},
		{
			"Check termination code match false",
			fields{
				&Filters{
					SidecarAnnotation:     "sidecar.istio.io/status",
					InitContainerName:     "istio-init",
					InitContainerExitCode: 126,
				},
				&Options{},
			},
			args{
				pod: *makeDetectPod(
					"TerminationCodeMatchFalse",
					"",
					121),
			},
			false,
		},
		{
			"Check termination code match true",
			fields{
				&Filters{
					SidecarAnnotation:     "sidecar.istio.io/status",
					InitContainerName:     "istio-init",
					InitContainerExitCode: 126,
				},
				&Options{},
			},
			args{
				pod: *makeDetectPod(
					"TerminationCodeMatchTrue",
					"",
					126),
			},
			true,
		},
		{
			"Check badly formatted pod",
			fields{
				&Filters{
					SidecarAnnotation:     "sidecar.istio.io/status",
					InitContainerName:     "istio-init",
					InitContainerExitCode: 126,
				},
				&Options{},
			},
			args{
				pod: *makePod(makePodArgs{
					PodName:             "Test",
					Annotations:         map[string]string{"sidecar.istio.io/status": "something"},
					InitContainerStatus: &v1.ContainerStatus{},
				}),
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bpr := BrokenPodReconciler{
				client:  fake.NewSimpleClientset(),
				Filters: tt.fields.Filters,
				Options: tt.fields.Options,
			}
			if got := bpr.detectPod(tt.args.pod); got != tt.want {
				t.Errorf("detectPod() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Test the ListBrokenPods function
// Note: The fake client does NOT support filtering by field selector, so that
// is not tested.
func TestBrokenPodReconciler_listBrokenPods(t *testing.T) {
	type fields struct {
		client  kubernetes.Interface
		Filters *Filters
		Options *Options
	}
	tests := []struct {
		name     string
		fields   fields
		wantList v1.PodList
	}{
		{
			name: "No broken pods",
			fields: fields{
				client: fake.NewSimpleClientset(&workingPodDiedPreviously, &workingPod),
				Filters: &Filters{
					SidecarAnnotation:               "sidecar.istio.io/status",
					InitContainerName:               "istio-init",
					InitContainerTerminationMessage: "Died for some reason",
					InitContainerExitCode:           126,
				},
				Options: &Options{},
			},
			wantList: v1.PodList{Items: []v1.Pod{}},
		},
		{
			name: "With broken pods (including one with bad annotation)",
			fields: fields{
				client: fake.NewSimpleClientset(&workingPodDiedPreviously, &workingPod, &brokenPodWaiting, &brokenPodNoAnnotation, &brokenPodTerminating),
				Filters: &Filters{
					SidecarAnnotation:               "sidecar.istio.io/status",
					InitContainerName:               "istio-init",
					InitContainerTerminationMessage: "Died for some reason",
					InitContainerExitCode:           126,
				},
				Options: &Options{},
			},
			wantList: v1.PodList{Items: []v1.Pod{brokenPodWaiting, brokenPodTerminating}},
		},
		{
			name: "With Label Selector",
			fields: fields{
				client: fake.NewSimpleClientset(&workingPodDiedPreviously, &workingPod, &brokenPodWaiting, &brokenPodNoAnnotation, &brokenPodTerminating),
				Filters: &Filters{
					SidecarAnnotation:               "sidecar.istio.io/status",
					InitContainerName:               "istio-init",
					InitContainerTerminationMessage: "Died for some reason",
					InitContainerExitCode:           126,
					LabelSelectors:                  &KeyValueSelectorSet{KeyValueSelectors: []string{"testlabel=true"}},
				},
				Options: &Options{},
			},
			wantList: v1.PodList{Items: []v1.Pod{brokenPodTerminating}},
		},
		{
			name: "With alternate sidecar annotation",
			fields: fields{
				client: fake.NewSimpleClientset(&workingPodDiedPreviously, &workingPod, &brokenPodWaiting, &brokenPodNoAnnotation, &brokenPodTerminating),
				Filters: &Filters{
					SidecarAnnotation:               "some.other.sidecar/annotation",
					InitContainerName:               "istio-init",
					InitContainerTerminationMessage: "Died for some reason",
					InitContainerExitCode:           126,
					LabelSelectors:                  &KeyValueSelectorSet{KeyValueSelectors: []string{"testlabel=true"}},
				},
				Options: &Options{},
			},
			wantList: v1.PodList{Items: []v1.Pod{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bpr := BrokenPodReconciler{
				client:  tt.fields.client,
				Filters: tt.fields.Filters,
				Options: tt.fields.Options,
			}
			var gotList, err = bpr.ListBrokenPods()
			if err != nil {
				t.Errorf("ListBrokenPods() got error listing pods: %v", err)
				return
			}
			if gotItems := gotList.Items; gotItems != nil {
				if !reflect.DeepEqual(gotItems, tt.wantList.Items) {
					t.Errorf("ListBrokenPods() gotList = %v, want %v", gotItems, tt.wantList.Items)
				}
			}
		})
	}
}

// Testing constructor
func TestNewBrokenPodReconciler(t *testing.T) {
	var (
		client  = fake.NewSimpleClientset()
		filter  = Filters{}
		options = Options{}
	)

	type args struct {
		client  kubernetes.Interface
		filters *Filters
		options *Options
	}
	tests := []struct {
		name    string
		args    args
		wantBpr BrokenPodReconciler
	}{
		{
			name: "Constructor test",
			args: args{
				client:  client,
				filters: &filter,
				options: &options,
			},
			wantBpr: BrokenPodReconciler{
				client:  client,
				Filters: &filter,
				Options: &options,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if gotBpr := NewBrokenPodReconciler(tt.args.client, tt.args.filters, tt.args.options); !reflect.DeepEqual(gotBpr, tt.wantBpr) {
				t.Errorf("NewBrokenPodReconciler() = %v, want %v", gotBpr, tt.wantBpr)
			}
		})
	}
}

func labelBrokenPodsClientset(pods ...v1.Pod) (cs kubernetes.Interface) {
	var csPods []runtime.Object

	for _, pod := range pods {
		csPods = append(csPods, pod.DeepCopy())
	}
	cs = fake.NewSimpleClientset(csPods...)
	return
}

func makePodLabelMap(pods []v1.Pod) (podmap map[string]string) {
	podmap = map[string]string{}
	for _, pod := range pods {
		podmap[pod.Name] = ""
		for key, value := range pod.Labels {
			podmap[pod.Name] = strings.Join([]string{podmap[pod.Name], fmt.Sprintf("%s=%s", key, value)}, ",")
		}
		podmap[pod.Name] = strings.Trim(podmap[pod.Name], " ,")
	}
	return
}

func TestBrokenPodReconciler_labelBrokenPods(t *testing.T) {
	type fields struct {
		client  kubernetes.Interface
		Filters *Filters
		Options *Options
	}
	tests := []struct {
		name       string
		fields     fields
		wantLabels map[string]string
		wantErr    bool
	}{
		{
			name: "No broken pods",
			fields: fields{
				client: labelBrokenPodsClientset(workingPod, workingPodDiedPreviously),
				Filters: &Filters{
					InitContainerName:               "istio-init",
					InitContainerExitCode:           126,
					InitContainerTerminationMessage: "Died for some reason",
				},
				Options: &Options{PodLabelKey: "testkey", PodLabelValue: "testval"},
			},
			wantLabels: map[string]string{"WorkingPod": "", "WorkingPodDiedPreviously": ""},
			wantErr:    false,
		},
		{
			name: "With broken pods",
			fields: fields{
				client: labelBrokenPodsClientset(workingPod, workingPodDiedPreviously, brokenPodWaiting),
				Filters: &Filters{
					InitContainerName:               "istio-init",
					InitContainerExitCode:           126,
					InitContainerTerminationMessage: "Died for some reason",
				},
				Options: &Options{PodLabelKey: "testkey", PodLabelValue: "testval"},
			},
			wantLabels: map[string]string{"WorkingPod": "", "WorkingPodDiedPreviously": "", "BrokenPodWaiting": "testkey=testval"},
			wantErr:    false,
		},
		{
			name: "With already labeled pod",
			fields: fields{
				client: labelBrokenPodsClientset(workingPod, workingPodDiedPreviously, brokenPodTerminating),
				Filters: &Filters{
					InitContainerName:               "istio-init",
					InitContainerExitCode:           126,
					InitContainerTerminationMessage: "Died for some reason",
				},
				Options: &Options{PodLabelKey: "testlabel", PodLabelValue: "true"},
			},
			wantLabels: map[string]string{"WorkingPod": "", "WorkingPodDiedPreviously": "", "BrokenPodTerminating": "testlabel=true"},
			wantErr:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bpr := BrokenPodReconciler{
				client:  tt.fields.client,
				Filters: tt.fields.Filters,
				Options: tt.fields.Options,
			}
			if err := bpr.LabelBrokenPods(); (err != nil) != tt.wantErr {
				t.Errorf("LabelBrokenPods() error = %v, wantErr %v", err, tt.wantErr)
			} else {
				havePods, err := bpr.client.CoreV1().Pods("").List(metav1.ListOptions{})
				if err != nil {
					t.Errorf("LabelBrokenPods() error = %v when listing pods", err)
				}
				plm := makePodLabelMap(havePods.Items)
				if !reflect.DeepEqual(plm, tt.wantLabels) {
					t.Errorf("LabelBrokenPods() haveLabels = %v, wantLabels = %v", plm, tt.wantLabels)
				}
			}
		})
	}
}

func TestKeyValueSelectorSet_AddSelectors(t *testing.T) {
	type fields struct {
		KeyValueSelectors []string
	}
	type args struct {
		selectors []string
	}
	tests := []struct {
		name       string
		fields     fields
		args       args
		wantString string
	}{
		{
			name:       "Adding a selector",
			fields:     fields{KeyValueSelectors: []string{}},
			args:       args{selectors: []string{"a=test", "b=test"}},
			wantString: "a=test,b=test",
		},
		{
			name:       "Adding empty selectors",
			fields:     fields{KeyValueSelectors: []string{}},
			args:       args{selectors: []string{}},
			wantString: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &KeyValueSelectorSet{
				KeyValueSelectors: tt.fields.KeyValueSelectors,
			}
			a.AddSelectors(tt.args.selectors...)
			if a.String() != tt.wantString {
				t.Errorf("AddSelectors() haveSelectors = %v, wantSelectors = %v", a.String(), tt.wantString)
			}
		})
	}
}

func TestBrokenPodReconciler_deleteBrokenPods(t *testing.T) {
	type fields struct {
		client  kubernetes.Interface
		Filters *Filters
		Options *Options
	}
	tests := []struct {
		name     string
		fields   fields
		wantErr  bool
		wantPods []v1.Pod
	}{
		{
			name: "No broken pods",
			fields: fields{
				client: labelBrokenPodsClientset(workingPod, workingPodDiedPreviously),
				Filters: &Filters{
					InitContainerName:               "istio-init",
					InitContainerExitCode:           126,
					InitContainerTerminationMessage: "Died for some reason",
				},
				Options: &Options{},
			},
			wantPods: []v1.Pod{workingPod, workingPodDiedPreviously},
			wantErr:  false,
		},
		{
			name: "With broken pods",
			fields: fields{
				client: labelBrokenPodsClientset(workingPod, workingPodDiedPreviously, brokenPodWaiting),
				Filters: &Filters{
					InitContainerName:               "istio-init",
					InitContainerExitCode:           126,
					InitContainerTerminationMessage: "Died for some reason",
				},
				Options: &Options{},
			},
			wantPods: []v1.Pod{workingPod, workingPodDiedPreviously},
			wantErr:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bpr := BrokenPodReconciler{
				client:  tt.fields.client,
				Filters: tt.fields.Filters,
				Options: tt.fields.Options,
			}
			if err := bpr.DeleteBrokenPods(); (err != nil) != tt.wantErr {
				t.Errorf("DeleteBrokenPods() error = %v, wantErr %v", err, tt.wantErr)
			}
			havePods, err := bpr.client.CoreV1().Pods("").List(metav1.ListOptions{})
			if err != nil {
				t.Errorf("DeleteBrokenPods() error listing pods: %v", err)
			}
			if !reflect.DeepEqual(havePods.Items, tt.wantPods) {
				t.Errorf("DeleteBrokenPods() error havePods = %v, wantPods = %v", havePods.Items, tt.wantPods)
			}

		})
	}
}
