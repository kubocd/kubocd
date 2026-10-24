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
package cmd

import (
	"context"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// releaseCache is the cache of the Releases of an API server, with the
// production indexes, and the map function of the Contexts that reads it.
type releaseCache struct {
	server  *apiServer
	cache   cache.Cache
	store   configstore.ConfigStore
	log     *logLines
	mapping handler.MapFunc
}

func startReleaseCache(t *testing.T) *releaseCache {
	t.Helper()
	server := startAPIServer(t, []string{"kubocd", "team", "other", "platform"})
	informers, err := cache.New(server.cfg, cache.Options{Scheme: server.scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := indexReleases(informers); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if err := informers.Start(ctx); err != nil {
			t.Errorf("cache: %v", err)
		}
	}()
	// Cleanups run in reverse order: this one stops the cache before the API server stops
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	if !informers.WaitForCacheSync(ctx) {
		t.Fatal("the cache does not sync")
	}
	store := configstore.New()
	log := &logLines{}
	return &releaseCache{server: server, cache: informers, store: store, log: log,
		mapping: findReleaseFromContext(informers, store, funcr.New(log.write, funcr.Options{}))}
}

// publish publishes a configuration of one Config with the spec given.
func (c *releaseCache) publish(t *testing.T, spec kubocdv1alpha1.ConfigSpec) {
	t.Helper()
	list := &kubocdv1alpha1.ConfigList{Items: []kubocdv1alpha1.Config{{
		ObjectMeta: metav1.ObjectMeta{Name: "conf", Namespace: "kubocd"}, Spec: spec}}}
	if err := c.store.AddConfigs(list, "kubocd"); err != nil {
		t.Fatal(err)
	}
}

// releaseOf is the Release namespace/name with the Contexts given.
func releaseOf(namespace string, name string, contexts ...kubocdv1alpha1.NamespacedName) *kubocdv1alpha1.Release {
	return &kubocdv1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: kubocdv1alpha1.ReleaseSpec{Contexts: contexts,
			Package: kubocdv1alpha1.PackageSourceSpec{Repository: "registry.example/demo", Tag: "0.1.0"}}}
}

// create creates release and waits until the cache, which indexes it then,
// shows it.
func (c *releaseCache) create(t *testing.T, release *kubocdv1alpha1.Release) {
	t.Helper()
	if err := c.server.client.Create(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: release.Namespace, Name: release.Name}
	eventually(t, "the Release "+key.String()+" in the cache", 10*time.Second, func() bool {
		return c.cache.Get(context.Background(), key, &kubocdv1alpha1.Release{}) == nil
	})
}

// remove deletes the Release namespace/name and waits until the cache no
// longer shows it.
func (c *releaseCache) remove(t *testing.T, namespace string, name string) {
	t.Helper()
	release := &kubocdv1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	if err := c.server.client.Delete(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: namespace, Name: name}
	eventually(t, "the Release "+key.String()+" gone from the cache", 10*time.Second, func() bool {
		return apierrors.IsNotFound(c.cache.Get(context.Background(), key, &kubocdv1alpha1.Release{}))
	})
}

// requested returns the Releases that a change of the Context namespace/name
// requests, sorted.
func (c *releaseCache) requested(namespace string, name string) []string {
	kcontext := &kubocdv1alpha1.Context{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	requests := c.mapping(context.Background(), kcontext)
	got := make([]string, 0, len(requests))
	for _, request := range requests {
		got = append(got, request.String())
	}
	slices.Sort(got)
	return got
}

func TestAReleaseIndexedBeforeAConfigurationIsFoundForItsNewDefaultContext(t *testing.T) {
	c := startReleaseCache(t)
	c.publish(t, kubocdv1alpha1.ConfigSpec{})
	c.create(t, releaseOf("team", "before"))
	c.publish(t, kubocdv1alpha1.ConfigSpec{DefaultNamespaceContexts: []string{"env"}})
	if got := c.requested("team", "env"); !slices.Equal(got, []string{"team/before"}) {
		t.Fatalf("a change of the default namespace Context team/env requests %v, want team/before", got)
	}
	if got := c.requested("other", "env"); len(got) != 0 {
		t.Fatalf("a change of other/env requests %v, want none", got)
	}
}

func TestADeletedReleaseIndexedUnderAnEarlierConfigurationLeavesTheOthersFound(t *testing.T) {
	c := startReleaseCache(t)
	c.publish(t, kubocdv1alpha1.ConfigSpec{DefaultNamespaceContexts: []string{"env"}})
	c.create(t, releaseOf("team", "gone"))
	c.create(t, releaseOf("team", "explicit", kubocdv1alpha1.NamespacedName{Name: "env"}))
	c.publish(t, kubocdv1alpha1.ConfigSpec{})
	c.remove(t, "team", "gone")
	if got := c.requested("team", "env"); !slices.Equal(got, []string{"team/explicit"}) || len(c.log.lines) != 0 {
		t.Fatalf("a change of team/env requests %v and logs %q, want team/explicit and no failed list", got, c.log.lines)
	}
}

func TestAGlobalDefaultContextRequestsEveryRelease(t *testing.T) {
	c := startReleaseCache(t)
	shared := kubocdv1alpha1.NamespacedName{Namespace: "platform", Name: "shared"}
	c.publish(t, kubocdv1alpha1.ConfigSpec{DefaultContexts: []kubocdv1alpha1.NamespacedName{shared},
		DefaultNamespaceContexts: []string{"env"}})
	c.create(t, releaseOf("team", "web"))
	c.create(t, releaseOf("other", "db", shared))
	skipping := releaseOf("other", "skipping")
	skipping.Spec.SkipDefaultContext = true
	c.create(t, skipping)
	want := []string{"other/db", "other/skipping", "team/web"}
	if got := c.requested("platform", "shared"); !slices.Equal(got, want) {
		t.Fatalf("a change of the default Context platform/shared requests %v, want %v", got, want)
	}
	if got := c.requested("other", "env"); !slices.Equal(got, []string{"other/db", "other/skipping"}) {
		t.Fatalf("a change of the default namespace Context other/env requests %v, want the Releases of other", got)
	}
	if got := c.requested("team", "unrelated"); len(got) != 0 || len(c.log.lines) != 0 {
		t.Fatalf("a change of team/unrelated requests %v and logs %q, want neither", got, c.log.lines)
	}
}
