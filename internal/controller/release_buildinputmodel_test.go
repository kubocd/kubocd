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

//nolint:goconst // test inputs read better inline
package controller

import (
	"context"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// releaseConnections serves the Connections given as the outputs of every
// Release, and no ClusterConnection.
type releaseConnections struct {
	client.Client
	connections []kv1alpha1.Connection
}

func (c releaseConnections) FindOutputConnectionsFromRelease(context.Context, types.NamespacedName) ([]kv1alpha1.Connection, ReconcileError) {
	return c.connections, nil
}

func (c releaseConnections) FindOutputClusterConnectionsFromRelease(context.Context, types.NamespacedName) ([]kv1alpha1.ClusterConnection, ReconcileError) {
	return nil, nil
}

func (c releaseConnections) FindConnectionsFromContract(context.Context, string, string) ([]kv1alpha1.Connection, ReconcileError) {
	return nil, nil
}

func (c releaseConnections) FindClusterConnectionsFromContract(context.Context, string) ([]kv1alpha1.ClusterConnection, ReconcileError) {
	return nil, nil
}

func TestTheErrorOnTheValuesOfAConnectionNumbersItsInput(t *testing.T) {
	// Connections of equal priority are sorted by name, the last name first.
	cases := map[string]struct{ invalid, valid string }{
		"connection sorted first":  {"pg-b", "pg-a"},
		"connection sorted second": {"pg-a", "pg-b"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			helper := releaseConnections{connections: []kv1alpha1.Connection{
				*readyConnection(c.valid, databaseContract, `{"host":"pg"}`),
				*readyConnection(c.invalid, databaseContract, `["pg"]`),
			}}
			inputs := []*kubopackage.InputRendered{
				{Contract: "cache", Alias: "cache", Optional: true},
				{Contract: "queue", Alias: "queue", Optional: true},
				{Contract: databaseContract, Alias: "db", AllowMultiple: true},
			}
			for _, input := range inputs {
				input.ReleaseRef.Namespace, input.ReleaseRef.Name = "apps", "provider"
			}
			_, rErr := BuildInputModel(context.Background(), helper, inputs)
			if want := "input#3: the values of the connection are not a map"; rErr == nil || rErr.Error() != want {
				t.Fatalf("got %v, want %q", rErr, want)
			}
		})
	}
}
