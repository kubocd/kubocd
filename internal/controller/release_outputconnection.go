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
	"encoding/json"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"

	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sort"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ownedByRelease tells if a Connection is controller-owned by the given
// release. The no-adoption rule: a pre-existing connection not owned by the
// reconciling release is never patched nor deleted.
func ownedByRelease(connection *kv1alpha1.Connection, releaseName string) bool {
	owner := metav1.GetControllerOf(connection)
	return owner != nil && owner.Kind == "Release" &&
		strings.HasPrefix(owner.APIVersion, kv1alpha1.GroupVersion.Group) &&
		owner.Name == releaseName
}

func (r *ReleaseReconciler) handleOutputConnection(op *releaseOperation, connectionName string, outputRendered *kubopackage.OutputRendered) (*kv1alpha1.Connection, ReconcileError) {
	connection := &kv1alpha1.Connection{}
	err := r.Get(op.ctx, types.NamespacedName{Name: connectionName, Namespace: op.release.Namespace}, connection)
	//fmt.Printf("handleOutputConnection(outputRendered:%s\n)\n", misc.Any2Yaml(outputRendered))
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("on Connection '%s': %w", connectionName, err), false, "ConnectionAccess")
		}
		if !outputRendered.Disabled {
			// Must create it
			op.logger.V(0).Info("Will create connection", "name", connectionName, "namespace", op.release.Namespace, "output", outputRendered.Name)
			op.outputConnectionK8sName[connectionName] = struct{}{} // Mark as non-orphan
			err := r.createConnection(op, outputRendered, connectionName)
			if err != nil {
				return nil, NewReconcileError(err, false, "ConnectionCreate")
			}
			r.Event(op.release, "Normal", "ConnectionCreated", fmt.Sprintf("Created Connection %q", connectionName))
			op.logger.V(1).Info("Launched connection", "connectionName", connectionName)
			op.outputConnectionByName[outputRendered.Name] = kv1alpha1.ReleaseOutputConnection{
				Kind:      kv1alpha1.Kind(connection.Kind),
				Name:      connection.Name,
				Namespace: connection.Namespace,
				Phase:     "",
				Message:   "",
			}
			return connection, nil
		}
		op.logger.V(1).Info("Disabled connection", "connection", connectionName)
		delete(op.outputConnectionByName, outputRendered.Name)
		// Nothing to do.
		return nil, nil
	}
	// Connection exists: no adoption, whoever owns it keeps it
	owned := ownedByRelease(connection, op.release.Name)
	// Update if needed
	if !outputRendered.Disabled {
		if !owned {
			return nil, NewReconcileError(fmt.Errorf("connection '%s' already exists and is not owned by this release", connectionName), false, "ConnectionOwnership")
		}
		op.outputConnectionK8sName[connectionName] = struct{}{} // Mark as non-orphan
		changed, err := patchConnection(r, op, connection, outputRendered)
		if err != nil {
			return nil, NewReconcileError(err, false, "ConnectionPatch")
		}
		if changed {
			op.logger.V(0).Info("Connection updated", "name", connectionName, "namespace", op.release.Namespace, "output", outputRendered.Name)
		} else {
			op.logger.V(1).Info("Connection unchanged", "name", connectionName, "namespace", op.release.Namespace, "output", outputRendered.Name)
		}
		op.outputConnectionByName[outputRendered.Name] = kv1alpha1.ReleaseOutputConnection{
			Kind:      kv1alpha1.Kind(connection.Kind),
			Name:      connection.Name,
			Namespace: connection.Namespace,
			Phase:     connection.Status.Phase,
			Message:   connection.Status.Message,
		}
		return connection, nil
	}
	if !owned {
		// Disabled output whose name collides with a foreign connection:
		// nothing would be touched anyway, do not delete, do not error
		delete(op.outputConnectionByName, outputRendered.Name)
		return nil, nil
	}
	op.logger.V(0).Info("Delete connection as disabled", "name", connectionName)
	err = r.Delete(op.ctx, connection)
	if err != nil {
		return nil, NewReconcileError(err, false, "ConnectionDelete")
	}
	delete(op.outputConnectionByName, outputRendered.Name)
	return nil, nil
}

func patchConnection(r *ReleaseReconciler, op *releaseOperation, connection *kv1alpha1.Connection, outputRendered *kubopackage.OutputRendered) (bool, error) {
	// Store original generation to detect changes
	originalGeneration := connection.Generation

	// Create a deep copy for the patch operation
	patch := client.MergeFrom(connection.DeepCopy())

	// Populate the HelmRelease with updated configuration
	err := PopulateConnection(connection, outputRendered)
	if err != nil {
		return false, fmt.Errorf("failed to populate connection '%s': %w", connection.Name, err)
	}

	// Apply the patch
	err = r.Patch(op.ctx, connection, patch)
	if err != nil {
		return false, fmt.Errorf("error while patching connection '%s': %w", connection.Name, err)
	}
	// Check if the generation changed to determine if an update occurred
	return originalGeneration != connection.Generation, nil
}

func (r *ReleaseReconciler) createConnection(op *releaseOperation, outputRendered *kubopackage.OutputRendered, connectionName string) error {
	connection := &kv1alpha1.Connection{}
	connection.SetName(connectionName)
	connection.SetNamespace(op.release.Namespace)
	err := PopulateConnection(connection, outputRendered)
	if err != nil {
		return fmt.Errorf("failed to populate connection '%s': %w", connectionName, err)
	}
	err = ctrl.SetControllerReference(op.release, connection, r.Scheme())
	if err != nil {
		return fmt.Errorf("unable to set connection '%s' owner reference: %w", connectionName, err)
	}
	if err = r.Create(op.ctx, connection); err != nil {
		return fmt.Errorf("error while creating connection '%s': %w", connectionName, err)
	}
	// This will be set by the connection controller
	//connection.Status.Parent = op.release.Name
	//err = r.Status().Update(op.ctx, connection)
	//if err != nil {
	//	return fmt.Errorf("error while setting status on connection '%s': %w", connectionName, err)
	//}
	return nil
}

// AppliedLabelsAnnotation records the label keys applied by the output, so a
// label removed from the package converges (gets deleted) while keys set by
// other actors are preserved.
const AppliedLabelsAnnotation = "kubocd.kubotal.io/applied-labels"

// ApplyManagedLabels applies the labels of an output on the object metadata:
// stale keys (applied previously, absent from the new set) are removed, keys
// set by other actors are left alone. The applied set is tracked in an
// annotation.
func ApplyManagedLabels(meta *metav1.ObjectMeta, rendered map[string]string) {
	previous := ""
	if meta.Annotations != nil {
		previous = meta.Annotations[AppliedLabelsAnnotation]
	}
	for _, k := range strings.Split(previous, ",") {
		if k == "" {
			continue
		}
		if _, still := rendered[k]; !still {
			delete(meta.Labels, k)
		}
	}
	if len(rendered) > 0 {
		if meta.Labels == nil {
			meta.Labels = make(map[string]string, len(rendered))
		}
		keys := make([]string, 0, len(rendered))
		for k, v := range rendered {
			meta.Labels[k] = v
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if meta.Annotations == nil {
			meta.Annotations = make(map[string]string, 1)
		}
		meta.Annotations[AppliedLabelsAnnotation] = strings.Join(keys, ",")
	} else if previous != "" {
		delete(meta.Annotations, AppliedLabelsAnnotation)
	}
}

func PopulateConnection(connection *kv1alpha1.Connection, outputRendered *kubopackage.OutputRendered) error {
	ApplyManagedLabels(&connection.ObjectMeta, outputRendered.Labels)
	connection.Spec.Disabled = false // Always false for managed connections
	valuesTxt, err := json.Marshal(outputRendered.Values)
	if err != nil {
		return fmt.Errorf("output '%s': could not encode values: %w", outputRendered.Name, err)
	}
	connection.Spec.Values = &v1.JSON{Raw: valuesTxt}
	connection.Spec.Interface = outputRendered.Interface
	connection.Spec.Description = outputRendered.Description
	connection.Spec.Priority = outputRendered.Priority
	connection.Spec.OutputName = outputRendered.Name
	return nil
}

func BuildConnectionName(releaseName, outputName string) string {
	return fmt.Sprintf("kcd-%s-%s", releaseName, outputName)
}
