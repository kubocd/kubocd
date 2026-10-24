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
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// afterRenderPackage has the package fields and the module fields given.
const afterRenderPackage = `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
%s
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
%s
`

// afterRenderContext holds the canary, and a negative duration that renders
// as 203h18m44s.
const afterRenderContext = `{"db":{"host":"pg","password":"` + canaryPassword + `("},"timeout":"-731924s"}`

// The messages that stand for those of the API server and of cel-go.
const (
	objectWithheld            = "message withheld, as it can quote the object"
	conditionOrObjectWithheld = "message withheld, as it can quote the condition or the object"
)

// postgresContract is the contract of the connections that exist.
const postgresContract = "postgres"

// fluxDuration is the pattern of the durations in the HelmRelease CRD.
var fluxDuration = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`)

// validatingCreates refuses, with the error of the API server, the
// HelmRelease durations that do not match the pattern of the CRD, and the
// connection names that are not DNS subdomains. Both errors quote the value.
func validatingCreates(r *ReleaseReconciler) {
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			var refused field.ErrorList
			switch o := obj.(type) {
			case *fluxv2.HelmRelease:
				if o.Spec.Timeout != nil && !fluxDuration.MatchString(o.Spec.Timeout.Duration.String()) {
					refused = append(refused, field.Invalid(field.NewPath("spec", "timeout"), o.Spec.Timeout.Duration.String(),
						"spec.timeout in body should match '"+fluxDuration.String()+"'"))
				}
			case *kv1alpha1.Connection, *kv1alpha1.ClusterConnection:
				if messages := validation.IsDNS1123Subdomain(obj.GetName()); len(messages) > 0 {
					refused = append(refused, field.Invalid(field.NewPath("metadata", "name"), obj.GetName(), strings.Join(messages, ",")))
				}
			}
			if len(refused) > 0 {
				gvk, err := apiutil.GVKForObject(obj, c.Scheme())
				if err != nil {
					return err
				}
				return apierrors.NewInvalid(gvk.GroupKind(), obj.GetName(), refused)
			}
			return c.Create(ctx, obj, opts...)
		},
	})
}

// forbiddingCanaryReads refuses, as RBAC would, to read an object whose name
// holds the canary. The error quotes the name.
func forbiddingCanaryReads(r *ReleaseReconciler) {
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if strings.Contains(key.Name, canaryPassword[:7]) {
				gvk, err := apiutil.GVKForObject(obj, c.Scheme())
				if err != nil {
					return err
				}
				resource := schema.GroupResource{Group: gvk.Group, Resource: strings.ToLower(gvk.Kind) + "s"}
				return apierrors.NewForbidden(resource, key.Name, errors.New("access denied"))
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
}

// forbiddingHelmReleaseReads refuses, as RBAC would, to read a HelmRelease.
func forbiddingHelmReleaseReads(r *ReleaseReconciler) {
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*fluxv2.HelmRelease); ok {
				return apierrors.NewForbidden(schema.GroupResource{Group: fluxv2.GroupVersion.Group, Resource: "helmreleases"}, key.Name, errors.New("access denied"))
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
}

// refusingCanaryLists refuses, as the API server would, to list in a
// namespace or with a field selector that holds the canary. RBAC quotes the
// namespace, and a rejected selector is quoted as given.
func refusingCanaryLists(r *ReleaseReconciler) {
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			listOptions := client.ListOptions{}
			listOptions.ApplyOptions(opts)
			if strings.Contains(listOptions.Namespace, canaryPassword[:7]) {
				resource := schema.GroupResource{Group: kv1alpha1.GroupVersion.Group, Resource: "connections"}
				return apierrors.NewForbidden(resource, "", fmt.Errorf("User \"kubocd\" cannot list resource %q in API group %q in the namespace %q",
					resource.Resource, resource.Group, listOptions.Namespace))
			}
			if listOptions.FieldSelector != nil && strings.Contains(listOptions.FieldSelector.String(), canaryPassword[:7]) {
				return apierrors.NewBadRequest(fmt.Sprintf("unable to parse the field selector %q", listOptions.FieldSelector))
			}
			return c.List(ctx, list, opts...)
		},
	})
}

// refusingCanaryDeletes refuses, as an admission webhook would, to delete an
// object whose values hold the canary. The denial quotes the values.
func refusingCanaryDeletes(r *ReleaseReconciler) {
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			var values *apiextensionsv1.JSON
			switch o := obj.(type) {
			case *fluxv2.HelmRelease:
				values = o.Spec.Values
			case *kv1alpha1.Connection:
				values = o.Spec.Values
			case *kv1alpha1.ClusterConnection:
				values = o.Spec.Values
			}
			if values == nil || !strings.Contains(string(values.Raw), canaryPassword[:7]) {
				return c.Delete(ctx, obj, opts...)
			}
			gvk, err := apiutil.GVKForObject(obj, c.Scheme())
			if err != nil {
				return err
			}
			resource := schema.GroupResource{Group: gvk.Group, Resource: strings.ToLower(gvk.Kind) + "s"}
			return apierrors.NewForbidden(resource, obj.GetName(), fmt.Errorf("admission webhook \"policy.example.com\" denied the request: the values %s are protected", values.Raw))
		},
	})
}

// refusingCanaryStatus refuses, as a validation rule would, a status of the
// Release whose usage holds the canary. The error quotes the usage.
func refusingCanaryStatus(r *ReleaseReconciler) {
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if release, ok := obj.(*kv1alpha1.Release); ok {
				for key, usage := range release.Status.Usage {
					if strings.Contains(usage, canaryPassword[:7]) {
						return apierrors.NewInvalid(schema.GroupKind{Group: kv1alpha1.GroupVersion.Group, Kind: "Release"}, release.Name,
							field.ErrorList{field.Invalid(field.NewPath("status", "usage").Key(key), usage, "refused by a validation rule")})
					}
				}
			}
			return c.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	})
}

// afterAFailedStatusUpdate stands for a reconciler whose previous status
// update of the Release demo failed: the next failure is returned.
func afterAFailedStatusUpdate(r *ReleaseReconciler) {
	r.statusFailures.fail(types.NamespacedName{Namespace: "apps", Name: "demo"})
}

// deletingTheRelease deletes the Release demo, which a second finalizer keeps
// once the controller removes its own.
func deletingTheRelease(t *testing.T) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		release := &kv1alpha1.Release{}
		if err := r.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
			t.Fatal(err)
		}
		release.Finalizers = append(release.Finalizers, "test.kubocd.kubotal.io/keep")
		if err := r.Update(context.Background(), release); err != nil {
			t.Fatal(err)
		}
		if err := r.Delete(context.Background(), release); err != nil {
			t.Fatal(err)
		}
	}
}

// refusingCanaryReleaseUpdates refuses, as an admission webhook would, an
// update of a Release whose status holds the canary. The denial quotes the
// status.
func refusingCanaryReleaseUpdates(r *ReleaseReconciler) {
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if release, ok := obj.(*kv1alpha1.Release); ok {
				for _, usage := range release.Status.Usage {
					if strings.Contains(usage, canaryPassword[:7]) {
						return apierrors.NewForbidden(schema.GroupResource{Group: kv1alpha1.GroupVersion.Group, Resource: "releases"}, release.Name,
							fmt.Errorf("admission webhook \"policy.example.com\" denied the request: the usage %v is protected", release.Status.Usage))
					}
				}
			}
			return c.Update(ctx, obj, opts...)
		},
	})
}

// withCanaryUsage stands for an earlier render of the Release demo, which left
// the canary in its status.
func withCanaryUsage(t *testing.T) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		release := &kv1alpha1.Release{}
		if err := r.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
			t.Fatal(err)
		}
		release.Status.Usage = map[string]string{"text": canaryPassword + "("}
		if err := r.Status().Update(context.Background(), release); err != nil {
			t.Fatal(err)
		}
	}
}

// withoutFinalizer stands for a Release demo that the controller never
// reconciled.
func withoutFinalizer(t *testing.T) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		release := &kv1alpha1.Release{}
		if err := r.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
			t.Fatal(err)
		}
		release.Finalizers = nil
		if err := r.Update(context.Background(), release); err != nil {
			t.Fatal(err)
		}
	}
}

// canaryValues are values of an object that quote the canary.
const canaryValues = `{"password":"` + canaryPassword + `("}`

// helmReleaseWithCanaryValues is the HelmRelease of the module main, with the
// values of an earlier render.
func helmReleaseWithCanaryValues() *fluxv2.HelmRelease {
	helmRelease := readyHelmRelease()
	helmRelease.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(canaryValues)}
	return helmRelease
}

// ownedByDemo makes the Release demo the controller of object.
func ownedByDemo(object client.Object) client.Object {
	controller := true
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: kv1alpha1.GroupVersion.String(), Kind: "Release", Name: "demo", UID: "demo", Controller: &controller}})
	return object
}

// logLine returns the line of log whose message is msg.
func logLine(log string, msg string) string {
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `"msg"="`+msg+`"`) {
			return line
		}
	}
	return ""
}

// withObjects creates objects before the reconciliation.
func withObjects(t *testing.T, objects ...client.Object) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		for _, object := range objects {
			if err := r.Create(context.Background(), object); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// readyHelmRelease is the HelmRelease of the module main, deployed by the
// Release demo.
func readyHelmRelease() *fluxv2.HelmRelease {
	helmRelease := &fluxv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-main", Namespace: "apps"},
		Status: fluxv2.HelmReleaseStatus{Conditions: []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Succeeded", LastTransitionTime: metav1.Now()},
		}},
	}
	ownedByDemo(helmRelease)
	return helmRelease
}

// contextCondition is a condition on the Context of the Release.
const contextCondition = `conditions: |
  - group: kubocd.kubotal.io
    kind: Context
    name: %s
    namespace: apps
    cel: %s`

func TestErrorsAfterTheRenderStayOutOfStatusEventsAndLog(t *testing.T) {
	cases := map[string]struct {
		packageFields string
		moduleFields  string
		options       func(t *testing.T) []func(*ReleaseReconciler)
		want          string
	}{
		"specPatch key from the Context": {moduleFields: `    specPatch: |
      {{ .Context.db.password }}: x`,
			want: "failed to populate HelmRelease 'demo-main': specPatch of module 'main' has a field that HelmRelease does not define"},
		"specPatch duration from the Context": {moduleFields: `    specPatch: |
      timeout: {{ .Context.db.password }}`,
			want: "failed to populate HelmRelease 'demo-main': specPatch of module 'main' has a value of the wrong type or format"},
		"onFailureStrategy from the Context": {moduleFields: `    onFailureStrategy: "{{ .Context.db.password }}"`,
			want: "failed to populate HelmRelease 'demo-main': onFailureStrategy of module 'main' is not a known strategy"},
		"timeout from the Context": {moduleFields: `    timeout: "{{ .Context.db.password }}"`,
			want: "error on rendering: module 'main': could not render 'timeout' template: the rendered text is not a duration"},
		"enabled from the Context": {moduleFields: `    enabled: "{{ .Context.db.password }}"`,
			want: "error on rendering: module 'main': could not render 'enabled' template: the rendered text is not a boolean"},
		"timeout refused by the API server": {moduleFields: `    timeout: "{{ .Context.timeout }}"`,
			options: func(*testing.T) []func(*ReleaseReconciler) { return []func(*ReleaseReconciler){validatingCreates} },
			want:    "error while creating HelmRelease 'demo-main': the API server answered Invalid for spec.timeout, " + objectWithheld},
		"output name refused by the API server": {packageFields: `outputs: |
  - contract: database
    name: "{{ .Context.db.password }}"`,
			options: func(t *testing.T) []func(*ReleaseReconciler) {
				return []func(*ReleaseReconciler){withObjects(t, readyHelmRelease()), validatingCreates}
			},
			want: "output[0]: error while creating its Connection: the API server answered Invalid for metadata.name, " + objectWithheld},
		"cluster output name refused by the API server": {packageFields: `outputs: |
  - contract: database
    kind: ClusterConnection
    name: "{{ .Context.db.password }}"`,
			options: func(t *testing.T) []func(*ReleaseReconciler) {
				return []func(*ReleaseReconciler){withObjects(t, readyHelmRelease()), validatingCreates}
			},
			want: "output[0]: error while creating its ClusterConnection: the API server answered Invalid for metadata.name, " + objectWithheld},
		"input contract from the Context": {packageFields: `inputs: |
  - contract: "{{ .Context.db.password }}"
    connectionRef:
      name: pg`,
			options: func(t *testing.T) []func(*ReleaseReconciler) {
				return []func(*ReleaseReconciler){withObjects(t, readyConnection("pg", postgresContract, `{"host":"pg"}`))}
			},
			want: "input#1: the connection has another contract than the input"},
		"input connection refused by the API server": {packageFields: `inputs: |
  - contract: database
    connectionRef:
      name: "{{ .Context.db.password }}"`,
			options: func(*testing.T) []func(*ReleaseReconciler) { return []func(*ReleaseReconciler){forbiddingCanaryReads} },
			want:    "input#1: could not get the Connection: the API server answered Forbidden, " + objectWithheld},
		"input lookup by Release refused by the API server": {packageFields: `inputs: |
  - contract: database
    releaseRef:
      name: other
      namespace: "{{ .Context.db.password }}"`,
			options: func(*testing.T) []func(*ReleaseReconciler) { return []func(*ReleaseReconciler){refusingCanaryLists} },
			want:    "input#1: could not list the Connections: the API server answered Forbidden, " + objectWithheld},
		"cluster input lookup by Release refused by the API server": {packageFields: `inputs: |
  - contract: database
    kind: ClusterConnection
    releaseRef:
      name: other
      namespace: "{{ .Context.db.password }}"`,
			options: func(*testing.T) []func(*ReleaseReconciler) { return []func(*ReleaseReconciler){refusingCanaryLists} },
			want:    "input#1: could not list the ClusterConnections: the API server answered BadRequest, " + objectWithheld},
		"input lookup by contract refused by the API server": {packageFields: `inputs: |
  - contract: database
    connectionRef:
      namespace: "{{ .Context.db.password }}"`,
			options: func(*testing.T) []func(*ReleaseReconciler) { return []func(*ReleaseReconciler){refusingCanaryLists} },
			want:    "input#1: could not list the Connections: the API server answered Forbidden, " + objectWithheld},
		"cluster input lookup by contract refused by the API server": {packageFields: `inputs: |
  - contract: "{{ .Context.db.password }}"
    kind: ClusterConnection`,
			options: func(*testing.T) []func(*ReleaseReconciler) { return []func(*ReleaseReconciler){refusingCanaryLists} },
			want:    "input#1: could not list the ClusterConnections: the API server answered BadRequest, " + objectWithheld},
		"HelmRelease read refused by the API server": {
			options: func(*testing.T) []func(*ReleaseReconciler) {
				return []func(*ReleaseReconciler){forbiddingHelmReleaseReads}
			},
			want: "could not get HelmRelease 'demo-main': the API server answered Forbidden, " + objectWithheld},
		"deletion of a disabled module refused by the API server": {moduleFields: `    enabled: "false"`,
			options: func(t *testing.T) []func(*ReleaseReconciler) {
				return []func(*ReleaseReconciler){withObjects(t, helmReleaseWithCanaryValues()), refusingCanaryDeletes}
			},
			want: "error while deleting HelmRelease 'demo-main': the API server answered Forbidden, " + objectWithheld},
		"cel expression from the Context": {packageFields: fmt.Sprintf(contextCondition, "cluster", `"spec.context.db.host == '{{ .Context.db.password }}'"`),
			want: "Waiting for cond.#1: Context.kubocd.kubotal.io 'apps/cluster': the cel expression is false"},
		"cel error quoting the object": {packageFields: fmt.Sprintf(contextCondition, "cluster", `"timestamp(spec.context.db.password) > timestamp('2020-01-01T00:00:00Z')"`),
			want: "Waiting for cond.#1: Context.kubocd.kubotal.io 'apps/cluster': unable to evaluate the cel expression on this object, " + conditionOrObjectWithheld},
		"invalid cel expression from the Context": {packageFields: fmt.Sprintf(contextCondition, "cluster", `"{{ .Context.db.password }} =="`),
			want: "condition[0]: the cel expression is not valid, " + conditionOrObjectWithheld},
		"condition object refused by the API server": {packageFields: fmt.Sprintf(contextCondition, `"{{ .Context.db.password }}"`, `""`),
			options: func(*testing.T) []func(*ReleaseReconciler) { return []func(*ReleaseReconciler){forbiddingCanaryReads} },
			want:    "condition[0]: unable to fetch the object: the API server answered Forbidden, " + objectWithheld},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var options []func(*ReleaseReconciler)
			if c.options != nil {
				options = c.options(t)
			}
			got := reconcileRelease(t, fmt.Sprintf(afterRenderPackage, c.packageFields, c.moduleFields), "", afterRenderContext, options...)
			for sink, text := range got.sinks() {
				for _, quoted := range []string{canaryPassword[:7], "203h18m44s"} {
					if strings.Contains(text, quoted) {
						t.Errorf("the %s quotes the data:\n%s", sink, text)
					}
				}
			}
			got.mustReport(t, c.want)
		})
	}
}

func TestACreatedConnectionIsLogged(t *testing.T) {
	cases := map[kv1alpha1.Kind]struct{ msg, name string }{
		kv1alpha1.KindConnection:        {"Created connection", `"name"="kcd-demo-db" "namespace"="apps" "output"="db"`},
		kv1alpha1.KindClusterConnection: {"Created clusterConnection", `"name"="kcd-apps-demo-db" "namespace"="apps" "output"="db"`},
	}
	for kind, want := range cases {
		t.Run(string(kind), func(t *testing.T) {
			manifest := fmt.Sprintf(afterRenderPackage, fmt.Sprintf(outputPackage, databaseContract, kind, ""), "")
			got := reconcileRelease(t, manifest, "", afterRenderContext, withObjects(t, readyHelmRelease()))
			if line := logLine(got.log, want.msg); got.err != nil || !strings.Contains(line, want.name) {
				t.Fatalf("got error %v and log\n%s\nwant a line %q with %s", got.err, got.log, want.msg, want.name)
			}
		})
	}
}

// withOverride adds override to the module overrides of the Release.
func withOverride(t *testing.T, override kv1alpha1.ModuleOverrideSpec) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		release := &kv1alpha1.Release{}
		if err := r.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
			t.Fatal(err)
		}
		release.Spec.ModuleOverrides = append(release.Spec.ModuleOverrides, override)
		if err := r.Update(context.Background(), release); err != nil {
			t.Fatal(err)
		}
	}
}

func TestASpecPatchValueThatTheReleaseReplacesIsAccepted(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", `    specPatch: |
      timeout: {{ .Context.db.password }}`)
	override := kv1alpha1.ModuleOverrideSpec{Module: "main", Timeout: &metav1.Duration{Duration: 5 * time.Minute}}
	got := reconcileRelease(t, manifest, "", afterRenderContext, withOverride(t, override))
	if want := "HelmRelease main: Deploying..."; got.err != nil || got.status != want {
		t.Fatalf("got error %v and status %q, want status %q", got.err, got.status, want)
	}
}

func TestAFieldThatTheReleaseAddsIsNotBlamedOnSpecPatch(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", `    specPatch: |
      install:
        createNamespace: true`)
	override := kv1alpha1.ModuleOverrideSpec{Module: "main", SpecPatch: &apiextensionsv1.JSON{Raw: []byte(`{"nope":1}`)}}
	got := reconcileRelease(t, manifest, "", afterRenderContext, withOverride(t, override))
	got.mustReport(t, "failed to populate HelmRelease 'demo-main': failed to unmarshal spec into HelmRelease for module 'main': "+
		"the spec merged from the package, the configuration and the Release has a field that HelmRelease does not define")
}

func TestASpecPatchValueThatTheReleaseReplacesIsNotBlamed(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", `    specPatch: |
      timeout: {{ .Context.db.password }}`)
	override := kv1alpha1.ModuleOverrideSpec{Module: "main", Timeout: &metav1.Duration{Duration: 5 * time.Minute},
		SpecPatch: &apiextensionsv1.JSON{Raw: []byte(`{"nope":1}`)}}
	got := reconcileRelease(t, manifest, "", afterRenderContext, withOverride(t, override))
	got.mustReport(t, "failed to populate HelmRelease 'demo-main': failed to unmarshal spec into HelmRelease for module 'main': "+
		"the spec merged from the package, the configuration and the Release has a field that HelmRelease does not define")
}

func TestAFailureOfSpecPatchAndOfTheReleaseIsNotBlamedOnEither(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", `    specPatch: |
      timeout: {{ .Context.db.password }}`)
	override := kv1alpha1.ModuleOverrideSpec{Module: "main", SpecPatch: &apiextensionsv1.JSON{Raw: []byte(`{"nope":1}`)}}
	got := reconcileRelease(t, manifest, "", afterRenderContext, withOverride(t, override))
	got.mustReport(t, "failed to populate HelmRelease 'demo-main': failed to unmarshal spec into HelmRelease for module 'main': "+
		"the spec merged from the package, the configuration and the Release has a value of the wrong type or format")
}

func TestAPIErrorKeepsTheReasonAndTheFields(t *testing.T) {
	gk := schema.GroupKind{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease"}
	cases := map[string]struct {
		err  error
		want string
	}{
		"invalid fields": {apierrors.NewInvalid(gk, "demo-main", field.ErrorList{
			field.NotSupported(field.NewPath("spec", "install", "strategy", "name"), canaryPassword, []string{"RemediateOnFailure"}),
			field.Invalid(field.NewPath("spec", "values").Key(canaryPassword), canaryPassword, "invalid"),
			field.Invalid(field.NewPath("spec").Child(canaryPassword+"("), canaryPassword, "invalid"),
			field.Invalid(nil, nil, "some validation rules were not checked"),
		}), "the API server answered Invalid for spec, spec.install, spec.values, " + objectWithheld},
		"fields that the kinds declare": {apierrors.NewInvalid(gk, "demo-main", field.ErrorList{
			field.Invalid(field.NewPath("status", "usage").Key(canaryPassword), canaryPassword, "refused"),
			field.Invalid(field.NewPath("metadata", "name"), canaryPassword, "invalid"),
			field.Invalid(field.NewPath("spec", "timeout"), canaryPassword, "invalid"),
		}), "the API server answered Invalid for metadata.name, spec.timeout, status.usage, " + objectWithheld},
		"fields that no kind declares": {apierrors.NewInvalid(gk, "demo-main", field.ErrorList{
			field.Invalid(field.NewPath("spec", canaryName), canaryPassword, "invalid"),
			field.Invalid(field.NewPath(canaryName, "spec"), canaryPassword, "invalid"),
		}), "the API server answered Invalid for spec, " + objectWithheld},
		"forbidden": {apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, canaryPassword, errors.New("access denied")),
			"the API server answered Forbidden, " + objectWithheld},
		"reason that the API server does not give": {&apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure,
			Code: 403, Reason: metav1.StatusReason(canaryName), Message: "denied: " + canaryPassword}},
			"the API server answered Unknown, " + objectWithheld},
		"no answer": {fmt.Errorf("invalid resource name %q: [may not contain '/']", canaryPassword+"/x"),
			"the request to the API server failed, " + objectWithheld},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(c.err.Error(), canaryPassword) {
				t.Fatalf("the error %q does not quote the canary", c.err)
			}
			if got := apiError(c.err); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// clusterConnectionOfDemo is an output ClusterConnection of the Release demo,
// with the values of an earlier render.
func clusterConnectionOfDemo() *kv1alpha1.ClusterConnection {
	return &kv1alpha1.ClusterConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "kcd-apps-demo-old"},
		Spec: kv1alpha1.ClusterConnectionSpec{Contract: postgresContract, Values: &apiextensionsv1.JSON{Raw: []byte(canaryValues)},
			ParentRelease: &kv1alpha1.ParentReleaseRef{Namespace: "apps", Name: "demo"}},
	}
}

func TestARefusedDeletionOfAConnectionIsLoggedWithoutTheAnswer(t *testing.T) {
	cases := map[string]struct {
		options func(t *testing.T) []func(*ReleaseReconciler)
		msg     string
		object  string
	}{
		"orphan Connection": {func(t *testing.T) []func(*ReleaseReconciler) {
			orphan := ownedByDemo(readyConnection("kcd-demo-old", postgresContract, canaryValues))
			return []func(*ReleaseReconciler){withObjects(t, readyHelmRelease(), orphan)}
		}, "unable to delete connection", `"connection"="kcd-demo-old"`},
		"orphan ClusterConnection": {func(t *testing.T) []func(*ReleaseReconciler) {
			return []func(*ReleaseReconciler){withObjects(t, readyHelmRelease(), clusterConnectionOfDemo())}
		}, "unable to delete clusterConnection", `"clusterConnection"="kcd-apps-demo-old"`},
		"ClusterConnection of a deleted Release": {func(t *testing.T) []func(*ReleaseReconciler) {
			return []func(*ReleaseReconciler){withObjects(t, clusterConnectionOfDemo()), deletingTheRelease(t)}
		}, "Failed to delete ClusterConnection", `"clusterConnection"="kcd-apps-demo-old"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			options := append(c.options(t), refusingCanaryDeletes)
			got := reconcileRelease(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext, options...)
			for sink, text := range got.sinks() {
				if strings.Contains(text, canaryPassword[:7]) {
					t.Errorf("the %s quotes the data:\n%s", sink, text)
				}
			}
			line := logLine(got.log, c.msg)
			if !strings.Contains(line, `"error"="the API server answered Forbidden, `+objectWithheld+`"`) || !strings.Contains(line, c.object) {
				t.Fatalf("got log\n%s\nwant a line %q with the reason of the refusal and %s", got.log, c.msg, c.object)
			}
		})
	}
}

// The folder of the testbed has no transient flag, which SafeRemove requires.
func TestAFolderThatTheDeletionOfAReleaseCannotRemoveIsLogged(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext, deletingTheRelease(t))
	got := testbed.reconcile(t)
	folder := `"folder"="` + filepath.Join(testbed.reconciler.ServerRoot, "hr", "apps", "demo") + `"`
	if line := logLine(got.log, "Failed to remove helm repository folder"); !strings.Contains(line, folder) {
		t.Fatalf("got log\n%s\nwant a line naming the folder with %s", got.log, folder)
	}
}

func TestARefusedStatusUpdateIsReturnedWithoutTheAnswer(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, `usage:
  text: "{{ .Context.db.password }}"`, "")
	got := reconcileRelease(t, manifest, "", afterRenderContext, withObjects(t, readyHelmRelease()), refusingCanaryStatus, afterAFailedStatusUpdate)
	for sink, text := range got.sinks() {
		if strings.Contains(text, canaryPassword[:7]) {
			t.Errorf("the %s quotes the data:\n%s", sink, text)
		}
	}
	if want := "could not update the Release status: the API server answered Invalid for status.usage, " + objectWithheld; got.err == nil || got.err.Error() != want {
		t.Fatalf("got returned error %v, want %q", got.err, want)
	}
}

func TestARefusedFinalizerUpdateIsReturnedWithoutTheAnswer(t *testing.T) {
	cases := map[string]struct {
		options func(t *testing.T) []func(*ReleaseReconciler)
		want    string
	}{
		"finalizer to add": {func(t *testing.T) []func(*ReleaseReconciler) {
			return []func(*ReleaseReconciler){withCanaryUsage(t), withoutFinalizer(t), refusingCanaryReleaseUpdates}
		}, "could not add the finalizer to the Release: the API server answered Forbidden, " + objectWithheld},
		"finalizer to remove": {func(t *testing.T) []func(*ReleaseReconciler) {
			return []func(*ReleaseReconciler){withCanaryUsage(t), deletingTheRelease(t), refusingCanaryReleaseUpdates}
		}, "could not remove the finalizer of the Release: the API server answered Forbidden, " + objectWithheld},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := reconcileRelease(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext, c.options(t)...)
			for sink, text := range got.sinks() {
				if strings.Contains(text, canaryPassword[:7]) {
					t.Errorf("the %s quotes the data:\n%s", sink, text)
				}
			}
			if got.err == nil || got.err.Error() != c.want {
				t.Fatalf("got returned error %v, want %q", got.err, c.want)
			}
		})
	}
}

var _ = Describe("An error of the API server", func() {
	It("keeps the reason and the field, not the name the API server refuses", func() {
		connection := &kv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "kcd-demo-" + canaryPassword + "(", Namespace: "default"},
			Spec:       kv1alpha1.ConnectionSpec{Contract: postgresContract},
		}
		err := k8sClient.Create(ctx, connection)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(canaryPassword))
		Expect(apiError(err)).To(Equal("the API server answered Invalid for metadata.name, " + objectWithheld))
	})
})
