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

package configstore_test

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kubocd/api/v1alpha1"
	"kubocd/internal/configstore"

	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultNamespace = "kubocd"
	packageUrl       = "quay.io/kubocd/packages/demo:0.1.0"
	keptPackageUrl   = "ghcr.io/kubocd/packages/demo:0.1.0"
	changed          = "changed"
)

type reader struct {
	name string
	read func(c configstore.Configuration) interface{}
}

// readers call every method of Configuration, once each: two calls to a
// ConfigStore can see two configurations.
var readers = []reader{
	{"IsClusterRole", func(s configstore.Configuration) interface{} { return s.IsClusterRole("role-a") }},
	{"GetPackageRedirect", packageRedirect(packageUrl)},
	{"GetPackageRedirect with no NewPrefix", packageRedirect(keptPackageUrl)},
	{"GetImageRedirects", func(s configstore.Configuration) interface{} { return s.GetImageRedirects() }},
	{"GetDefaultContexts", func(s configstore.Configuration) interface{} { return s.GetDefaultContexts() }},
	{"GetDefaultNamespaceContexts", func(s configstore.Configuration) interface{} { return s.GetDefaultNamespaceContexts() }},
	{"ObjectMap", func(s configstore.Configuration) interface{} { return sortedObjectMap(s.ObjectMap()) }},
	{"GetOnFailureStrategy", func(s configstore.Configuration) interface{} { return s.GetOnFailureStrategy("retry") }},
	{"GetDefaultOnFailureStrategy", func(s configstore.Configuration) interface{} { return s.GetDefaultOnFailureStrategy() }},
	{"GetDefaultHelmTimeout", func(s configstore.Configuration) interface{} { return s.GetDefaultHelmTimeout().String() }},
	{"GetDefaultHelmInterval", func(s configstore.Configuration) interface{} { return s.GetDefaultHelmInterval().String() }},
	{"GetSpecPatch", func(s configstore.Configuration) interface{} { return s.GetSpecPatch() }},
	{"GetDefaultPackageInterval", func(s configstore.Configuration) interface{} { return s.GetDefaultPackageInterval().String() }},
}

func packageRedirect(url string) func(c configstore.Configuration) interface{} {
	return func(s configstore.Configuration) interface{} {
		spec, newUrl := s.GetPackageRedirect(url)
		return []interface{}{spec, newUrl}
	}
}

// sortedObjectMap sorts the two lists that ObjectMap builds from maps.
func sortedObjectMap(m map[string]interface{}) map[string]interface{} {
	sort.Strings(m["clusterRoles"].([]string))
	strategies := m["onFailureStrategies"].([]map[string]interface{})
	sort.Slice(strategies, func(i, j int) bool {
		return strategies[i]["name"].(string) < strategies[j]["name"].(string)
	})
	return m
}

func answer(t *testing.T, r reader, c configstore.Configuration) string {
	data, err := json.Marshal(r.read(c))
	if err != nil {
		t.Errorf("%s: %v", r.name, err)
	}
	return string(data)
}

// answers gives the answer of every reader, marshalled, so that it can be
// compared once the store changed.
func answers(t *testing.T, c configstore.Configuration) map[string]string {
	result := make(map[string]string, len(readers))
	for _, r := range readers {
		result[r.name] = answer(t, r, c)
	}
	return result
}

func strategy(name string, value string) *v1alpha1.OnFailureStrategySpec {
	return &v1alpha1.OnFailureStrategySpec{Name: name, Strategy: &apiextensionsv1.JSON{Raw: []byte(value)}}
}

func duration(d time.Duration) *metav1.Duration {
	return &metav1.Duration{Duration: d}
}

// configsA and configsB set every field of the configuration, each to another value.
func configsA() *v1alpha1.ConfigList {
	return &v1alpha1.ConfigList{Items: []v1alpha1.Config{
		{ObjectMeta: metav1.ObjectMeta{Name: "a-routing"}, Spec: v1alpha1.ConfigSpec{
			ClusterRoles: []string{"role-a"},
			PackageRedirects: []*v1alpha1.PackageRedirectSpec{
				{OldPrefix: "quay.io/kubocd/", NewPrefix: "mirror-a.local/kubocd/",
					OciAddOn: v1alpha1.OciAddOn{SecretRef: &fluxmeta.LocalObjectReference{Name: "pull-a"}}},
				{OldPrefix: "ghcr.io/kubocd/", OciAddOn: v1alpha1.OciAddOn{SecretRef: &fluxmeta.LocalObjectReference{Name: "ghcr-a"}}},
			},
			ImageRedirects: []*v1alpha1.ImageRedirectSpec{
				{OldPrefix: "docker.io/", NewPrefix: "mirror-a.local/", ImagePullSecrets: []string{"pull-a"}},
			},
			DefaultContexts:          []v1alpha1.NamespacedName{{Name: "cluster-a"}},
			DefaultNamespaceContexts: []string{"project-a"},
		}},
		{ObjectMeta: metav1.ObjectMeta{Name: "a-tuning"}, Spec: v1alpha1.ConfigSpec{
			OnFailureStrategies: []*v1alpha1.OnFailureStrategySpec{
				strategy("retry", `{"install":{"remediation":{"retries":3}}}`),
				strategy("keep-a", `{"upgrade":{"remediation":{"retries":1}}}`),
			},
			DefaultOnFailureStrategy: "retry",
			DefaultHelmTimeout:       duration(3 * time.Minute),
			DefaultHelmInterval:      duration(10 * time.Minute),
			DefaultPackageInterval:   duration(15 * time.Minute),
			SpecPatch:                &apiextensionsv1.JSON{Raw: []byte(`{"timeout":"3m"}`)},
		}},
	}}
}

func configsB() *v1alpha1.ConfigList {
	return &v1alpha1.ConfigList{Items: []v1alpha1.Config{
		{ObjectMeta: metav1.ObjectMeta{Name: "b-routing"}, Spec: v1alpha1.ConfigSpec{
			ClusterRoles: []string{"role-b"},
			PackageRedirects: []*v1alpha1.PackageRedirectSpec{
				{OldPrefix: "quay.io/kubocd/", NewPrefix: "mirror-b.local/kubocd/",
					OciAddOn: v1alpha1.OciAddOn{SecretRef: &fluxmeta.LocalObjectReference{Name: "pull-b"}}},
				{OldPrefix: "ghcr.io/kubocd/", OciAddOn: v1alpha1.OciAddOn{SecretRef: &fluxmeta.LocalObjectReference{Name: "ghcr-b"}}},
			},
			ImageRedirects: []*v1alpha1.ImageRedirectSpec{
				{OldPrefix: "docker.io/", NewPrefix: "mirror-b.local/", ImagePullSecrets: []string{"pull-b"}},
			},
			DefaultContexts:          []v1alpha1.NamespacedName{{Namespace: "platform", Name: "cluster-b"}},
			DefaultNamespaceContexts: []string{"project-b"},
		}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b-tuning"}, Spec: v1alpha1.ConfigSpec{
			OnFailureStrategies: []*v1alpha1.OnFailureStrategySpec{
				strategy("retry", `{"install":{"remediation":{"retries":5}}}`),
				strategy("keep-b", `{"upgrade":{"remediation":{"retries":2}}}`),
			},
			DefaultOnFailureStrategy: "keep-b",
			DefaultHelmTimeout:       duration(4 * time.Minute),
			DefaultHelmInterval:      duration(20 * time.Minute),
			DefaultPackageInterval:   duration(25 * time.Minute),
			SpecPatch:                &apiextensionsv1.JSON{Raw: []byte(`{"timeout":"4m"}`)},
		}},
	}}
}

func loadedStore(t *testing.T, configs *v1alpha1.ConfigList) configstore.ConfigStore {
	t.Helper()
	store := configstore.New()
	if err := store.AddConfigs(configs, defaultNamespace); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestEveryReaderGetsAWholeConfigurationWhileAddConfigsRuns(t *testing.T) {
	lists := []*v1alpha1.ConfigList{configsA(), configsB()}
	wants := []map[string]string{answers(t, loadedStore(t, lists[0])), answers(t, loadedStore(t, lists[1]))}
	for _, r := range readers {
		if wants[0][r.name] == wants[1][r.name] {
			t.Fatalf("%s gives the same answer for both configurations: %s", r.name, wants[0][r.name])
		}
	}
	store := loadedStore(t, lists[0])
	var done atomic.Bool
	var rounds atomic.Int64
	var started, finished sync.WaitGroup
	for range 4 {
		started.Add(1)
		finished.Add(1)
		go func() {
			defer finished.Done()
			started.Done()
			for !done.Load() {
				for _, r := range readers {
					if got := answer(t, r, store); got != wants[0][r.name] && got != wants[1][r.name] {
						t.Errorf("%s gives %s, the answer of neither configuration", r.name, got)
						return
					}
				}
				rounds.Add(1)
			}
		}()
	}
	started.Wait()
	roundsBefore := rounds.Load()
	// With one CPU, the readers may not run at all during the first 1000 calls.
	for i := 0; i < 1000 || rounds.Load() == roundsBefore; i++ {
		if err := store.AddConfigs(lists[i%2], defaultNamespace); err != nil {
			t.Error(err)
		}
		if t.Failed() {
			break
		}
	}
	done.Store(true)
	finished.Wait()
}

func TestAFailedAddConfigsKeepsThePreviousConfiguration(t *testing.T) {
	notAMap := configsB()
	notAMap.Items = append(notAMap.Items, v1alpha1.Config{ObjectMeta: metav1.ObjectMeta{Name: "z-broken"},
		Spec: v1alpha1.ConfigSpec{OnFailureStrategies: []*v1alpha1.OnFailureStrategySpec{strategy("broken", `[1, 2]`)}}})
	twoDefaults := configsB()
	twoDefaults.Items = append(twoDefaults.Items, v1alpha1.Config{ObjectMeta: metav1.ObjectMeta{Name: "z-default"},
		Spec: v1alpha1.ConfigSpec{DefaultOnFailureStrategy: "retry"}})
	undefinedDefault := configsB()
	undefinedDefault.Items[1].Spec.DefaultOnFailureStrategy = "missing"
	for _, test := range []struct {
		name    string
		configs *v1alpha1.ConfigList
		wantErr string
	}{
		{"a strategy that is not a map", notAMap, "'broken' failed to unmarshal"},
		{"two different default strategies", twoDefaults, "DefaultOnFailureStrategy is defined multiple times"},
		{"an undefined default strategy", undefinedDefault, "'missing' is not defined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := loadedStore(t, configsA())
			want := answers(t, store)
			err := store.AddConfigs(test.configs, defaultNamespace)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("AddConfigs returned %v, want an error with %q", err, test.wantErr)
			}
			got := answers(t, store)
			for _, r := range readers {
				if got[r.name] != want[r.name] {
					t.Errorf("after the failure, %s gives %s, want %s", r.name, got[r.name], want[r.name])
				}
			}
		})
	}
}

func TestChangingAnAnswerLeavesTheStoreAndItsConfigurationUnchanged(t *testing.T) {
	for _, test := range []struct {
		reader string
		change func(c configstore.Configuration)
	}{
		{"GetPackageRedirect", func(s configstore.Configuration) {
			redirect, _ := s.GetPackageRedirect(packageUrl)
			redirect.NewPrefix = "changed/"
			redirect.SecretRef.Name = changed
		}},
		{"GetPackageRedirect with no NewPrefix", func(s configstore.Configuration) {
			redirect, _ := s.GetPackageRedirect(keptPackageUrl)
			redirect.SecretRef.Name = changed
		}},
		{"GetImageRedirects", func(s configstore.Configuration) {
			redirects := s.GetImageRedirects()
			redirects[0].NewPrefix = "changed/"
			redirects[0].ImagePullSecrets[0] = changed
		}},
		{"GetDefaultContexts", func(s configstore.Configuration) { s.GetDefaultContexts()[0].Name = changed }},
		{"GetDefaultNamespaceContexts", func(s configstore.Configuration) { s.GetDefaultNamespaceContexts()[0] = changed }},
		{"GetOnFailureStrategy", func(s configstore.Configuration) {
			s.GetOnFailureStrategy("retry")["install"].(map[string]interface{})["remediation"] = changed
		}},
		{"GetDefaultOnFailureStrategy", func(s configstore.Configuration) {
			s.GetDefaultOnFailureStrategy()["install"].(map[string]interface{})["remediation"] = changed
		}},
		{"GetSpecPatch", func(s configstore.Configuration) { s.GetSpecPatch().Raw[2] = 'X' }},
		{"ObjectMap", func(s configstore.Configuration) {
			m := s.ObjectMap()
			m["packageRedirects"].([]*v1alpha1.PackageRedirectSpec)[0].SecretRef.Name = changed
			m["imageRedirects"].([]*v1alpha1.ImageRedirectSpec)[0].ImagePullSecrets[0] = changed
			m["defaultContexts"].([]v1alpha1.NamespacedName)[0].Name = changed
			m["defaultNamespaceContexts"].([]string)[0] = changed
			m["specPatch"].(*apiextensionsv1.JSON).Raw[2] = 'X'
			for _, item := range m["onFailureStrategies"].([]map[string]interface{}) {
				item["strategy"].(map[string]interface{})["install"] = changed
			}
		}},
	} {
		t.Run(test.reader, func(t *testing.T) {
			store := loadedStore(t, configsA())
			want := answers(t, store)
			test.change(store)
			test.change(store.Current())
			for name, c := range map[string]configstore.Configuration{"store": store, "configuration": store.Current()} {
				got := answers(t, c)
				for _, r := range readers {
					if got[r.name] != want[r.name] {
						t.Errorf("%s: %s gives %s, want %s", name, r.name, got[r.name], want[r.name])
					}
				}
			}
		})
	}
}

func TestTheLastConfigThatSetsASpecPatchGivesIt(t *testing.T) {
	withPatch := func(name string, raw string) v1alpha1.Config {
		config := v1alpha1.Config{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if raw != "" {
			config.Spec.SpecPatch = &apiextensionsv1.JSON{Raw: []byte(raw)}
		}
		return config
	}
	for _, test := range []struct {
		name    string
		configs []v1alpha1.Config
		want    string
	}{
		{"a later Config without specPatch", []v1alpha1.Config{withPatch("b", ""), withPatch("a", `{"timeout":"3m"}`)},
			`{"timeout":"3m"}`},
		{"a later Config with a specPatch", []v1alpha1.Config{withPatch("b", `{"timeout":"4m"}`), withPatch("a", `{"timeout":"3m"}`)},
			`{"timeout":"4m"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := loadedStore(t, &v1alpha1.ConfigList{Items: test.configs})
			if got := store.GetSpecPatch(); got == nil || string(got.Raw) != test.want {
				t.Fatalf("got the specPatch %v, want %s", got, test.want)
			}
		})
	}
}

func TestTheVersionGrowsWithEachPublishedConfiguration(t *testing.T) {
	store := configstore.New()
	for i, configs := range []*v1alpha1.ConfigList{configsA(), configsB()} {
		if err := store.AddConfigs(configs, defaultNamespace); err != nil {
			t.Fatal(err)
		}
		if got := store.Version(); got != uint64(i+1) {
			t.Fatalf("after %d configurations, got the version %d", i+1, got)
		}
	}
	broken := configsB()
	broken.Items[1].Spec.DefaultOnFailureStrategy = "missing"
	if err := store.AddConfigs(broken, defaultNamespace); err == nil {
		t.Fatal("AddConfigs accepted an undefined default strategy")
	}
	if got := store.Version(); got != 2 {
		t.Fatalf("after a failed AddConfigs, got the version %d, want 2", got)
	}
}

func TestAConfigurationTakenBeforeAPublicationKeepsItsAnswers(t *testing.T) {
	store := loadedStore(t, configsA())
	want := answers(t, store)
	configuration := store.Current()
	if err := store.AddConfigs(configsB(), defaultNamespace); err != nil {
		t.Fatal(err)
	}
	if configuration.Version() != 1 || store.Version() != 2 {
		t.Fatalf("got the versions %d for the configuration and %d for the store, want 1 and 2", configuration.Version(),
			store.Version())
	}
	got := answers(t, configuration)
	published := answers(t, store)
	for _, r := range readers {
		if got[r.name] != want[r.name] {
			t.Errorf("%s gives %s once B is published, want %s", r.name, got[r.name], want[r.name])
		}
		if published[r.name] == want[r.name] {
			t.Errorf("%s of the store gives the answer of A once B is published: %s", r.name, published[r.name])
		}
	}
}
