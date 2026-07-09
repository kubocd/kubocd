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
	"context"
	"encoding/json"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"

	"github.com/go-logr/logr"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ReleaseReconciler) handleOutputConnection(op *releaseOperation, connectionName string, outputRendered *kubopackage.OutputRendered) (*kv1alpha1.Connection, ReconcileError) {
	connection := &kv1alpha1.Connection{}
	err := r.Get(op.ctx, types.NamespacedName{Name: connectionName, Namespace: op.release.Namespace}, connection)
	//fmt.Printf("handleOutputConnection(outputRendered:%s\n)\n", misc.Any2Yaml(outputRendered))
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("on Connection '%s': %w", connectionName, err), false, "ConnectionAccess")
		}
		if outputRendered.Enabled {
			// Must create it
			op.logger.V(0).Info("Will create connection", "name", connectionName, "namespace", op.release.Namespace, "output", outputRendered.Name)
			op.outputConnectionK8sName[connectionName] = struct{}{} // Mark as non-orphan
			err := r.createConnection(op, outputRendered, connectionName)
			if err != nil {
				return nil, NewReconcileError(err, false, "ConnectionCreate")
			}
			r.Event(op.release, "Normal", "ConnectionCreated", fmt.Sprintf("Created Connection %q", connectionName))
			op.logger.V(1).Info("Launched connection", "connectionName", connectionName)
			op.outputConnectionStates[outputRendered.Name] = kv1alpha1.ConnectionState{
				Phase:   "",
				Message: "",
			}
			return connection, nil
		}
		op.logger.V(1).Info("Disabled connection", "connection", connectionName)
		delete(op.outputConnectionStates, outputRendered.Name)
		// Nothing to do.
		return nil, nil
	}
	// Connection exist. Update if needed
	if outputRendered.Enabled {
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
		op.outputConnectionStates[outputRendered.Name] = computeConnectionState(connection)
		return connection, nil
	}
	op.logger.V(0).Info("Delete connection as disabled", "name", connectionName)
	err = r.Delete(op.ctx, connection)
	if err != nil {
		return nil, NewReconcileError(err, false, "ConnectionDelete")
	}
	delete(op.outputConnectionStates, outputRendered.Name)
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

func computeConnectionState(connection *kv1alpha1.Connection) kv1alpha1.ConnectionState {
	return kv1alpha1.ConnectionState{
		Phase:   connection.Status.Phase,
		Message: connection.Status.Message,
	}
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
	return nil
}

func PopulateConnection(connection *kv1alpha1.Connection, outputRendered *kubopackage.OutputRendered) error {
	connection.Spec.Disabled = false // Always false for managed connections
	valuesTxt, err := json.Marshal(outputRendered.Values)
	if err != nil {
		return fmt.Errorf("output '%s': could not encode values: %w", outputRendered.Name, err)
	}
	connection.Spec.Values = &v1.JSON{Raw: valuesTxt}
	connection.Spec.Interface = outputRendered.Interface
	connection.Spec.Description = outputRendered.Description
	connection.Spec.Priority = outputRendered.Priority
	return nil
}

func BuildConnectionName(releaseName, outputName string) string {
	return fmt.Sprintf(ConnectionNameFormat, releaseName, outputName)
}

const ReleaseIndexOnOutputConnection = "releaseIndexOnOutputConnection"

func (r *ReleaseReconciler) FindOutputConnectionFromRelease(ctx context.Context, release client.Object, logger logr.Logger) []string {
	connections := kv1alpha1.ConnectionList{}
	listOps := &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(ReleaseIndexOnOutputConnection, release.GetName()),
		Namespace:     release.GetNamespace(),
	}
	err := r.List(ctx, &connections, listOps)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "findOutputConnectionFromRelease(): Unable to find release bindings")
		}
		return []string{}
	}
	requests := make([]string, 0, 10)
	for _, item := range connections.Items {
		requests = append(requests, item.GetName())
	}
	return requests
}
