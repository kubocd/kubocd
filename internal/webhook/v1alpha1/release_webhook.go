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

// SetupReleaseWebhookWithManager registers the webhook for Release in the manager.
func SetupReleaseWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &kubocdv1alpha1.Release{}).
		WithValidator(&ReleaseCustomValidator{
			logger: mgr.GetLogger().WithName("release-webhook-validator"),
		}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-kubocd-kubotal-io-v1alpha1-release,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubocd.kubotal.io,resources=releases,verbs=delete,versions=v1alpha1,name=vrelease-v1alpha1.kb.io,admissionReviewVersions=v1

// ReleaseCustomValidator checks the deletion of a Release.
type ReleaseCustomValidator struct {
	logger logr.Logger
}

var _ admission.Validator[*kubocdv1alpha1.Release] = &ReleaseCustomValidator{}

// ValidateCreate admits every creation. The webhook configurations send
// deletions only.
func (v *ReleaseCustomValidator) ValidateCreate(ctx context.Context, release *kubocdv1alpha1.Release) (admission.Warnings, error) {
	return nil, nil
}

// ValidateUpdate admits every update. The webhook configurations send
// deletions only.
func (v *ReleaseCustomValidator) ValidateUpdate(ctx context.Context, oldObj, release *kubocdv1alpha1.Release) (admission.Warnings, error) {
	return nil, nil
}

// ValidateDelete refuses the deletion of a protected Release: by
// spec.protected when set, by the protection in its status otherwise. The
// controller writes that status once it has loaded the package. While no
// status written since holds it, status.printProtected is empty: the
// protection is not known, so the deletion is refused unless spec.protected
// is set.
func (v *ReleaseCustomValidator) ValidateDelete(ctx context.Context, release *kubocdv1alpha1.Release) (admission.Warnings, error) {
	v.logger.Info("Validation for Release upon deletion", "name", release.GetName())

	if release.Spec.Protected != nil {
		if *release.Spec.Protected {
			return nil, fmt.Errorf("release %s is protected", release.GetName())
		}
		return nil, nil
	}
	if release.Status.PrintProtected == "" {
		return nil, fmt.Errorf("release %s may be protected by its package, and the controller has not computed its "+
			"protection yet: set spec.protected to false to delete it anyway", release.GetName())
	}
	if release.Status.Protected {
		return nil, fmt.Errorf("release %s is protected", release.GetName())
	}
	return nil, nil
}
