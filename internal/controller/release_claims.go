/*
Copyright 2026 Kubotal

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

package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"maps"
	"reflect"
	"slices"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=claims,verbs=get;list;watch;create;patch;delete

// handleClaims creates or updates the Claim of each claim parameter set, then returns the inputs and the
// bindings of the bound ones, like a connectionRef, and why the others are not bound.
func (r *ReleaseReconciler) handleClaims(op *releaseOperation, parameters map[string]interface{},
	inputs []*kubopackage.InputRendered) ([]*kubopackage.InputRendered, []RefBinding, []string, ReconcileError) {
	aliases := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		aliases[input.Alias] = true
	}
	op.claimNames = make(map[string]struct{})
	claimInputs := make([]*kubopackage.InputRendered, 0)
	bindings := make([]RefBinding, 0)
	messages := make([]string, 0)
	for _, decl := range op.pckContainer.ParamClaimDecls {
		alias := formatAlias(decl.Path, false)
		target, added, ok := claimValueAt(parameters, decl.Path)
		if !ok {
			if decl.Required {
				return nil, nil, nil, NewReconcileError(fmt.Errorf("parameter '%s' is required: no claim target provided", alias), true, "ClaimParameters")
			}
			continue
		}
		if aliases[alias] {
			return nil, nil, nil, NewReconcileError(fmt.Errorf("the inputs stanza alias '%s' collides with a claim parameter", alias), true, "ClaimParameters")
		}
		name := strings.ToLower(op.release.Name + "-" + strings.Join(decl.Path, "-"))
		if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
			return nil, nil, nil, NewReconcileError(fmt.Errorf("parameter '%s' gives the invalid claim name '%s'", alias, name), true, "ClaimParameters")
		}
		if _, ok := op.claimNames[name]; ok {
			return nil, nil, nil, NewReconcileError(fmt.Errorf("parameter '%s' gives the claim name '%s' of another parameter", alias, name), true, "ClaimParameters")
		}
		op.claimNames[name] = struct{}{}
		requirements, err := mergeRequirements(decl.Requirements, added)
		if err != nil {
			return nil, nil, nil, NewReconcileError(fmt.Errorf("parameter '%s': %w", alias, err), true, "ClaimParameters")
		}
		claim, message, reconcileError := r.handleClaim(op, name, decl.Contract, target, requirements)
		if reconcileError != nil {
			return nil, nil, nil, reconcileError
		}
		if message != "" {
			messages = append(messages, fmt.Sprintf("claim '%s': %s", name, message))
			continue
		}
		input := &kubopackage.InputRendered{Contract: decl.Contract, Kind: kv1alpha1.KindConnection, Alias: alias}
		input.ConnectionRef.Name = claim.Status.Connection
		input.ConnectionRef.Namespace = op.release.Namespace
		claimInputs = append(claimInputs, input)
		bindings = append(bindings, RefBinding{Alias: alias, Path: decl.Path})
	}
	return claimInputs, bindings, messages, nil
}

// claimValueAt reads the target of a claim parameter in the parameters, and the requirements the Release adds.
func claimValueAt(parameters map[string]interface{}, path []string) (kv1alpha1.ClaimTarget, map[string]interface{}, bool) {
	var node interface{} = parameters
	for _, seg := range path {
		nodeMap, ok := node.(map[string]interface{})
		if !ok {
			return kv1alpha1.ClaimTarget{}, nil, false
		}
		if node, ok = nodeMap[seg]; !ok {
			return kv1alpha1.ClaimTarget{}, nil, false
		}
	}
	values, ok := node.(map[string]interface{})
	if !ok {
		return kv1alpha1.ClaimTarget{}, nil, false
	}
	provisioner, _ := values["provisioner"].(string)
	connection, _ := values["connection"].(string)
	requirements, _ := values["requirements"].(map[string]interface{})
	return kv1alpha1.ClaimTarget{Provisioner: provisioner, Connection: connection}, requirements,
		provisioner != "" || connection != ""
}

// mergeRequirements adds the requirements of the Release to those of the package, which it cannot change.
func mergeRequirements(pck, release map[string]interface{}) (map[string]interface{}, error) {
	if len(release) == 0 {
		return pck, nil
	}
	merged := make(map[string]interface{}, len(pck)+len(release))
	maps.Copy(merged, pck)
	for _, name := range slices.Sorted(maps.Keys(release)) {
		if _, set := pck[name]; set {
			return nil, fmt.Errorf("requirement '%s' is set by the package", name)
		}
		merged[name] = release[name]
	}
	return merged, nil
}

// handleClaim makes the Claim name match the declaration, and returns it, or why it is not bound.
func (r *ReleaseReconciler) handleClaim(op *releaseOperation, name, contract string, target kv1alpha1.ClaimTarget,
	requirements map[string]interface{}) (*kv1alpha1.Claim, string, ReconcileError) {
	desired := kv1alpha1.ClaimSpec{Contract: contract, Target: target}
	if len(requirements) > 0 {
		raw, err := json.Marshal(requirements)
		if err != nil {
			return nil, "", NewReconcileError(fmt.Errorf("claim '%s': unable to marshal the requirements", name), true, "ClaimParameters")
		}
		desired.Requirements = &apiextensionsv1.JSON{Raw: raw}
	}
	claim := &kv1alpha1.Claim{}
	err := r.Get(op.ctx, types.NamespacedName{Namespace: op.release.Namespace, Name: name}, claim)
	if apierrors.IsNotFound(err) {
		claim = &kv1alpha1.Claim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: op.release.Namespace}, Spec: desired}
		if err := ctrl.SetControllerReference(op.release, claim, r.Scheme()); err != nil {
			return nil, "", NewReconcileError(fmt.Errorf("unable to set the owner reference of its Claim: %w", err), false, "ClaimCreate")
		}
		if err := r.Create(op.ctx, claim); err != nil {
			return nil, "", NewReconcileError(fmt.Errorf("error while creating its Claim: %s", apiError(err)), false, "ClaimCreate")
		}
		op.logger.V(0).Info("Created claim", "name", name)
		r.Event(op.release, "Normal", "ClaimCreated", fmt.Sprintf("Created Claim %q", name))
		return claim, r.unboundClaim(claim), nil
	}
	if err != nil {
		return nil, "", NewReconcileError(fmt.Errorf("could not get its Claim: %s", apiError(err)), false, "ClaimAccess")
	}
	if !metav1.IsControlledBy(claim, op.release) {
		return nil, "", NewReconcileError(fmt.Errorf("the Claim '%s' exists and the Release does not control it", name), false, "ClaimAccess")
	}
	if !claim.DeletionTimestamp.IsZero() {
		return nil, "being removed", nil
	}
	if claim.Spec.Contract != desired.Contract || claim.Spec.Target != desired.Target {
		// Both are immutable: the claim is replaced
		if err := client.IgnoreNotFound(r.Delete(op.ctx, claim)); err != nil {
			return nil, "", NewReconcileError(fmt.Errorf("error while deleting its Claim: %s", apiError(err)), false, "ClaimDelete")
		}
		op.logger.V(0).Info("Deleted claim, to replace it", "name", name)
		return nil, "being replaced", nil
	}
	if !sameRequirements(claim.Spec.Requirements, desired.Requirements) {
		patch := client.MergeFrom(claim.DeepCopy())
		claim.Spec.Requirements = desired.Requirements
		if err := r.Patch(op.ctx, claim, patch); err != nil {
			return nil, "", NewReconcileError(fmt.Errorf("error while patching its Claim: %s", apiError(err)), false, "ClaimPatch")
		}
	}
	if claim.IsBound() {
		return claim, "", nil
	}
	return claim, r.unboundClaim(claim), nil
}

// unboundClaim gives why a claim is not bound, from its status when it is written for its current spec.
func (r *ReleaseReconciler) unboundClaim(claim *kv1alpha1.Claim) string {
	if claim.Status.Phase == kv1alpha1.ClaimPhaseUnbound && claim.Status.ObservedGeneration == claim.Generation &&
		claim.Status.Message != "" {
		return claim.Status.Message
	}
	if claim.Spec.Target.Provisioner != "" {
		return fmt.Sprintf("waiting for provisioner '%s'", claim.Spec.Target.Provisioner)
	}
	if r.ConnectionsGate {
		return "not checked yet"
	}
	return "not checked, as the connections feature gate is off"
}

func sameRequirements(a, b *apiextensionsv1.JSON) bool {
	decode := func(j *apiextensionsv1.JSON) interface{} {
		var value interface{}
		if j != nil && len(j.Raw) > 0 {
			_ = json.Unmarshal(j.Raw, &value)
		}
		if m, ok := value.(map[string]interface{}); ok && len(m) == 0 {
			return nil
		}
		return value
	}
	return reflect.DeepEqual(decode(a), decode(b))
}

// deleteOrphanClaims deletes the Claims that the Release controls and no longer declares.
func (r *ReleaseReconciler) deleteOrphanClaims(op *releaseOperation) ReconcileError {
	claims := &kv1alpha1.ClaimList{}
	if err := r.List(op.ctx, claims, client.InNamespace(op.release.Namespace)); err != nil {
		return NewReconcileError(fmt.Errorf("could not list its Claims: %s", apiError(err)), false, "ClaimAccess")
	}
	for i := range claims.Items {
		claim := &claims.Items[i]
		if _, ok := op.claimNames[claim.Name]; ok || !metav1.IsControlledBy(claim, op.release) {
			continue
		}
		op.logger.V(0).Info("Deleting orphan claim", "name", claim.Name)
		if err := client.IgnoreNotFound(r.Delete(op.ctx, claim)); err != nil {
			op.logger.Error(errors.New(apiError(err)), "unable to delete claim", "claim", claim.Name)
		}
	}
	return nil
}
