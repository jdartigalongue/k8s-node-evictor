package main

import (
	"fmt"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"time"
)

type Controller struct {
	indexer             cache.Indexer
	queue               workqueue.RateLimitingInterface
	informer            cache.Controller
	kubeClient          *kubernetes.Clientset
	maxNotReadyDuration time.Duration
}

func NewController(indexer cache.Indexer, queue workqueue.RateLimitingInterface, informer cache.Controller, kubeClient *kubernetes.Clientset, maxNotReadyDuration time.Duration) *Controller {
	return &Controller{indexer: indexer, queue: queue, informer: informer, kubeClient: kubeClient, maxNotReadyDuration: maxNotReadyDuration}
}

func (c *Controller) Run(threadiness int, stopCh chan struct{}) {
	defer runtime.HandleCrash()

	// Let the workers stop when we are done
	defer c.queue.ShutDown()
	logrus.Info("Starting k8s-node-evictor controller")

	go c.informer.Run(stopCh)

	// Wait for all involved caches to be synced, before processing items from the queue is started
	if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh
	logrus.Info("Stopping k8s-node-evictor controller")
}

func (c *Controller) runWorker() {
	for c.processNextItem() {
	}
}

func (c *Controller) processNextItem() bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	// Tell the queue that we are done with processing this key. This unblocks the key for other workers
	// This allows safe parallel processing because two pods with the same key are never processed in
	// parallel.
	defer c.queue.Done(key)

	// Invoke the method containing the business logic
	err := c.evictor(key.(string))

	// Handle the error if something went wrong during the execution of the business logic
	c.handleErr(err, key)
	return true
}

func (c *Controller) evictor(key string) error {
	obj, exists, err := c.indexer.GetByKey(key)
	if err != nil {
		logrus.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if !exists {
		fmt.Printf("Node %s has been deleted\n", key)
	} else {
		for _, v := range obj.(*corev1.Node).Status.Conditions {
			if v.Type == corev1.NodeReady && v.Status != corev1.ConditionTrue {
				logrus.Infof("node not ready: %s", obj.(*corev1.Node).GetName())
				if v.LastHeartbeatTime.Time.Before(time.Now().Add(-c.maxNotReadyDuration)) {
					logrus.Warnf("deleting nodes: %s", obj.(*corev1.Node).GetName())
					err = c.kubeClient.CoreV1().Nodes().Delete(obj.(*corev1.Node).GetName(), &metav1.DeleteOptions{})
					if err != nil {
						return err
					}
					break
				}
			}
		}
	}

	return nil
}

// handleErr checks if an error happened and makes sure we will retry later.
func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		// Forget about the #AddRateLimited history of the key on every successful synchronization.
		// This ensures that future processing of updates for this key is not delayed because of
		// an outdated error history.
		c.queue.Forget(key)
		return
	}

	// This controller retries 5 times if something goes wrong. After that, it stops trying.
	if c.queue.NumRequeues(key) < 5 {
		logrus.Infof("Error syncing pod %v: %v", key, err)

		// Re-enqueue the key rate limited. Based on the rate limiter on the
		// queue and the re-enqueue history, the key will be processed later again.
		c.queue.AddRateLimited(key)
		return
	}

	c.queue.Forget(key)
	// Report to an external entity that, even after several retries, we could not successfully process this key
	runtime.HandleError(err)
	logrus.Infof("Dropping pod %q out of the queue: %v", key, err)
}
