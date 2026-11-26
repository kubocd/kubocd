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
	"encoding/json"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"slices"
	"strings"
	"testing"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// invalidDuration is the Duration decoded from the text given.
func invalidDuration(t *testing.T, text string) *kv1alpha1.Duration {
	t.Helper()
	d := &kv1alpha1.Duration{}
	raw, _ := json.Marshal(text)
	if err := json.Unmarshal(raw, d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAnInvalidDurationIsReportedOnItsReleaseWithoutRetry(t *testing.T) {
	cases := map[string]struct {
		change func(*kv1alpha1.Release)
		want   string
	}{
		"package interval": {func(r *kv1alpha1.Release) { r.Spec.Package.Interval = *invalidDuration(t, "9999999h") },
			`spec.package.interval: "9999999h" is not a duration of at most about 2562047h, such as 90s or 2m30s`},
		"package timeout": {func(r *kv1alpha1.Release) { r.Spec.Package.Timeout = invalidDuration(t, "9999999999m") },
			`spec.package.timeout: "9999999999m" is not a duration`},
		"module timeout": {func(r *kv1alpha1.Release) {
			r.Spec.ModuleOverrides = []kv1alpha1.ModuleOverrideSpec{{Module: "main", Timeout: invalidDuration(t, "9999999h")}}
		}, `spec.moduleOverrides[0].timeout: "9999999h" is not a duration`},
		"module interval": {func(r *kv1alpha1.Release) {
			r.Spec.ModuleOverrides = []kv1alpha1.ModuleOverrideSpec{{Module: "main", Interval: invalidDuration(t, "garbage")}}
		}, `spec.moduleOverrides[0].interval: "garbage" is not a duration`},
		"negative module timeout": {func(r *kv1alpha1.Release) {
			r.Spec.ModuleOverrides = []kv1alpha1.ModuleOverrideSpec{{Module: "main", Timeout: invalidDuration(t, "-5m")}}
		}, `spec.moduleOverrides[0].timeout: "-5m0s" is a negative duration`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", "")
			changeRelease(t, testbed, c.change)
			got := testbed.reconcile(t)
			state := stateOf(t, testbed)
			if got.err != nil || got.result.RequeueAfter != 0 || state.phase != kv1alpha1.ReleasePhaseError || !strings.HasPrefix(got.status, c.want) {
				t.Fatalf("got the error %v, the result %+v, %s and the status %q, want ERROR %q without retry", got.err, got.result, state,
					got.status, c.want)
			}
			if state.ready == nil || state.ready.Status != metav1.ConditionFalse || state.ready.Reason != "Failed" ||
				state.ready.Message != got.status || state.observed != 1 {
				t.Fatalf("got %s, want Ready False for the generation 1", state)
			}
			if !slices.ContainsFunc(got.events, func(e string) bool { return strings.Contains(e, "InvalidDuration") }) {
				t.Fatalf("got the events %q, want the InvalidDuration one", got.events)
			}
			source := &sourcev1.OCIRepository{}
			if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "kcd-demo"}, source); err != nil {
				t.Fatal(err)
			}
			if source.Spec.URL != "" || source.Spec.Interval.Duration != 0 {
				t.Fatalf("got the OCIRepository spec %+v, want it untouched", source.Spec)
			}
			// The text the user wrote stays in the Release
			release := &kv1alpha1.Release{}
			if err := testbed.client.Get(context.Background(), demoKey, release); err != nil {
				t.Fatal(err)
			}
			if err := release.Spec.ValidateDurations(); err == nil || !strings.HasPrefix(err.Error(), c.want) {
				t.Fatalf("got %v, want the invalid duration kept", err)
			}
		})
	}
}

func TestARenderedDurationOutOfRangeIsReportedOnItsRelease(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", `    timeout: "{{ .Context.timeout }}"`)
	got := reconcileRelease(t, manifest, "", `{"timeout":"9999999h"}`)
	want := "error on rendering: module 'main': could not render 'timeout' template: the rendered text is not a duration (such as 90s or 2m30s)"
	if got.status != want || got.err == nil || strings.Contains(got.err.Error(), "panic") {
		t.Fatalf("got the status %q and the error %v, want %q", got.status, got.err, want)
	}
}

func TestADurationOutOfRangeInASpecPatchIsReportedOnItsRelease(t *testing.T) {
	override := kv1alpha1.ModuleOverrideSpec{Module: "main", SpecPatch: &apiextensionsv1.JSON{Raw: []byte(`{"timeout":"9999999h"}`)}}
	got := reconcileRelease(t, fmt.Sprintf(afterRenderPackage, "", ""), "", "", withOverride(t, override))
	want := "failed to populate HelmRelease 'demo-main': failed to unmarshal spec into HelmRelease for module 'main': " +
		"the spec merged from the package, the configuration and the Release has a value of the wrong type or format"
	if got.status != want || got.err == nil {
		t.Fatalf("got the status %q and the error %v, want %q", got.status, got.err, want)
	}
}
