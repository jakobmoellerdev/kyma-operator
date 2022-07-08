package watch

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	operatorv1alpha1 "github.com/kyma-project/kyma-operator/operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	Status = "status"
	State  = "state"
)

var ErrStateInvalid = errors.New("state from component object could not be interpreted")

// ComponentChangeHandler is necessary because we cannot simply trust the Observation Process triggered by a Controller
// through the Controller Owner Reference. This is because there are state changes in components, which we do not want
// to observe and instead discard. Using a Controller Owner Reference this would not be possible. Instead, we use a
// custom OwnerReference in combination with this change handler, which only reacts to Module State changes on
// the defined field. This causes us to only react with Kyma Reconciliations when the components update their
// well-defined state. Every other change will be discarded.
type ComponentChangeHandler struct {
	client.Reader
	record.EventRecorder
}

func NewComponentChangeHandler(handlerClient ChangeHandlerClient) *ComponentChangeHandler {
	return &ComponentChangeHandler{Reader: handlerClient, EventRecorder: handlerClient}
}

func (h *ComponentChangeHandler) Watch(ctx context.Context) func(event.UpdateEvent, workqueue.RateLimitingInterface) {
	logger := log.FromContext(ctx).WithName("component-change-handler")

	return func(event event.UpdateEvent, queue workqueue.RateLimitingInterface) {
		objectBytesNew, err := json.Marshal(event.ObjectNew)
		if err != nil {
			logger.Error(err, "error transforming new component object")

			return
		}

		objectBytesOld, err := json.Marshal(event.ObjectOld)
		if err != nil {
			logger.Error(err, "error transforming old component object")

			return
		}

		componentNew := unstructured.Unstructured{}
		componentOld := unstructured.Unstructured{}

		if err = json.Unmarshal(objectBytesNew, &componentNew); err != nil {
			logger.Error(err, "error transforming new component object")

			return
		}

		if err = json.Unmarshal(objectBytesOld, &componentOld); err != nil {
			logger.Error(err, "error transforming old component object")

			return
		}

		if componentNew.Object[Status] == nil {
			return
		}

		componentNameLabel := componentNew.GetLabels()[operatorv1alpha1.ControllerName]
		if componentNameLabel == "" {
			return
		}

		kyma, err := h.GetKymaOwner(ctx, &componentNew)
		if err != nil {
			logger.Error(err, "error getting Kyma owner")
		}

		oldState := extractState(componentOld, logger)
		newState := extractState(componentNew, logger)

		if oldState.(string) == newState.(string) {
			return
		}

		queue.Add(reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(kyma),
		})
	}
}

func extractState(component unstructured.Unstructured, logger logr.Logger) interface{} {
	var state interface{}

	var ok bool

	if component.Object[Status] != nil {
		state, ok = component.Object[Status].(map[string]interface{})[State]
		if !ok {
			logger.Error(ErrStateInvalid, "missing state")
		}
	} else {
		state = ""
	}

	return state
}

func (h *ComponentChangeHandler) GetKymaOwner(ctx context.Context,
	component *unstructured.Unstructured,
) (*operatorv1alpha1.Kyma, error) {
	var ownerName string

	ownerRefs := component.GetOwnerReferences()
	kyma := &operatorv1alpha1.Kyma{}

	for _, ownerRef := range ownerRefs {
		if operatorv1alpha1.KymaKind == ownerRef.Kind {
			ownerName = ownerRef.Name

			break
		}
	}

	err := h.Get(ctx, client.ObjectKey{
		Name:      ownerName,
		Namespace: component.GetNamespace(),
	}, kyma)
	if err != nil {
		return nil, fmt.Errorf("error while fetching kyma owner in the component change handler: %w", err)
	}

	return kyma, nil
}
