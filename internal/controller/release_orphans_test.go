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
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// refusingOwnerLists refuses the lists of the output connections of a Release
// through the index given, with a denial that quotes the canary, as a policy
// can quote any text.
func refusingOwnerLists(index string, resource string) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				listOptions := client.ListOptions{}
				listOptions.ApplyOptions(opts)
				if listOptions.FieldSelector != nil {
					if _, ok := listOptions.FieldSelector.RequiresExactMatch(index); ok {
						return apierrors.NewForbidden(schema.GroupResource{Group: kv1alpha1.GroupVersion.Group, Resource: resource}, "",
							fmt.Errorf("denied by the policy %q", canaryPassword))
					}
				}
				return c.List(ctx, list, opts...)
			},
		})
	}
}

func TestAReleaseIsNotReadyWhileItsConnectionsCannotBeListed(t *testing.T) {
	cases := map[string]struct {
		refusing func(*ReleaseReconciler)
		want     string
	}{
		"Connections": {refusingOwnerLists(ReleaseIndexOnOutputConnection, "connections"),
			"could not list its Connections: the API server answered Forbidden, " + objectWithheld},
		"ClusterConnections": {refusingOwnerLists(ReleaseIndexOnOutputClusterConnection, "clusterconnections"),
			"could not list its ClusterConnections: the API server answered Forbidden, " + objectWithheld},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := reconcileRelease(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
				withObjects(t, readyHelmRelease()), c.refusing)
			for sink, text := range got.sinks() {
				if strings.Contains(text, canaryPassword[:7]) {
					t.Errorf("the %s quotes the answer:\n%s", sink, text)
				}
			}
			if got.err == nil || got.status != c.want {
				t.Fatalf("got error %v and status %q, want an error and the status %q", got.err, got.status, c.want)
			}
		})
	}
}
