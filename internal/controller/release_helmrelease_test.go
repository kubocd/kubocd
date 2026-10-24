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
	"kubocd/internal/configstore"
	"reflect"
	"testing"
	"time"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestAHelmReleaseThatTheReleaseDoesNotControlIsLeftAlone(t *testing.T) {
	const theirValues = `{"theirs":true}`
	controller := true
	owners := map[string][]metav1.OwnerReference{
		"of another Release": {{APIVersion: kv1alpha1.GroupVersion.String(), Kind: "Release", Name: "other", UID: "other",
			Controller: &controller}},
		"of an earlier Release of the same name": {{APIVersion: kv1alpha1.GroupVersion.String(), Kind: "Release", Name: "demo",
			UID: "former", Controller: &controller}},
		"without owner": nil,
	}
	for name, owner := range owners {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, module enabled %t", name, enabled), func(t *testing.T) {
				helmRelease := readyHelmRelease()
				helmRelease.SetOwnerReferences(owner)
				helmRelease.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(theirValues)}
				moduleFields := ""
				if !enabled {
					moduleFields = `    enabled: "false"`
				}
				testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", moduleFields), "", afterRenderContext,
					withObjects(t, helmRelease))
				got := testbed.reconcile(t)
				if enabled {
					want := "HelmRelease 'demo-main' exists and is not controlled by this Release"
					if got.err == nil || got.status != want {
						t.Fatalf("got error %v and status %q, want an error and the status %q", got.err, got.status, want)
					}
				} else if got.err != nil {
					t.Fatalf("got error %v, want none for a disabled module", got.err)
				}
				left := &fluxv2.HelmRelease{}
				if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo-main"}, left); err != nil {
					t.Fatalf("the HelmRelease is gone: %v", err)
				}
				if values := string(left.Spec.Values.Raw); values != theirValues {
					t.Fatalf("got the values %s of the HelmRelease, want %s", values, theirValues)
				}
			})
		}
	}
}

// getDemoMain reads the HelmRelease of the module main.
func getDemoMain(t *testing.T, testbed *releaseTestbed) *fluxv2.HelmRelease {
	t.Helper()
	helmRelease := &fluxv2.HelmRelease{}
	if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo-main"}, helmRelease); err != nil {
		t.Fatalf("the HelmRelease is gone: %v", err)
	}
	return helmRelease
}

func TestAHelmReleaseReplacedOnceReadIsLeftAlone(t *testing.T) {
	const theirValues = `{"theirs":true}`
	controller := true
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("module enabled %t", enabled), func(t *testing.T) {
			mine := readyHelmRelease()
			mine.UID = "mine"
			replacement := readyHelmRelease()
			replacement.UID = "theirs"
			replacement.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: kv1alpha1.GroupVersion.String(), Kind: "Release",
				Name: "other", UID: "other", Controller: &controller}})
			replacement.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(theirValues)}
			moduleFields := ""
			if !enabled {
				moduleFields = `    enabled: "false"`
			}
			testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", moduleFields), "", afterRenderContext,
				withObjects(t, mine), changingAfterRead(t, client.ObjectKeyFromObject(mine), replacedBy(t, replacement)))
			if got := testbed.reconcile(t); got.err == nil {
				t.Fatalf("got no error and the status %q, want the write refused", got.status)
			}
			left := getDemoMain(t, testbed)
			if left.UID != "theirs" || string(left.Spec.Values.Raw) != theirValues {
				t.Fatalf("got the HelmRelease %s with the values %s, want the replacement unchanged", left.UID, left.Spec.Values.Raw)
			}
		})
	}
}

// A control: a HelmRelease written again between the read and the write, with
// the same UID, does not make the write fail.
func TestARewriteOfTheHelmReleaseOnceReadDoesNotFailTheWrite(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("module enabled %t", enabled), func(t *testing.T) {
			mine := readyHelmRelease()
			mine.UID = "mine"
			moduleFields := ""
			if !enabled {
				moduleFields = `    enabled: "false"`
			}
			testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", moduleFields), "", afterRenderContext,
				withObjects(t, mine), changingAfterRead(t, client.ObjectKeyFromObject(mine), rewritten(t)))
			if got := testbed.reconcile(t); got.err != nil {
				t.Fatalf("got the error %v, want the HelmRelease written", got.err)
			}
			left := &fluxv2.HelmRelease{}
			err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo-main"}, left)
			if enabled && (err != nil || left.Spec.Chart == nil) {
				t.Fatalf("got the error %v and the HelmRelease %+v, want it patched", err, left.Spec)
			}
			if !enabled && err == nil {
				t.Fatalf("got the HelmRelease of a disabled module, want it deleted")
			}
		})
	}
}

// withModuleOverride gives the Release demo the override of its module main.
func withModuleOverride(t *testing.T, onFailureStrategy string, specPatch string) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		release := &kv1alpha1.Release{}
		if err := r.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
			t.Fatal(err)
		}
		override := kv1alpha1.ModuleOverrideSpec{Module: "main", OnFailureStrategy: onFailureStrategy}
		if specPatch != "" {
			override.SpecPatch = &apiextensionsv1.JSON{Raw: []byte(specPatch)}
		}
		release.Spec.ModuleOverrides = []kv1alpha1.ModuleOverrideSpec{override}
		if err := r.Update(context.Background(), release); err != nil {
			t.Fatal(err)
		}
	}
}

// specJSON is the spec of a HelmRelease as JSON.
func specJSON(t *testing.T, helmRelease *fluxv2.HelmRelease) string {
	t.Helper()
	b, err := json.Marshal(helmRelease.Spec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// p5Module sets, through specPatch, a label and a post-renderer with one
// patch.
const p5Module = `    specPatch: |
      commonMetadata:
        labels:
          kept: "yes"
      postRenderers:
        - kustomize:
            patches:
              - patch: '[{"op": "add", "path": "/metadata/labels/a", "value": "b"}]'
                target: {kind: Deployment}`

func TestAnExistingHelmReleaseGetsTheSpecOfANewOne(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", p5Module)
	created := newReleaseTestbed(t, manifest, "", afterRenderContext)
	if got := created.reconcile(t); got.err != nil {
		t.Fatal(got.err)
	}
	want := specJSON(t, getDemoMain(t, created))

	existing := readyHelmRelease()
	retries := 10
	existing.Spec.DriftDetection = &fluxv2.DriftDetection{Mode: fluxv2.DriftDetectionEnabled}
	existing.Spec.Install = &fluxv2.Install{Remediation: &fluxv2.InstallRemediation{Retries: retries}}
	existing.Spec.CommonMetadata = &fluxv2.CommonMetadata{Labels: map[string]string{"kept": "yes", "removed": "yes"}}
	existing.Spec.PostRenderers = []fluxv2.PostRenderer{{Kustomize: &fluxv2.Kustomize{
		Patches: []kustomize.Patch{{Patch: `[{"op": "add", "path": "/metadata/labels/a", "value": "b"}]`,
			Target: &kustomize.Selector{Kind: "Deployment"}}},
		Images: []kustomize.Image{{Name: "removed", NewTag: "1.0"}},
	}}}
	updated := newReleaseTestbed(t, manifest, "", afterRenderContext, withObjects(t, existing))
	if got := updated.reconcile(t); got.err != nil {
		t.Fatal(got.err)
	}
	if got := specJSON(t, getDemoMain(t, updated)); got != want {
		t.Fatalf("got the spec\n%s\nwant the spec of a new HelmRelease\n%s", got, want)
	}
}

// strategyStore holds the failure strategies of the kubocd-ctrl chart.
func strategyStore(t *testing.T) configstore.ConfigStore {
	t.Helper()
	store := configstore.New()
	retry := `{"install":{"strategy":{"name":"RetryOnFailure","retryInterval":"1m0s"}},` +
		`"upgrade":{"strategy":{"name":"RetryOnFailure","retryInterval":"1m0s"}}}`
	remediate := `{"install":{"strategy":{"name":"RemediateOnFailure"},"remediation":{"retries":10}},` +
		`"upgrade":{"strategy":{"name":"RemediateOnFailure"},"remediation":{"retries":10}}}`
	err := store.AddConfigs(&kv1alpha1.ConfigList{Items: []kv1alpha1.Config{{
		ObjectMeta: metav1.ObjectMeta{Name: "strategies"},
		Spec: kv1alpha1.ConfigSpec{OnFailureStrategies: []*kv1alpha1.OnFailureStrategySpec{
			{Name: "updateOnFailure", Strategy: &apiextensionsv1.JSON{Raw: []byte(retry)}},
			{Name: "reinstallOnFailure", Strategy: &apiextensionsv1.JSON{Raw: []byte(remediate)}},
		}, DefaultOnFailureStrategy: "updateOnFailure"},
	}}}, "kubocd")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAChangeOfFailureStrategyLeavesNoFieldOfTheFormerOne(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
		func(r *ReleaseReconciler) { r.ConfigStore = strategyStore(t) }, withModuleOverride(t, "reinstallOnFailure", ""))
	if got := testbed.reconcile(t); got.err != nil {
		t.Fatal(got.err)
	}
	if spec := getDemoMain(t, testbed).Spec; spec.Install == nil || spec.Install.Remediation == nil || spec.Install.Remediation.Retries != 10 {
		t.Fatalf("got the install %+v, want the remediation of reinstallOnFailure", spec.Install)
	}
	withModuleOverride(t, "updateOnFailure", "")(testbed.reconciler)
	if got := testbed.reconcile(t); got.err != nil {
		t.Fatal(got.err)
	}
	spec := getDemoMain(t, testbed).Spec
	install, upgrade := spec.Install, spec.Upgrade
	if install == nil || install.Strategy == nil || install.Strategy.Name != "RetryOnFailure" || install.Remediation != nil {
		t.Errorf("got the install %+v, want RetryOnFailure without remediation", install)
	}
	if upgrade == nil || upgrade.Strategy == nil || upgrade.Strategy.Name != "RetryOnFailure" || upgrade.Remediation != nil {
		t.Errorf("got the upgrade %+v, want RetryOnFailure without remediation", upgrade)
	}
}

// computedSpec is a spec as the controller merges it, before the patches of a
// case.
func computedSpec() map[string]interface{} {
	return map[string]interface{}{
		"chart": map[string]interface{}{"spec": map[string]interface{}{"chart": "demo", "version": "1.0.0",
			"sourceRef": map[string]interface{}{"kind": "HelmRepository", "name": "kcd-demo", "namespace": "apps"}, "interval": "30m0s"}},
		"values":          map[string]interface{}{"replicas": 1},
		"targetNamespace": "apps",
		"releaseName":     "demo-main",
		"dependsOn":       []map[string]string{},
		"timeout":         &metav1.Duration{Duration: 2 * time.Minute},
		"interval":        &metav1.Duration{Duration: 30 * time.Minute},
	}
}

func TestANullSetOnAnExistingHelmReleaseDecodesAsBefore(t *testing.T) {
	cases := map[string]struct{ set, null string }{
		"a string":                   {`{"storageNamespace":"old"}`, `{"storageNamespace":null}`},
		"a boolean":                  {`{"rollback":{"disableWait":true}}`, `{"rollback":{"disableWait":null}}`},
		"a boolean, in another case": {`{"rollback":{"disableWait":true}}`, `{"rollback":{"DisableWait":null}}`},
		"a struct":                   {`{}`, `{"chart":{"spec":null}}`},
		"a pointer":                  {`{"driftDetection":{"mode":"enabled"}}`, `{"driftDetection":null}`},
		"a duration pointer":         {`{}`, `{"timeout":null}`},
		"a duration":                 {`{}`, `{"interval":null}`},
		"a map":                      {`{"commonMetadata":{"labels":{"a":"b"}}}`, `{"commonMetadata":{"labels":null}}`},
		"a map key":                  {`{"commonMetadata":{"labels":{"a":"b","c":"d"}}}`, `{"commonMetadata":{"labels":{"a":null,"c":"d"}}}`},
		"a list":                     {`{"valuesFrom":[{"kind":"ConfigMap","name":"a"}]}`, `{"valuesFrom":null}`},
		"a list element":             {`{"postRenderers":[{"kustomize":{"images":[{"name":"a","newTag":"1"}]}}]}`, `{"postRenderers":[null]}`},
		"a field of a list element": {`{"postRenderers":[{"kustomize":{"images":[{"name":"a","newTag":"1"}]}}]}`,
			`{"postRenderers":[{"Kustomize":null}]}`},
		"a pointer to a list":  {`{"test":{"enable":true,"filters":[{"name":"a"}]}}`, `{"test":{"enable":true,"filters":null}}`},
		"values":               {`{"values":{"a":1}}`, `{"values":null}`},
		"a key of values":      {`{"values":{"a":1,"b":2}}`, `{"values":{"a":null,"b":2}}`},
		"an integer past 2^53": {`{"install":{"remediation":{"retries":1}}}`, `{"install":{"remediation":{"retries":null}}}`},
		"a list that grows": {`{"postRenderers":[{"kustomize":{"images":[{"name":"a","newTag":"1"}]}}]}`,
			`{"postRenderers":[{"kustomize":{"images":[{"name":"a","newTag":null}]}},{"kustomize":{}}]}`},
		"a list that shrinks": {`{"valuesFrom":[{"kind":"ConfigMap","name":"a"},{"kind":"Secret","name":"b"}]}`,
			`{"valuesFrom":[{"kind":"ConfigMap","name":"a"}]}`},
		"a list emptied": {`{"valuesFrom":[{"kind":"ConfigMap","name":"a"}]}`, `{"valuesFrom":[]}`},
		"two keys of one field": {`{"rollback":{"disableWait":true,"disableWaitForJobs":true}}`,
			`{"Rollback":{"disableWait":null},"rollback":{"disableWaitForJobs":null}}`},
		"two lists of one field": {`{"postRenderers":[{"kustomize":{"images":[{"name":"a","newTag":"1"}]}}]}`,
			`{"postRenderers":[{"kustomize":{"images":[{"name":"a","newTag":null}]}}],"PostRenderers":[{"kustomize":{}}]}`},
		"a promoted field": {`{"healthCheckExprs":[{"apiVersion":"v1","kind":"Pod","current":"true","failed":"false"}]}`,
			`{"healthCheckExprs":[{"apiVersion":"v1","kind":"Pod","current":"true","failed":null}]}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			patch := func(raw string) map[string]interface{} {
				spec, err := Merge(computedSpec(), &apiextensionsv1.JSON{Raw: []byte(raw)})
				if err != nil {
					t.Fatal(err)
				}
				return spec
			}
			existing := &fluxv2.HelmReleaseSpec{}
			if err := decodeHelmReleaseSpec(patch(c.set), existing); err != nil {
				t.Fatal(err)
			}
			if existing.Install != nil && existing.Install.Remediation != nil {
				// Not a float64: a decoding of the spec as plain JSON would round it
				existing.Install.Remediation.Retries = 1<<53 + 1
			}
			merged := patch(c.null)
			before := existing.DeepCopy()
			beforeErr := decodeHelmReleaseSpec(merged, before)
			got := retainedSpec(existing, merged)
			gotErr := decodeHelmReleaseSpec(merged, got)
			if (gotErr == nil) != (beforeErr == nil) || (gotErr == nil && !reflect.DeepEqual(got, before)) {
				gotJSON, _ := json.Marshal(got)
				beforeJSON, _ := json.Marshal(before)
				t.Fatalf("got %s (%v), want the old decoding %s (%v)", gotJSON, gotErr, beforeJSON, beforeErr)
			}
		})
	}
}

func TestANullOfValuesReachesTheHelmReleaseAsBefore(t *testing.T) {
	values := func(t *testing.T, testbed *releaseTestbed) map[string]interface{} {
		t.Helper()
		helmRelease := getDemoMain(t, testbed)
		if helmRelease.Spec.Values == nil {
			return nil
		}
		got := map[string]interface{}{}
		if err := json.Unmarshal(helmRelease.Spec.Values.Raw, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	manifest := fmt.Sprintf(afterRenderPackage, "", "    values: |\n      owner: demo")
	// At creation, and at the next reconciliation, the null stays
	testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, withModuleOverride(t, "", `{"values":{"fromRelease":null}}`))
	for pass := 1; pass <= 2; pass++ {
		if got := testbed.reconcile(t); got.err != nil {
			t.Fatal(got.err)
		}
		if v, ok := values(t, testbed)["fromRelease"]; !ok || v != nil {
			t.Fatalf("pass %d: got the values %v, want fromRelease kept as null", pass, values(t, testbed))
		}
	}
	// Added to an existing HelmRelease, as a new key or in place of a value, the merge patch removes the key
	for name, existingValues := range map[string]string{"a new key": `{"owner":"demo"}`, "a value": `{"owner":"demo","fromRelease":"x"}`} {
		existing := readyHelmRelease()
		existing.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(existingValues)}
		testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, withObjects(t, existing),
			withModuleOverride(t, "", `{"values":{"fromRelease":null}}`))
		if got := testbed.reconcile(t); got.err != nil {
			t.Fatal(got.err)
		}
		if v, ok := values(t, testbed)["fromRelease"]; ok {
			t.Errorf("%s: got fromRelease %v, want the key removed, as before", name, v)
		}
	}
}

func TestSuspendSetOnTheHelmReleaseStaysUnlessTheSpecSetsIt(t *testing.T) {
	for name, c := range map[string]struct {
		specPatch string
		want      bool
	}{"no suspend in the spec": {"", true}, "suspend false in the spec": {`{"suspend":false}`, false}} {
		t.Run(name, func(t *testing.T) {
			existing := readyHelmRelease()
			existing.Spec.Suspend = true
			testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext, withObjects(t, existing),
				withModuleOverride(t, "", c.specPatch))
			if got := testbed.reconcile(t); got.err != nil {
				t.Fatal(got.err)
			}
			if got := getDemoMain(t, testbed).Spec.Suspend; got != c.want {
				t.Fatalf("got suspend %t, want %t", got, c.want)
			}
		})
	}
}
