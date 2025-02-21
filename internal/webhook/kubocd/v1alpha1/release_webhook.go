/*
Copyright 2025.

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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kubocdv1alpha1 "kubocd/api/kubocd/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var releaselog = logf.Log.WithName("release-resource")

// SetupReleaseWebhookWithManager registers the webhook for Release in the manager.
func SetupReleaseWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&kubocdv1alpha1.Release{}).
		WithValidator(&ReleaseCustomValidator{}).
		WithDefaulter(&ReleaseCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-kubocd-kubotal-io-v1alpha1-release,mutating=true,failurePolicy=fail,sideEffects=None,groups=kubocd.kubotal.io,resources=releases,verbs=create;update,versions=v1alpha1,name=mrelease-v1alpha1.kb.io,admissionReviewVersions=v1

// ReleaseCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Release when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type ReleaseCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &ReleaseCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind Release.
func (d *ReleaseCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	release, ok := obj.(*kubocdv1alpha1.Release)

	if !ok {
		return fmt.Errorf("expected an Release object but got %T", obj)
	}
	releaselog.Info("Defaulting for Release", "name", release.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-kubocd-kubotal-io-v1alpha1-release,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubocd.kubotal.io,resources=releases,verbs=create;update,versions=v1alpha1,name=vrelease-v1alpha1.kb.io,admissionReviewVersions=v1

// ReleaseCustomValidator struct is responsible for validating the Release resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type ReleaseCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &ReleaseCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type Release.
func (v *ReleaseCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	release, ok := obj.(*kubocdv1alpha1.Release)
	if !ok {
		return nil, fmt.Errorf("expected a Release object but got %T", obj)
	}
	releaselog.Info("Validation for Release upon creation", "name", release.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type Release.
func (v *ReleaseCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	release, ok := newObj.(*kubocdv1alpha1.Release)
	if !ok {
		return nil, fmt.Errorf("expected a Release object for the newObj but got %T", newObj)
	}
	releaselog.Info("Validation for Release upon update", "name", release.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type Release.
func (v *ReleaseCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	release, ok := obj.(*kubocdv1alpha1.Release)
	if !ok {
		return nil, fmt.Errorf("expected a Release object but got %T", obj)
	}
	releaselog.Info("Validation for Release upon deletion", "name", release.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
