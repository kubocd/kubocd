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
	"context"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/misc"
	"reflect"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// The indexes of the claims that name a connection, by contract and by connection
const ClaimIndexOnContract = "claimIndexOnContract"
const ClaimIndexOnTargetConnection = "claimIndexOnTargetConnection"

// ClaimReconciler binds the claims that name an existing connection. It never writes the status of a claim
// that names a provisioner, as the provisioner does.
type ClaimReconciler struct {
	client.Client
	record.EventRecorder
	Logger          logr.Logger
	ConditionLimits *ConditionLimits
}

// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=claims,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=claims/status,verbs=patch

func (r *ClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)
	claim := &kv1alpha1.Claim{}
	if err := r.Get(ctx, req.NamespacedName, claim); err != nil {
		logger.V(1).Info("Unable to fetch resource. Seems deleted")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if claim.Spec.Target.Connection == "" {
		return ctrl.Result{}, nil
	}
	reason, err := r.bindingError(ctx, claim)
	if err != nil {
		return ctrl.Result{}, err
	}
	previous := claim.DeepCopy()
	claim.Status.ObservedGeneration = claim.Generation
	if reason == "" {
		claim.Status.Phase = kv1alpha1.ClaimPhaseBound
		claim.Status.Connection = claim.Spec.Target.Connection
		claim.Status.Message = ""
	} else {
		claim.Status.Phase = kv1alpha1.ClaimPhaseUnbound
		claim.Status.Connection = ""
		claim.Status.Message = reason
	}
	if reflect.DeepEqual(previous.Status, claim.Status) {
		return ctrl.Result{}, nil
	}
	if claim.Status.Phase != previous.Status.Phase || claim.Status.Message != previous.Status.Message {
		if reason == "" {
			r.Event(claim, "Normal", "Bound", fmt.Sprintf("Bound to connection '%s'", claim.Status.Connection))
		} else {
			r.Event(claim, "Warning", "Unbound", reason)
		}
	}
	if err := r.Status().Patch(ctx, claim, client.MergeFrom(previous)); err != nil {
		return ctrl.Result{}, fmt.Errorf("could not update the Claim status: %s", apiError(err))
	}
	return ctrl.Result{}, nil
}

// bindingError returns why the claim cannot bind the connection it names, or "" when it can. Its messages
// name the connection and the requirement, never a value of the connection.
func (r *ClaimReconciler) bindingError(ctx context.Context, claim *kv1alpha1.Claim) (string, error) {
	name := claim.Spec.Target.Connection
	connection := &kv1alpha1.Connection{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: claim.Namespace, Name: name}, connection); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Sprintf("connection '%s' not found", name), nil
		}
		return "", fmt.Errorf("could not get the Connection: %s", apiError(err))
	}
	if connection.Spec.Contract != claim.Spec.Contract {
		return fmt.Sprintf("connection '%s' follows the contract '%s', not '%s'", name, connection.Spec.Contract, claim.Spec.Contract), nil
	}
	if connection.Status.Phase != kv1alpha1.ConnectionPhaseReady {
		return fmt.Sprintf("connection '%s' is not ready", name), nil
	}
	contract, err := findContract(ctx, r, connection)
	if err != nil {
		return "", err
	}
	if contract == nil {
		// Without a contract, no requirement can be checked
		contract = &kv1alpha1.ClusterContract{}
	} else if contract.GetStatusPhase() != kv1alpha1.ContractPhaseReady {
		return fmt.Sprintf("contract '%s' is not ready", claim.Spec.Contract), nil
	}
	defaults, _, _, err := resolveContract(contract)
	if err != nil {
		return fmt.Sprintf("contract '%s' is in error", claim.Spec.Contract), nil
	}
	compiled, err := resolveContractClaim(contract)
	if err != nil {
		return fmt.Sprintf("contract '%s' is in error", claim.Spec.Contract), nil
	}
	values := make(map[string]interface{})
	if raw := connection.GetValuesRaw(); raw != nil {
		if yaml.UnmarshalStrict(raw, &values) != nil {
			return fmt.Sprintf("the values of connection '%s' are not a map", name), nil
		}
	}
	requirements := make(map[string]interface{})
	if claim.Spec.Requirements != nil {
		if yaml.UnmarshalStrict(claim.Spec.Requirements.Raw, &requirements) != nil {
			return "the requirements are not a map", nil
		}
	}
	unmet, err := compiled.unmetRequirement(ctx, requirements, misc.MergeMaps(defaults, values), r.limits())
	if err != nil {
		if ctx.Err() != nil {
			return "", err
		}
		return err.Error(), nil
	}
	if unmet != "" {
		return fmt.Sprintf("requirement '%s' is not met by connection '%s'", unmet, name), nil
	}
	return "", nil
}

func (r *ClaimReconciler) limits() ConditionLimits {
	if r.ConditionLimits != nil {
		return *r.ConditionLimits
	}
	return DefaultConditionLimits
}
