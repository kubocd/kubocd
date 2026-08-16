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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ReleaseReconciler) handleOutputClusterConnection(op *releaseOperation, clusterConnectionName string, outputRendered *kubopackage.OutputRendered) (*kv1alpha1.ClusterConnection, ReconcileError) {
	clusterConnection := &kv1alpha1.ClusterConnection{}
	err := r.Get(op.ctx, types.NamespacedName{Name: clusterConnectionName}, clusterConnection)
	//fmt.Printf("handleOutputClusterConnection(outputRendered:%s\n)\n", misc.Any2Yaml(outputRendered))
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("on ClusterConnection '%s': %w", clusterConnectionName, err), false, "ClusterConnectionAccess")
		}
		// Must create it
		op.logger.V(0).Info("Will create clusterConnection", "name", clusterConnectionName, "namespace", op.release.Namespace, "output", outputRendered.Name)
		op.outputClusterConnectionK8sName[clusterConnectionName] = struct{}{} // Mark as non-orphan
		err := r.createClusterConnection(op, outputRendered, clusterConnectionName)
		if err != nil {
			return nil, NewReconcileError(err, false, "ConnectionCreate")
		}
		r.Event(op.release, "Normal", "ClusterConnectionCreated", fmt.Sprintf("Created ClusterConnection %q", clusterConnectionName))
		op.logger.V(1).Info("Launched clusterConnection", "connectionName", clusterConnectionName)
		op.outputConnectionByName[outputRendered.Name] = kv1alpha1.ReleaseOutputConnection{
			Kind:      kv1alpha1.Kind(clusterConnection.Kind),
			Name:      clusterConnection.Name,
			Namespace: "",
			Phase:     "",
			Message:   "",
		}
		return clusterConnection, nil
	}
	// Connection exist. Update if needed
	op.outputClusterConnectionK8sName[clusterConnectionName] = struct{}{} // Mark as non-orphan
	changed, err := patchClusterConnection(r, op, clusterConnection, outputRendered)
	if err != nil {
		return nil, NewReconcileError(err, false, "ClusterConnectionPatch")
	}
	if changed {
		op.logger.V(0).Info("ClusterConnection updated", "name", clusterConnectionName, "output", outputRendered.Name)
	} else {
		op.logger.V(1).Info("ClusterConnection unchanged", "name", clusterConnectionName, "output", outputRendered.Name)
	}
	op.outputConnectionByName[outputRendered.Name] = kv1alpha1.ReleaseOutputConnection{
		Kind:      kv1alpha1.Kind(clusterConnection.Kind),
		Name:      clusterConnection.Name,
		Namespace: "",
		Phase:     clusterConnection.Status.Phase,
		Message:   clusterConnection.Status.Message,
	}
	return clusterConnection, nil
}

func patchClusterConnection(r *ReleaseReconciler, op *releaseOperation, clusterConnection *kv1alpha1.ClusterConnection, outputRendered *kubopackage.OutputRendered) (bool, error) {
	// Store original generation to detect changes
	originalGeneration := clusterConnection.Generation

	// Create a deep copy for the patch operation
	patch := client.MergeFrom(clusterConnection.DeepCopy())

	// Populate the HelmRelease with updated configuration
	err := PopulateClusterConnection(op, clusterConnection, outputRendered)
	if err != nil {
		return false, fmt.Errorf("failed to populate clusterConnection '%s': %w", clusterConnection.Name, err)
	}

	// Apply the patch
	err = r.Patch(op.ctx, clusterConnection, patch)
	if err != nil {
		return false, fmt.Errorf("error while patching clusterConnection '%s': %w", clusterConnection.Name, err)
	}
	// Check if the generation changed to determine if an update occurred
	return originalGeneration != clusterConnection.Generation, nil
}

func (r *ReleaseReconciler) createClusterConnection(op *releaseOperation, outputRendered *kubopackage.OutputRendered, clusterConnectionName string) error {
	clusterConnection := &kv1alpha1.ClusterConnection{}
	clusterConnection.SetName(clusterConnectionName)
	err := PopulateClusterConnection(op, clusterConnection, outputRendered)
	if err != nil {
		return fmt.Errorf("failed to populate clusterConnection '%s': %w", clusterConnectionName, err)
	}
	//err = ctrl.SetControllerReference(op.release, clusterConnection, r.Scheme())
	//if err != nil {
	//	return fmt.Errorf("unable to set connection '%s' owner reference: %w", clusterConnectionName, err)
	//}
	clusterConnection.Spec.ParentRelease = &kv1alpha1.ParentReleaseRef{
		Name:      op.release.Name,
		Namespace: op.release.Namespace,
	}
	if err = r.Create(op.ctx, clusterConnection); err != nil {
		return fmt.Errorf("error while creating clusterConnection '%s': %w", clusterConnectionName, err)
	}
	// This will be set by the ClusterConnection controller
	//clusterConnection.Status.Parent = fmt.Sprintf("%s/%s", op.release.Namespace, op.release.Name)
	//err = r.Status().Update(op.ctx, clusterConnection)
	//if err != nil {
	//	return fmt.Errorf("error while setting status on connection '%s': %w", clusterConnectionName, err)
	//}
	return nil
}

func PopulateClusterConnection(op *releaseOperation, clusterConnection *kv1alpha1.ClusterConnection, outputRendered *kubopackage.OutputRendered) error {
	valuesTxt, err := json.Marshal(outputRendered.Values)
	if err != nil {
		return fmt.Errorf("output '%s': could not encode values: %w", outputRendered.Name, err)
	}
	clusterConnection.Spec.Values = &v1.JSON{Raw: valuesTxt}
	clusterConnection.Spec.Contract = outputRendered.Contract
	clusterConnection.Spec.Description = outputRendered.Description
	clusterConnection.Spec.Priority = outputRendered.Priority
	clusterConnection.Spec.OutputName = outputRendered.Name
	clusterConnection.Spec.ParentRelease = &kv1alpha1.ParentReleaseRef{
		Name:      op.release.Name,
		Namespace: op.release.Namespace,
	}
	return nil
}

// BuildClusterConnectionName Same as BuildConnectionName
func BuildClusterConnectionName(releaseName, releaseNamespace, outputName string) string {
	return fmt.Sprintf("kcd-%s-%s-%s", releaseNamespace, releaseName, outputName)
}
