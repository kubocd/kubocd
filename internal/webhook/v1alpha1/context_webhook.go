/*
Copyright 2025 Kubotal

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	kubocdv1alpha1 "kubocd/api/v1alpha1"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupContextWebhookWithManager registers the webhook for Context in the manager.
func SetupContextWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&kubocdv1alpha1.Context{}).
		WithValidator(&ContextCustomValidator{
			logger: mgr.GetLogger().WithName("context-webhook-validator"),
		}).
		WithDefaulter(&ContextCustomDefaulter{
			logger: mgr.GetLogger().WithName("context-webhook-defaulter"),
		}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-kubocd-kubotal-io-v1alpha1-context,mutating=true,failurePolicy=fail,sideEffects=None,groups=kubocd.kubotal.io,resources=contexts,verbs=create;update,versions=v1alpha1,name=mcontext-v1alpha1.kb.io,admissionReviewVersions=v1

// ContextCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Context when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type ContextCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
	logger logr.Logger
}

var _ webhook.CustomDefaulter = &ContextCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind Context.
func (d *ContextCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	context, ok := obj.(*kubocdv1alpha1.Context)

	if !ok {
		return fmt.Errorf("expected an Context object but got %T", obj)
	}
	d.logger.Info("Defaulting for Context", "name", context.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-kubocd-kubotal-io-v1alpha1-context,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubocd.kubotal.io,resources=contexts,verbs=create;update;delete,versions=v1alpha1,name=vcontext-v1alpha1.kb.io,admissionReviewVersions=v1

// ContextCustomValidator struct is responsible for validating the Context resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type ContextCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
	logger logr.Logger
}

var _ webhook.CustomValidator = &ContextCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type Context.
func (v *ContextCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	kcontext, ok := obj.(*kubocdv1alpha1.Context)
	if !ok {
		return nil, fmt.Errorf("expected a Context object but got %T", obj)
	}
	v.logger.Info("Validation for Context upon creation", "name", kcontext.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type Context.
func (v *ContextCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	kcontext, ok := newObj.(*kubocdv1alpha1.Context)
	if !ok {
		return nil, fmt.Errorf("expected a Context object for the newObj but got %T", newObj)
	}
	v.logger.Info("Validation for Context upon update", "name", kcontext.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type Context.
func (v *ContextCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	kcontext, ok := obj.(*kubocdv1alpha1.Context)
	if !ok {
		return nil, fmt.Errorf("expected a Context object but got %T", obj)
	}
	v.logger.Info("Validation for Context upon deletion", "name", kcontext.GetName())

	if kcontext.Spec.Protected {
		return nil, fmt.Errorf("context %s is protected", kcontext.GetName())
	}
	return nil, nil
}
