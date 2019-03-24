package main

import (
	"flag"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/workqueue"
	"os"
	"path/filepath"
	"time"
)

func main() {
	kubeconfig, err := GetKubeConfig()
	if err != nil {
		panic(err)
	}

	kubeclient, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		panic(err)
	}

	timeoutConfig, exist := os.LookupEnv("MAX_TIMEOUT")
	if !exist {
		panic("MAX_TIMEOUT must be set")
	}

	duration, err := time.ParseDuration(timeoutConfig)
	if err != nil {
		panic(err)
	}

	if duration < 0 {
		panic("MAX_TIMEOUT cannot be less than 0 seconds")
	}

	listWatcher := cache.NewListWatchFromClient(kubeclient.CoreV1().RESTClient(), "nodes", metav1.NamespaceAll, fields.Everything())

	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	indexer, informer := cache.NewIndexerInformer(listWatcher, &corev1.Node{}, time.Minute, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
		UpdateFunc: func(old interface{}, new interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(new)
			if err == nil {
				queue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			// IndexerInformer uses a delta queue, therefore for deletes we have to use this
			// key function.
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
	}, cache.Indexers{})

	controller := NewController(indexer, queue, informer, kubeclient, duration)

	stop := make(chan struct{})
	defer close(stop)
	go controller.Run(1, stop)

	select {}
}

func GetKubeConfig() (*rest.Config, error) {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Infof("unable to get in cluster config due to %v", err)
		log.Infof("trying to use local config")
		config, err = newKubeClientFromOutsideCluster()
		if err != nil {
			log.Errorf("unable to retrive the local config due to %v", err)
			log.Panicf("failed to find a valid cluster config")
		}
	}

	return config, err
}

func newKubeClientFromOutsideCluster() (*rest.Config, error) {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		log.Errorf("error creating default client config: %s", err)
		return nil, err
	}
	return config, err
}
