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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ContractFacade is the interface hiding differences between Contract and ClusterContract
// +kubebuilder:object:generate=false
type ContractFacade interface {
	client.Object

	GetKind() Kind
	GetStatusPhase() ContractPhase
	GetSchemaRaw() []byte
	GetClaim() *ContractClaim

	// Also used, but provided by client.Object
	//GetName() string
	//GetNamespace() string
	//GetGeneration() int64

}

// ContractClaim defines the requirements a Claim on the contract can hold, and how each one is checked
// against a connection.
type ContractClaim struct {
	// The schema of the requirements. Unknown requirements are refused.
	// May be a JSON/openAPI schema or the kubocd simplified schema format.
	// +kubebuilder:validation:Optional
	Requirements *apiextensionsv1.JSON `json:"requirements,omitempty"`

	// One CEL expression by requirement, true when the connection meets it. Variables are 'connection'
	// (the values of the connection) and 'claim' (the requirements of the claim).
	// +kubebuilder:validation:Optional
	Checks map[string]string `json:"checks,omitempty"`
}

type ContractPhase string

const ContractPhaseReady = ContractPhase("READY")
const ContractPhaseError = ContractPhase("ERROR")
