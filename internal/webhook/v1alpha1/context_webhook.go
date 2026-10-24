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
	kubocdv1alpha1 "kubocd/api/v1alpha1"

	"github.com/go-logr/logr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupContextWebhookWithManager registers the webhook for Context in the manager.
func SetupContextWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &kubocdv1alpha1.Context{}).
		WithValidator(&ContextCustomValidator{
			logger: mgr.GetLogger().WithName("context-webhook-validator"),
		}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-kubocd-kubotal-io-v1alpha1-context,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubocd.kubotal.io,resources=contexts,verbs=delete,versions=v1alpha1,name=vcontext-v1alpha1.kb.io,admissionReviewVersions=v1

// ContextCustomValidator checks the deletion of a Context.
type ContextCustomValidator struct {
	logger logr.Logger
}

var _ admission.Validator[*kubocdv1alpha1.Context] = &ContextCustomValidator{}

// ValidateCreate admits every creation. The webhook configurations send
// deletions only.
func (v *ContextCustomValidator) ValidateCreate(ctx context.Context, kcontext *kubocdv1alpha1.Context) (admission.Warnings, error) {
	return nil, nil
}

// ValidateUpdate admits every update. The webhook configurations send
// deletions only.
func (v *ContextCustomValidator) ValidateUpdate(ctx context.Context, oldObj, kcontext *kubocdv1alpha1.Context) (admission.Warnings, error) {
	return nil, nil
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type Context.
func (v *ContextCustomValidator) ValidateDelete(ctx context.Context, kcontext *kubocdv1alpha1.Context) (admission.Warnings, error) {
	v.logger.Info("Validation for Context upon deletion", "name", kcontext.GetName())

	if kcontext.Spec.Protected {
		return nil, fmt.Errorf("context %s is protected", kcontext.GetName())
	}
	return nil, nil
}
