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

package cmd

import (
	"context"
	"fmt"
	kapi "kubocd/api/v1alpha1"
	"kubocd/internal/controller"
	"kubocd/internal/kubopackage"
	"kubocd/internal/tmpl"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"
)

const renderCanary = "s3cr3t-SEC1-canary"

// lookupPackage has the inputs given, rendered from the Context.
const lookupPackage = `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
inputs: |
%s
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
`

// renderedInputs renders the inputs given, the Context holding namespace as
// ns, as kubocd render does for a Release of the namespace apps.
func renderedInputs(t *testing.T, inputs string, namespace string) []*kubopackage.InputRendered {
	t.Helper()
	pck := &kubopackage.Package{}
	if err := yaml.UnmarshalStrict([]byte(fmt.Sprintf(lookupPackage, inputs)), pck); err != nil {
		t.Fatalf("invalid test manifest: %v", err)
	}
	container := &kubopackage.PckContainer{}
	err := container.SetPackage(pck, nil, "0.1.0@sha256:0123456789abcdef", nil, tmpl.PackageFunctions(false))
	if err != nil {
		t.Fatalf("invalid test package: %v", err)
	}
	model := map[string]interface{}{"Context": map[string]interface{}{"ns": namespace}}
	rendered, err := container.Package.RenderInputs(model, "apps")
	if err != nil {
		t.Fatal(err)
	}
	return rendered
}

// unreachedAPIServer is a client that sends nothing for an invalid namespace:
// client-go refuses it while it builds the request, and quotes it.
func unreachedAPIServer(t *testing.T) client.Client {
	t.Helper()
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(kapi.GroupVersion.WithKind(string(kapi.KindConnection)), meta.RESTScopeNamespace)
	mapper.Add(kapi.GroupVersion.WithKind(string(kapi.KindClusterConnection)), meta.RESTScopeRoot)
	c, err := client.New(&rest.Config{Host: "http://127.0.0.1:1"}, client.Options{Scheme: scheme, Mapper: mapper})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// refusingAPIServer refuses every list, as RBAC would, quoting the namespace.
func refusingAPIServer(*testing.T) client.Client {
	return interceptor.NewClient(fake.NewClientBuilder().WithScheme(scheme).Build(), interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			listOptions := client.ListOptions{}
			listOptions.ApplyOptions(opts)
			resource := schema.GroupResource{Group: kapi.GroupVersion.Group, Resource: "connections"}
			if _, ok := list.(*kapi.ClusterConnectionList); ok {
				resource.Resource = "clusterconnections"
			}
			scope := "at the cluster scope"
			if listOptions.Namespace != "" {
				scope = fmt.Sprintf("in the namespace %q", listOptions.Namespace)
			}
			refusal := fmt.Errorf("User \"operator\" cannot list resource %q in API group %q %s",
				resource.Resource, resource.Group, scope)
			return apierrors.NewForbidden(resource, "", refusal)
		},
	})
}

func TestRenderGivesARefusedInputLookupWithoutTheRequest(t *testing.T) {
	const (
		byRelease = `  - contract: database
    releaseRef:
      name: other
      namespace: "{{ .Context.ns }}"`
		byContract = `  - contract: database
    connectionRef:
      namespace: "{{ .Context.ns }}"`
		clusterByRelease = `  - contract: database
    kind: ClusterConnection
    releaseRef:
      name: other
      namespace: "{{ .Context.ns }}"`
		clusterByContract = `  - contract: database
    kind: ClusterConnection`
		connections        = "input#1: could not list the Connections: "
		clusterConnections = "input#1: could not list the ClusterConnections: "
		failed             = "the request to the API server failed, message withheld, as it can quote the object"
		answered           = "the API server answered Forbidden, message withheld, as it can quote the object"
	)
	cases := map[string]struct {
		inputs    string
		namespace string
		client    func(*testing.T) client.Client
		want      string
	}{
		"lookup by Release, namespace refused by client-go": {
			byRelease, renderCanary + "/x", unreachedAPIServer, connections + failed},
		"lookup by contract, namespace refused by client-go": {
			byContract, renderCanary + "/x", unreachedAPIServer, connections + failed},
		"lookup by Release, refused by the API server": {
			byRelease, renderCanary, refusingAPIServer, connections + answered},
		"lookup by contract, refused by the API server": {
			byContract, renderCanary, refusingAPIServer, connections + answered},
		"cluster lookup by Release, refused by the API server": {
			clusterByRelease, renderCanary, refusingAPIServer, clusterConnections + answered},
		"cluster lookup by contract, refused by the API server": {
			clusterByContract, renderCanary, refusingAPIServer, clusterConnections + answered},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			k8sClient := c.client(t)
			if strings.Contains(c.inputs, ".Context.ns") {
				raw := k8sClient.List(context.Background(), &kapi.ConnectionList{}, client.InNamespace(c.namespace))
				if raw == nil || !strings.Contains(raw.Error(), renderCanary) {
					t.Fatalf("the refusal %v does not quote the namespace", raw)
				}
			}
			helper := &buildInputModelHelper{Client: k8sClient}
			_, err := controller.BuildInputModel(context.Background(), helper, renderedInputs(t, c.inputs, c.namespace))
			if err == nil || err.Error() != c.want {
				t.Fatalf("got error %v, want %q", err, c.want)
			}
		})
	}
}
