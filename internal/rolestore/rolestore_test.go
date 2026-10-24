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
package rolestore_test

import (
	"testing"

	"kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/rolestore"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var (
	requester    = types.NamespacedName{Namespace: "apps", Name: "demo"}
	dependencies = []string{"ingress", "storage"}
)

// clusterRoles is a list of one Config that declares the cluster roles given.
func clusterRoles(roles ...string) *v1alpha1.ConfigList {
	return &v1alpha1.ConfigList{Items: []v1alpha1.Config{{
		ObjectMeta: metav1.ObjectMeta{Name: "conf", Namespace: "kubocd"},
		Spec:       v1alpha1.ConfigSpec{ClusterRoles: roles},
	}}}
}

func loadedStore(t *testing.T, list *v1alpha1.ConfigList) configstore.ConfigStore {
	t.Helper()
	store := configstore.New()
	if err := store.AddConfigs(list, "kubocd"); err != nil {
		t.Fatal(err)
	}
	return store
}

// publishingOnce publishes next on the store it wraps once the configuration
// in effect has been read, by Current or by a first IsClusterRole, as the
// Config controller can do during a check.
type publishingOnce struct {
	configstore.ConfigStore
	t         *testing.T
	next      *v1alpha1.ConfigList
	published bool
}

func (s *publishingOnce) publish() {
	if s.published {
		return
	}
	s.published = true
	if err := s.AddConfigs(s.next, "kubocd"); err != nil {
		s.t.Fatal(err)
	}
}

func (s *publishingOnce) Current() configstore.Configuration {
	configuration := s.ConfigStore.Current()
	s.publish()
	return configuration
}

func (s *publishingOnce) IsClusterRole(role string) bool {
	answer := s.ConfigStore.IsClusterRole(role)
	s.publish()
	return answer
}

func TestADependencyCheckReadsOneConfiguration(t *testing.T) {
	a, b := clusterRoles("ingress"), clusterRoles("storage")
	if got := rolestore.New(loadedStore(t, a), logr.Discard()).MissingDependency(requester, dependencies); got != "storage" {
		t.Fatalf("got %q missing with the cluster role ingress, want storage", got)
	}
	if got := rolestore.New(loadedStore(t, b), logr.Discard()).MissingDependency(requester, dependencies); got != "ingress" {
		t.Fatalf("got %q missing with the cluster role storage, want ingress", got)
	}
	switching := &publishingOnce{ConfigStore: loadedStore(t, a), t: t, next: b}
	got := rolestore.New(switching, logr.Discard()).MissingDependency(requester, dependencies)
	if !switching.published || got != "storage" {
		t.Fatalf("got %q missing with the cluster role storage published during the check (published: %v), want storage, as "+
			"with the cluster role ingress alone", got, switching.published)
	}
}

func TestDependencyChecksDuringPublicationsReadOneConfigurationEach(t *testing.T) {
	a, b := clusterRoles("ingress"), clusterRoles("storage")
	store := loadedStore(t, a)
	roles := rolestore.New(store, logr.Discard())
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			next := a
			if i%2 == 1 {
				next = b
			}
			if err := store.AddConfigs(next, "kubocd"); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for range 2000 {
		if missing := roles.MissingDependency(requester, dependencies); missing != "storage" && missing != "ingress" {
			t.Errorf("got %q missing, the answer of neither configuration", missing)
			break
		}
	}
	close(stop)
	<-done
}
