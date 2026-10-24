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
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/cache"
	"kubocd/internal/configstore"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// recordingCache records the keys of the packages put in the cache.
type recordingCache struct {
	cache.Cache
	sets []string
}

func (c *recordingCache) Set(key string, value cache.Entry) {
	c.sets = append(c.sets, key)
	c.Cache.Set(key, value)
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
