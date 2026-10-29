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

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"math/big"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// ContextReconciler reconciles a Context object
type ContextReconciler struct {
	client.Client
	//Scheme *runtime.Scheme
	record.EventRecorder
	Logger logr.Logger
}

// Just a container to avoid messy parameters passing
type contextOperation struct {
	ctx      context.Context
	logger   logr.Logger
	kcontext *kv1alpha1.Context
}

// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=contexts,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=contexts/status,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *ContextReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.V(1).Info(fmt.Sprintf("vv..............vv  %s:%s", req.NamespacedName.Namespace, req.NamespacedName.Name))
	result, err := r.reconcile2(ctx, req, logger)
	// result := ctrl.Result{}
	// var err error = nil
	logger.V(1).Info("^^..............^^", "result", result)
	return result, err
}

func (r *ContextReconciler) reconcile2(ctx context.Context, req ctrl.Request, logger logr.Logger) (ctrl.Result, error) {
	// We don't use logger provided by the manager, as it is quite verbose
	//logger := log.FromContext(ctx)
	kcontext := &kv1alpha1.Context{}
	err := r.Get(ctx, req.NamespacedName, kcontext)
	if err != nil {
		logger.V(1).Info("Unable to fetch resource. Seems deleted")
		// we'll ignore not-found errors, since they can't be fixed by an immediate requeue
		// (we'll need to wait for a new notification), and we can get them on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	read := kcontext.Status.DeepCopy()

	op := &contextOperation{
		ctx:      ctx,
		logger:   logger,
		kcontext: kcontext,
	}

	// We have nothing to cleanup with this kind. So no need to setup a finalizer
	rErr := groomContext(kcontext, logger)
	if rErr != nil {
		return r.reportError(op, rErr)
	}
	if len(kcontext.Spec.Parents) == 0 {
		// Must ensure status is empty
		if kcontext.Status.Context != nil || kcontext.Status.Parents != "" {
			kcontext.Status.Context = nil
			kcontext.Status.Parents = ""
			return ctrl.Result{}, r.updatePhase(op, kv1alpha1.ContextPhaseReady, true)
		}
		return ctrl.Result{}, r.updatePhase(op, kv1alpha1.ContextPhaseReady, false)
	} else {
		// -------Build a displayable Parents list
		parents := make([]string, len(kcontext.Spec.Parents))
		for i := range kcontext.Spec.Parents {
			parents[i] = kcontext.Spec.Parents[i].String()
		}
		kcontext.Status.Parents = strings.Join(parents, ",")

		if cycle := parentCycle(ctx, r, kcontext); cycle != "" {
			return r.reportError(op, NewReconcileError(fmt.Errorf("parents form a cycle: %s", cycle), true, "ParentCycle"))
		}

		// ------ And now, build the current context
		base := make(map[string]interface{})
		for _, parent := range kcontext.Spec.Parents {
			parentContext := &kv1alpha1.Context{}
			err = r.Get(ctx, parent.ToObjectKey(), parentContext)
			if err != nil {
				if errors.IsNotFound(err) {
					// Not retried: the watch of the parents reconciles the child when the parent comes
					return r.reportError(op, NewReconcileError(fmt.Errorf("parent '%s' not found", parent.String()), true, "MissingParent"))
				}
				return r.reportError(op, NewReconcileError(fmt.Errorf("could not get parent '%s': %w", parent.String(), err), false, "GetParent"))
			}
			if parentContext.Status.Phase != kv1alpha1.ContextPhaseReady {
				return r.reportError(op, NewReconcileError(fmt.Errorf("parent '%s' is in error", parent.String()), false, "ParentError"))
			}
			// OK. Merge our info on top of our parent
			ctx := parentContext.Status.Context
			if ctx == nil {
				ctx = parentContext.Spec.Context
			}
			base, err = Merge(base, ctx)
			if err != nil {
				return r.reportError(op, NewReconcileError(fmt.Errorf("unable to merge context from '%s': %w", parent.String(), err), true, "ContextError")) // Should not occurs
			}
		}
		// And merge out own content
		base, err = Merge(base, kcontext.Spec.Context)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("unable to merge our own context values: %w", err), true, "ContextError")) // Should not occurs
		}
		// And store in status
		ba, err := json.Marshal(&base)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("unable to marshal result"), false, "ContextError")) // Should not occurs
		}
		kcontext.Status.Context = &apiextensionsv1.JSON{
			Raw: ba,
		}
		changed := kcontext.Status.Parents != read.Parents || !sameJSON(kcontext.Status.Context, read.Context)
		return ctrl.Result{}, r.updatePhase(op, kv1alpha1.ContextPhaseReady, changed)
	}
}

// parentCycle follows the parents of the Context, then theirs, and returns the
// path that leads back to the Context, such as apps:a -> apps:b -> apps:a, or
// "" when none does. A parent that cannot be read ends its branch: the loop on
// the parents of the Context, or the Context whose parent it is, reports it.
func parentCycle(ctx context.Context, reader client.Reader, kcontext *kv1alpha1.Context) string {
	self := kv1alpha1.NamespacedName{Namespace: kcontext.Namespace, Name: kcontext.Name}
	explored := make(map[kv1alpha1.NamespacedName]bool)
	var walk func(path []kv1alpha1.NamespacedName, parents []kv1alpha1.NamespacedName) []kv1alpha1.NamespacedName
	walk = func(path []kv1alpha1.NamespacedName, parents []kv1alpha1.NamespacedName) []kv1alpha1.NamespacedName {
		for _, parent := range parents {
			if parent == self {
				return append(slices.Clip(path), parent)
			}
			if explored[parent] {
				continue
			}
			explored[parent] = true
			parentContext := &kv1alpha1.Context{}
			if err := reader.Get(ctx, parent.ToObjectKey(), parentContext); err != nil {
				continue
			}
			grandParents := slices.Clone(parentContext.Spec.Parents)
			for i := range grandParents {
				if grandParents[i].Namespace == "" {
					grandParents[i].Namespace = parentContext.Namespace
				}
			}
			if cycle := walk(append(slices.Clip(path), parent), grandParents); cycle != nil {
				return cycle
			}
		}
		return nil
	}
	cycle := walk([]kv1alpha1.NamespacedName{self}, kcontext.Spec.Parents)
	names := make([]string, len(cycle))
	for i := range cycle {
		names[i] = cycle[i].String()
	}
	return strings.Join(names, " -> ")
}

func groomContext(kcontext *kv1alpha1.Context, logger logr.Logger) ReconcileError {
	for i := range kcontext.Spec.Parents {
		child := &kcontext.Spec.Parents[i]
		if child.Namespace == "" {
			logger.V(1).Info("Set namespace for child", "childName", child.Name, "childNamespace", kcontext.ObjectMeta.Namespace)
			child.Namespace = kcontext.ObjectMeta.Namespace
		}
	}
	// Check context is a valid map. A Context without one merges nothing.
	if kcontext.Spec.Context == nil {
		return nil
	}
	kuboContext := make(map[string]interface{})
	err := yaml.UnmarshalStrict(kcontext.Spec.Context.Raw, &kuboContext)
	if err != nil {
		return NewReconcileError(fmt.Errorf("unable to unmarshal kubo context: %w", err), false, "InvalidContext")
	}
	return nil
}

// If error is 'fatal', this means it is due to something which can't be fixed with retry (i.e: invalid image).
// In such case, set status.phase = ERROR, log and don't retry
func (r *ContextReconciler) reportError(op *contextOperation, rErr ReconcileError) (ctrl.Result, error) {
	err2 := r.updatePhase(op, kv1alpha1.ContextPhaseError, false)
	if err2 != nil {
		return ctrl.Result{}, rErr // Will retry
	}
	if rErr.GetEventReason() != "" && rErr.GetBaseError() != nil {
		r.Event(op.kcontext, "Warning", rErr.GetEventReason(), rErr.Error())
	}
	if rErr.IsFatal() {
		op.logger.Error(rErr, "Wait for this to be fixed")
		return ctrl.Result{}, nil
	} else {
		return ctrl.Result{}, rErr
	}
}

// sameJSON tells whether a and b hold the same JSON value, numbers compared
// by their value. The API server can encode a value it stores differently
// from the controller.
func sameJSON(a, b *apiextensionsv1.JSON) bool {
	if a == nil || b == nil {
		return a == b
	}
	aValue, aErr := decodeJSON(a.Raw)
	bValue, bErr := decodeJSON(b.Raw)
	return aErr == nil && bErr == nil && sameValue(aValue, bValue)
}

// decodeJSON decodes raw, its numbers kept as written.
func decodeJSON(raw []byte) (interface{}, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value interface{}
	err := decoder.Decode(&value)
	return value, err
}

// sameValue tells whether two decoded JSON values are the same, numbers
// compared by their exact value, so that 1, 1.0 and 1e0 are one.
func sameValue(a, b interface{}) bool {
	switch a := a.(type) {
	case json.Number:
		b, ok := b.(json.Number)
		if !ok {
			return false
		}
		x, xOk := new(big.Rat).SetString(a.String())
		y, yOk := new(big.Rat).SetString(b.String())
		return xOk && yOk && x.Cmp(y) == 0
	case map[string]interface{}:
		b, ok := b.(map[string]interface{})
		if !ok || len(a) != len(b) {
			return false
		}
		for key, value := range a {
			if other, ok := b[key]; !ok || !sameValue(value, other) {
				return false
			}
		}
		return true
	case []interface{}:
		b, ok := b.([]interface{})
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !sameValue(a[i], b[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}

func (r *ContextReconciler) updatePhase(op *contextOperation, phase kv1alpha1.ContextPhase, force bool) error {
	if op.kcontext.Status.Phase == phase && !force {
		op.logger.V(1).Info("Context phase is already up-to-date", "phase", phase)
		return nil
	}
	op.logger.V(1).Info("Updating phase", "newPhase", phase, "oldPhase", op.kcontext.Status.Phase, "force", force)
	op.kcontext.Status.Phase = phase
	return r.Status().Update(op.ctx, op.kcontext)
}
