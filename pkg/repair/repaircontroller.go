package repair

import (
	"fmt"
	"strings"
	"time"

	"istio.io/pkg/log"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	client "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"k8s.io/client-go/util/workqueue"
)

type RepairController struct {
	clientset     client.Interface
	workQueue     workqueue.RateLimitingInterface
	podController cache.Controller

	reconciler BrokenPodReconciler
}

func NewRepairController(reconciler BrokenPodReconciler) (*RepairController, error) {
	c := &RepairController{
		clientset:  reconciler.client,
		workQueue:  workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
		reconciler: reconciler,
	}

	podListWatch := cache.NewFilteredListWatchFromClient(
		c.clientset.CoreV1().RESTClient(),
		"pods",
		metav1.NamespaceAll,
		func(options *metav1.ListOptions) {
			labelSelectors := []string{}
			fieldSelectors := []string{}

			for _, ls := range []string{options.LabelSelector, reconciler.Filters.LabelSelectors} {
				if ls != "" {
					labelSelectors = append(labelSelectors, ls)
				}
			}
			for _, fs := range []string{options.FieldSelector, reconciler.Filters.LabelSelectors} {
				if fs != "" {
					fieldSelectors = append(fieldSelectors, fs)
				}
			}
			options.LabelSelector = strings.Join(labelSelectors, ",")
			options.FieldSelector = strings.Join(fieldSelectors, ",")
		},
	)

	_, c.podController = cache.NewInformer(podListWatch, &v1.Pod{}, 0, cache.ResourceEventHandlerFuncs{
		AddFunc: func(newObj interface{}) {
			c.workQueue.AddRateLimited(newObj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.workQueue.AddRateLimited(newObj)
		},
	})

	return c, nil
}

func (rc *RepairController) Run(stopCh <-chan struct{}) {
	go rc.podController.Run(stopCh)
	if !cache.WaitForCacheSync(stopCh, rc.podController.HasSynced) {
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	go wait.Until(
		func() {
			for rc.processNextItem() {
			}
		}, time.Second,
		stopCh,
	)

	<-stopCh
	log.Infof("Stopping repair controller.")
}

func (rc *RepairController) processNextItem() bool {
	obj, quit := rc.workQueue.Get()
	if quit {
		return false
	}
	defer rc.workQueue.Done(obj)

	pod, ok := obj.(*v1.Pod)
	if !ok {
		log.Errorf("Error decoding object, invalid type. Dropping.")
		rc.workQueue.Forget(obj)
		return true
	}

	err := rc.reconciler.ReconcilePod(*pod)

	if err == nil {
		log.Debugf("Removing %s/%s from work queue", pod.Namespace, pod.Name)
		rc.workQueue.Forget(obj)
	} else if rc.workQueue.NumRequeues(obj) < 5 {
		log.Errorf("Error: %s", err)
		log.Infof("Re-adding %s/%s to work queue", pod.Namespace, pod.Name)
		rc.workQueue.AddRateLimited(obj)
	} else {
		log.Infof("Requeue limit reached, removing %s/%s", pod.Namespace, pod.Name)
		rc.workQueue.Forget(obj)
		runtime.HandleError(err)
	}

	return true
}
