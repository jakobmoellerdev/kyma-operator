package controllers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kyma-project/lifecycle-manager/api/v1beta1"
	internalv1beta1 "github.com/kyma-project/lifecycle-manager/internal/manifest/v1beta1"
	declarative "github.com/kyma-project/lifecycle-manager/pkg/declarative/v2"
	"github.com/kyma-project/lifecycle-manager/pkg/labels"
	"github.com/kyma-project/lifecycle-manager/pkg/security"
	listener "github.com/kyma-project/runtime-watcher/listener/pkg/event"
	"github.com/kyma-project/runtime-watcher/listener/pkg/types"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

func SetupWithManager(
	mgr manager.Manager,
	options controller.Options,
	insecure bool,
	checkInterval time.Duration,
	settings SetupUpSetting,
) error {
	var verifyFunc listener.Verify
	if settings.EnableDomainNameVerification {
		// Verifier used to verify incoming listener requests
		verifyFunc = security.NewRequestVerifier(mgr.GetClient()).Verify
	} else {
		verifyFunc = func(r *http.Request, watcherEvtObject *types.WatchEvent) error {
			return nil
		}
	}

	runnableListener, eventChannel := listener.RegisterListenerComponent(
		settings.ListenerAddr, strings.ToLower(labels.OperatorName), verifyFunc,
	)

	// start listener as a manager runnable
	if err := mgr.Add(runnableListener); err != nil {
		return err
	}

	codec, err := v1beta1.NewCodec()
	if err != nil {
		return fmt.Errorf("unable to initialize codec: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.Manifest{}).
		Watches(&source.Kind{Type: &v1.Secret{}}, handler.Funcs{}).
		Watches(
			eventChannel, &handler.Funcs{
				GenericFunc: func(event event.GenericEvent, queue workqueue.RateLimitingInterface) {
					ctrl.Log.WithName("listener").Info(
						fmt.Sprintf(
							"event coming from SKR, adding %s to queue",
							client.ObjectKeyFromObject(event.Object).String(),
						),
					)
					queue.Add(ctrl.Request{NamespacedName: client.ObjectKeyFromObject(event.Object)})
				},
			},
		).WithOptions(options).Complete(ManifestReconciler(mgr, codec, insecure, checkInterval))
}

func ManifestReconciler(
	mgr manager.Manager, codec *v1beta1.Codec, insecure bool,
	checkInterval time.Duration,
) *declarative.Reconciler {
	return declarative.NewFromManager(
		mgr, &v1beta1.Manifest{},
		declarative.WithSpecResolver(
			internalv1beta1.NewManifestSpecResolver(codec, insecure),
		),
		declarative.WithCustomReadyCheck(internalv1beta1.NewManifestCustomResourceReadyCheck()),
		declarative.WithRemoteTargetCluster(
			(&internalv1beta1.RemoteClusterLookup{KCP: &declarative.ClusterInfo{
				Client: mgr.GetClient(),
				Config: mgr.GetConfig(),
			}}).ConfigResolver,
		),
		declarative.WithClientCacheKeyFromLabelOrResource(labels.KymaName),
		declarative.WithPostRun{internalv1beta1.PostRunCreateCR},
		declarative.WithPreDelete{internalv1beta1.PreDeleteDeleteCR},
		declarative.WithPeriodicConsistencyCheck(checkInterval),
	)
}
