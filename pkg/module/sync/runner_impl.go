package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/kyma-project/module-manager/pkg/types"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	manifestV1alpha1 "github.com/kyma-project/module-manager/api/v1alpha1"

	"github.com/kyma-project/lifecycle-manager/api/v1alpha1"
	"github.com/kyma-project/lifecycle-manager/pkg/module/common"
)

func New(clnt client.Client) *RunnerImpl {
	return &RunnerImpl{
		Client:    clnt,
		versioner: schema.GroupVersions(clnt.Scheme().PreferredVersionAllGroups()),
		converter: clnt.Scheme(),
	}
}

type RunnerImpl struct {
	client.Client
	versioner runtime.GroupVersioner
	converter runtime.ObjectConvertor
}

// Sync implements Runner.Sync.
func (r *RunnerImpl) Sync(ctx context.Context, kyma *v1alpha1.Kyma,
	modules common.Modules,
) error {
	ssaStart := time.Now()
	baseLogger := log.FromContext(ctx)

	results := make(chan error, len(modules))
	for _, module := range modules {
		go func(module *common.Module) {
			if err := r.updateModule(ctx, kyma, module); err != nil {
				results <- fmt.Errorf("could not update module %s: %w", module.Name, err)
				return
			}
			module.Logger(baseLogger).V(int(zap.DebugLevel)).Info("successfully patched module")
			results <- nil
		}(module)
	}
	var errs []error
	for i := 0; i < len(modules); i++ {
		if err := <-results; err != nil {
			errs = append(errs, err)
		}
	}
	ssaFinish := time.Since(ssaStart)
	if errs != nil {
		return fmt.Errorf("ServerSideApply failed (after %s): %w", ssaFinish, types.NewMultiError(errs))
	}
	baseLogger.V(int(zap.DebugLevel)).Info("ServerSideApply finished", "time", ssaFinish)
	return nil
}

func (r *RunnerImpl) getModule(ctx context.Context, module *manifestV1alpha1.Manifest) error {
	return r.Get(ctx, client.ObjectKey{Namespace: module.GetNamespace(), Name: module.GetName()}, module)
}

func (r *RunnerImpl) updateModule(ctx context.Context, kyma *v1alpha1.Kyma,
	module *common.Module,
) error {
	if err := r.setupModule(module, kyma); err != nil {
		return err
	}
	obj, err := r.converter.ConvertToVersion(module.Manifest, r.versioner)
	if err != nil {
		return err
	}
	clObj := obj.(client.Object)
	if err := r.Patch(ctx, obj.(client.Object),
		client.Apply,
		client.FieldOwner(kyma.Labels[v1alpha1.ManagedBy]),
		client.ForceOwnership,
	); err != nil {
		return fmt.Errorf("error applying manifest %s: %w", client.ObjectKeyFromObject(module), err)
	}
	// TODO replace with unstructured lookup
	module.UpdateStatusAndReferencesFromUnstructured(clObj.(*manifestV1alpha1.Manifest))

	return nil
}

func (r *RunnerImpl) setupModule(module *common.Module, kyma *v1alpha1.Kyma) error {
	// set labels
	module.ApplyLabelsAndAnnotations(kyma)

	if module.GetOwnerReferences() == nil {
		// set owner reference
		if err := controllerutil.SetControllerReference(kyma, module.Manifest, r.Scheme()); err != nil {
			return fmt.Errorf("error setting owner reference on component CR of type: %s for resource %s %w",
				module.Name, kyma.Name, err)
		}
	}

	return nil
}

func (r *RunnerImpl) SyncModuleStatus(ctx context.Context, kyma *v1alpha1.Kyma, modules common.Modules) bool {
	statusMap := kyma.GetModuleStatusMap()
	statusUpdateRequiredFromUpdate := r.updateModuleStatusFromExistingModules(modules, statusMap, kyma)
	statusUpdateRequiredFromDelete := r.deleteNoLongerExistingModuleStatus(ctx, statusMap, kyma)
	return statusUpdateRequiredFromUpdate || statusUpdateRequiredFromDelete
}

func (r *RunnerImpl) updateModuleStatusFromExistingModules(modules common.Modules,
	moduleStatusMap map[string]*v1alpha1.ModuleStatus, kyma *v1alpha1.Kyma,
) bool {
	updateRequired := false
	for idx := range modules {
		module := modules[idx]
		latestModuleStatus := v1alpha1.ModuleStatus{
			For:        module.For,
			FQDN:       module.FQDN,
			Name:       module.Manifest.GetName(),
			Namespace:  module.Manifest.GetNamespace(),
			Generation: module.Manifest.GetGeneration(),
			TemplateInfo: v1alpha1.TemplateInfo{
				Name:       module.Template.Name,
				Namespace:  module.Template.Namespace,
				Channel:    module.Template.Spec.Channel,
				Generation: module.Template.Generation,
				Version:    module.Version,
			},
			State: stateFromManifest(module.Manifest),
		}
		moduleStatus, exists := moduleStatusMap[module.For]
		if exists {
			if moduleStatus.State != latestModuleStatus.State {
				updateRequired = true
			}
			*moduleStatus = latestModuleStatus
		} else {
			updateRequired = true
			kyma.Status.ModuleStatus = append(kyma.Status.ModuleStatus, latestModuleStatus)
		}
	}
	return updateRequired
}

func stateFromManifest(obj *manifestV1alpha1.Manifest) v1alpha1.State {
	state := v1alpha1.State(obj.Status.State)
	if state == "" {
		return v1alpha1.StateProcessing
	}
	return state
}

func (r *RunnerImpl) deleteNoLongerExistingModuleStatus(ctx context.Context,
	moduleStatusMap map[string]*v1alpha1.ModuleStatus, kyma *v1alpha1.Kyma,
) bool {
	updateRequired := false
	moduleStatusArr := kyma.GetNoLongerExistingModuleStatus()
	if len(moduleStatusArr) == 0 {
		return false
	}
	for i := range moduleStatusArr {
		moduleStatus := moduleStatusArr[i]
		module := manifestV1alpha1.Manifest{}
		module.SetName(moduleStatus.Name)
		module.SetNamespace(moduleStatus.Namespace)
		err := r.getModule(ctx, &module)
		if errors.IsNotFound(err) {
			updateRequired = true
			delete(moduleStatusMap, moduleStatus.For)
		}
	}
	kyma.Status.ModuleStatus = convertToNewmoduleStatus(moduleStatusMap)
	return updateRequired
}

func convertToNewmoduleStatus(moduleStatusMap map[string]*v1alpha1.ModuleStatus) []v1alpha1.ModuleStatus {
	newModuleStatus := make([]v1alpha1.ModuleStatus, 0)
	for _, moduleStatus := range moduleStatusMap {
		newModuleStatus = append(newModuleStatus, *moduleStatus)
	}
	return newModuleStatus
}
