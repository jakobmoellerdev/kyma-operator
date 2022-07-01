package dynamic

import (
	"context"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"strings"
	"time"
)

type ComponentInformer struct {
	schema.GroupVersionResource
	source.Informer
}

func (ci *ComponentInformer) String() string {
	return ci.GroupVersionResource.String()
}

func Informers(mgr manager.Manager, gv schema.GroupVersion) (map[string]source.Source, error) {
	c, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, err
	}

	informerFactory := dynamicinformer.NewDynamicSharedInformerFactory(c, time.Minute*30)
	err = mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		informerFactory.Start(ctx.Done())
		return nil
	}))
	if err != nil {
		return nil, err
	}

	//TODO maybe replace with native REST Handling
	cs, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, err
	}
	resources, err := cs.ServerResourcesForGroupVersion(gv.String())
	if client.IgnoreNotFound(err) != nil {
		return nil, err
	}

	if err != nil {
		return nil, err
	}

	dynamicInformerSet := make(map[string]source.Source)
	for _, resource := range resources.APIResources {
		//TODO Verify if this filtering is really necessary or if we can somehow only listen to status changes instead of resource changes with ResourceVersionChangedPredicate
		if strings.HasSuffix(resource.Name, "status") {
			continue
		}
		gvr := gv.WithResource(resource.Name)
		informer := informerFactory.ForResource(gvr).Informer()
		dynamicInformerSet[gvr.String()] = &ComponentInformer{Informer: source.Informer{Informer: informer}, GroupVersionResource: gvr}
	}
	return dynamicInformerSet, nil
}