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
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

// outputOfDemo returns the status of the output db of the Release demo.
func outputOfDemo(t *testing.T, testbed *releaseTestbed) kv1alpha1.ReleaseOutputConnection {
	t.Helper()
	release := &kv1alpha1.Release{}
	if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
		t.Fatal(err)
	}
	return release.Status.OutputConnectionByName["db"]
}

func TestTheStatusOfAReleaseNamesItsOutputConnectionFromItsCreation(t *testing.T) {
	cases := map[kv1alpha1.Kind]kv1alpha1.ReleaseOutputConnection{
		kv1alpha1.KindConnection:        {Kind: kv1alpha1.KindConnection, Name: "kcd-demo-db", Namespace: "apps"},
		kv1alpha1.KindClusterConnection: {Kind: kv1alpha1.KindClusterConnection, Name: "kcd-apps-demo-db"},
	}
	for kind, want := range cases {
		t.Run(string(kind), func(t *testing.T) {
			manifest := fmt.Sprintf(afterRenderPackage, fmt.Sprintf(outputPackage, databaseContract, kind, ""), "")
			testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, withObjects(t, readyHelmRelease()))
			for _, pass := range []string{"creation", "update"} {
				if got := testbed.reconcile(t); got.err != nil {
					t.Fatalf("%s: got error %v", pass, got.err)
				}
				if got := outputOfDemo(t, testbed); got != want {
					t.Fatalf("%s: got the output %+v, want %+v", pass, got, want)
				}
			}
		})
	}
}
