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

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ConnectionFacade is the interface hiding differences between Connection and ClusterConnection
// +kubebuilder:object:generate=false
type ConnectionFacade interface {
	client.Object

	GetKind() Kind
	GetName() string
	GetNamespace() string

	GetInterface() string
	GetStatusPhase() ConnectionPhase
	GetOutputName() string
	GetPriority() int
	// GetValues() *apiextensionsv1.JSON
	GetValuesRaw() []byte
}

// ConnectionPhase is used both for Connection and ClusterConnection
type ConnectionPhase string

const ConnectionPhaseReady = ConnectionPhase("READY")
const ConnectionPhaseError = ConnectionPhase("ERROR")
const ConnectionPhaseDisabled = ConnectionPhase("DISABLED")

// const ConnectionPhasePending = ConnectionPhase("PENDING")
