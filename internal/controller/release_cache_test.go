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
	"kubocd/internal/cache"
	"kubocd/internal/configstore"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// recordingCache records the keys and the weights of the packages put in the
// cache.
type recordingCache struct {
	cache.Cache
	sets    []string
	weights []int64
}

func (c *recordingCache) Set(key string, value cache.Entry, weight int64) {
	c.sets = append(c.sets, key)
	c.weights = append(c.weights, weight)
	c.Cache.Set(key, value, weight)
}

// movingVersion stands for Configs that change at each read of the version.
type movingVersion struct {
	configstore.ConfigStore
	version atomic.Uint64
}

func (s *movingVersion) Version() uint64 {
	return s.version.Add(1)
}

func TestAChangeOfTheConfigsReachesAPackageInCache(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext)
	cached := &recordingCache{Cache: testbed.reconciler.PackageCache}
	testbed.reconciler.PackageCache = cached
	for _, want := range []struct {
		configs           []kv1alpha1.Config
		timeout, interval time.Duration
	}{
		{nil, 2 * time.Minute, 30 * time.Minute},
		{[]kv1alpha1.Config{{ObjectMeta: metav1.ObjectMeta{Name: "config"}, Spec: kv1alpha1.ConfigSpec{
			DefaultHelmTimeout: &metav1.Duration{Duration: 7 * time.Minute}, DefaultHelmInterval: &metav1.Duration{Duration: 9 * time.Minute}}}},
			7 * time.Minute, 9 * time.Minute},
	} {
		if want.configs != nil {
			if err := testbed.reconciler.ConfigStore.AddConfigs(&kv1alpha1.ConfigList{Items: want.configs}, "kubocd"); err != nil {
				t.Fatal(err)
			}
		}
		if got := testbed.reconcile(t); got.err != nil {
			t.Fatal(got.err)
		}
		spec := getDemoMain(t, testbed).Spec
		if spec.Timeout == nil || spec.Timeout.Duration != want.timeout || spec.Interval.Duration != want.interval {
			t.Fatalf("got the timeout %v and the interval %v of the HelmRelease, want %v and %v", spec.Timeout, spec.Interval,
				want.timeout, want.interval)
		}
	}
	if len(cached.sets) != 2 {
		t.Fatalf("got the packages %q put in the cache, want one for each configuration", cached.sets)
	}
}

func TestAPackageLoadedWhileTheConfigsChangeIsNotCached(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext)
	cached := &recordingCache{Cache: testbed.reconciler.PackageCache}
	testbed.reconciler.PackageCache = cached
	testbed.reconciler.ConfigStore = &movingVersion{ConfigStore: testbed.reconciler.ConfigStore}
	if got := testbed.reconcile(t); got.err != nil {
		t.Fatal(got.err)
	}
	if len(cached.sets) != 0 {
		t.Fatalf("got the packages %q put in the cache, want none", cached.sets)
	}
}

func TestThePackageInCacheWeighsItsManifests(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", "")
	testbed := newReleaseTestbed(t, manifest, "", afterRenderContext)
	cached := &recordingCache{Cache: testbed.reconciler.PackageCache}
	testbed.reconciler.PackageCache = cached
	if got := testbed.reconcile(t); got.err != nil {
		t.Fatal(got.err)
	}
	status, err := os.ReadFile(filepath.Join(testbed.reconciler.ServerRoot, "hr", "apps", "demo", "status.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(len(manifest) + len(status)); len(cached.weights) != 1 || cached.weights[0] != want {
		t.Fatalf("got the weights %v, want one of %d bytes", cached.weights, want)
	}
}

// withRevision gives the package of the Release demo the revision given, in
// its OCIRepository, its HelmRepository and its folder.
func withRevision(t *testing.T, revision string) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		ctx := context.Background()
		key := types.NamespacedName{Namespace: "apps", Name: fmt.Sprintf(OciRepositoryNameFormat, "demo")}
		source := &sourcev1.OCIRepository{}
		if err := r.Get(ctx, key, source); err != nil {
			t.Fatal(err)
		}
		source.Status.Artifact.Revision = revision
		if err := r.Update(ctx, source); err != nil {
			t.Fatal(err)
		}
		helmRepository := &sourcev1.HelmRepository{}
		if err := r.Get(ctx, key, helmRepository); err != nil {
			t.Fatal(err)
		}
		helmRepository.SetAnnotations(map[string]string{packageRevisionAnnotation: revision})
		if err := r.Update(ctx, helmRepository); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.ServerRoot, "hr", "apps", "demo", "revision.txt"), []byte(revision), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTwoReleasesTakeTurnsInACacheOfOnePackage(t *testing.T) {
	shared := &recordingCache{Cache: cache.NewCache(time.Minute, 1, 1<<20, logr.Discard())}
	first := newReleaseTestbed(t, fmt.Sprintf(reconciledPackage, "first: 1"), "", "")
	second := newReleaseTestbed(t, fmt.Sprintf(reconciledPackage, "second: 2"), "", "", withRevision(t, olderRevision))
	first.reconciler.PackageCache, second.reconciler.PackageCache = shared, shared
	for pass := 1; pass <= 2; pass++ {
		for _, c := range []struct {
			testbed *releaseTestbed
			want    string
		}{{first, `{"first":1}`}, {second, `{"second":2}`}} {
			if got := c.testbed.reconcile(t); got.err != nil {
				t.Fatal(got.err)
			}
			if values := string(getDemoMain(t, c.testbed).Spec.Values.Raw); values != c.want {
				t.Fatalf("pass %d: got the values %s, want %s", pass, values, c.want)
			}
		}
	}
	if len(shared.sets) != 4 {
		t.Fatalf("got the packages %q put in the cache, want each loaded again at each pass", shared.sets)
	}
}
