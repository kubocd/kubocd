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
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/kubopackage"
	"strings"
	"testing"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// oneModulePackage has one module, without specPatch.
const oneModulePackage = `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
    values: |
      owner: demo
`

// configsOf is a list of one Config with the spec given.
func configsOf(spec kv1alpha1.ConfigSpec) *kv1alpha1.ConfigList {
	return &kv1alpha1.ConfigList{Items: []kv1alpha1.Config{{ObjectMeta: metav1.ObjectMeta{Name: "conf", Namespace: "kubocd"}, Spec: spec}}}
}

func storeOf(t *testing.T, spec kv1alpha1.ConfigSpec) configstore.ConfigStore {
	t.Helper()
	store := configstore.New()
	if err := store.AddConfigs(configsOf(spec), "kubocd"); err != nil {
		t.Fatal(err)
	}
	return store
}

// publishingAtFirstRead publishes next on the store it wraps once the
// configuration in effect has been read, by Current or by a first getter, as
// the Config controller can do while a HelmRelease is built.
type publishingAtFirstRead struct {
	configstore.ConfigStore
	t         *testing.T
	next      kv1alpha1.ConfigSpec
	published bool
}

func (s *publishingAtFirstRead) publish() {
	if s.published {
		return
	}
	s.published = true
	if err := s.AddConfigs(configsOf(s.next), "kubocd"); err != nil {
		s.t.Fatal(err)
	}
}

func (s *publishingAtFirstRead) Current() configstore.Configuration {
	configuration := s.ConfigStore.Current()
	s.publish()
	return configuration
}

func (s *publishingAtFirstRead) GetDefaultOnFailureStrategy() map[string]interface{} {
	strategy := s.ConfigStore.GetDefaultOnFailureStrategy()
	s.publish()
	return strategy
}

func (s *publishingAtFirstRead) GetSpecPatch() *apiextensionsv1.JSON {
	patch := s.ConfigStore.GetSpecPatch()
	s.publish()
	return patch
}

// populatedMain builds the HelmRelease of the module main of the Release demo
// of oneModulePackage with store.
func populatedMain(t *testing.T, store configstore.ConfigStore) (*fluxv2.HelmRelease, error) {
	t.Helper()
	pck := newTestPackage(t, oneModulePackage)
	pck.Status = &kubopackage.Status{ChartByModule: map[string]kubopackage.ChartRef{"main": {Name: "demo", Version: "1.0.0"}}}
	release := newTestRelease("demo", "")
	rendered, _, err := renderRelease(newTestHelper(t), configstore.New(), release, pck)
	if err != nil {
		t.Fatal(err)
	}
	helmRelease := &fluxv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: BuildHelmReleaseName("demo", "main"), Namespace: "apps"}}
	err = PopulateHelmRelease(helmRelease, release, pck, rendered, "kcd-demo", pck.Package.Modules[0],
		map[string]string{"main": "demo-main"}, store)
	return helmRelease, err
}

const mergedSpecHasAnUndefinedField = "the spec merged from the package, the configuration and the Release has a field that " +
	"HelmRelease does not define"

func TestTheSpecOfAHelmReleaseIsCheckedAgainstTheConfigurationThatBuiltIt(t *testing.T) {
	undefined := kv1alpha1.ConfigSpec{SpecPatch: &apiextensionsv1.JSON{Raw: []byte(`{"notAHelmReleaseField":1}`)}}
	if _, err := populatedMain(t, storeOf(t, undefined)); err == nil || !strings.Contains(err.Error(), mergedSpecHasAnUndefinedField) {
		t.Fatalf("got the error %v, want the merged spec at fault", err)
	}
	switching := &publishingAtFirstRead{ConfigStore: storeOf(t, undefined), t: t}
	_, err := populatedMain(t, switching)
	if !switching.published || err == nil || !strings.Contains(err.Error(), mergedSpecHasAnUndefinedField) {
		t.Fatalf("got the error %v with a configuration without specPatch published during the build (published: %v), "+
			"want the merged spec at fault, as with the specPatch alone", err, switching.published)
	}
}

func TestTheSpecOfAHelmReleaseTakesItsStrategyAndItsPatchFromOneConfiguration(t *testing.T) {
	configuration := func(retries string, timeout string) kv1alpha1.ConfigSpec {
		return kv1alpha1.ConfigSpec{
			OnFailureStrategies: []*kv1alpha1.OnFailureStrategySpec{{Name: "retry",
				Strategy: &apiextensionsv1.JSON{Raw: []byte(`{"install":{"remediation":{"retries":` + retries + `}}}`)}}},
			DefaultOnFailureStrategy: "retry",
			SpecPatch:                &apiextensionsv1.JSON{Raw: []byte(`{"timeout":"` + timeout + `"}`)},
		}
	}
	switching := &publishingAtFirstRead{ConfigStore: storeOf(t, configuration("3", "3m")), t: t, next: configuration("5", "4m")}
	helmRelease, err := populatedMain(t, switching)
	if err != nil {
		t.Fatal(err)
	}
	retries := 0
	if install := helmRelease.Spec.Install; install != nil && install.Remediation != nil {
		retries = install.Remediation.Retries
	}
	timeout := ""
	if helmRelease.Spec.Timeout != nil {
		timeout = helmRelease.Spec.Timeout.Duration.String()
	}
	if !switching.published || retries != 3 || timeout != "3m0s" {
		t.Fatalf("got %d retries and the timeout %q with another configuration published during the build (published: %v), "+
			"want 3 and 3m0s, both from the configuration in effect when the build started", retries, timeout, switching.published)
	}
}
