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

package v1alpha1

import "sigs.k8s.io/controller-runtime/pkg/client"

// InterfaceFacade is the interface hiding differences between Interface and ClusterInterface
// +kubebuilder:object:generate=false
type InterfaceFacade interface {
	client.Object

	GetKind() Kind
	GetStatusPhase() InterfacePhase
	GetSchemaRaw() []byte

	// Also used, but provided by client.Object
	//GetName() string
	//GetNamespace() string
	//GetGeneration() int64

}

type InterfacePhase string

const InterfacePhaseReady = InterfacePhase("READY")
const InterfacePhaseError = InterfacePhase("ERROR")
