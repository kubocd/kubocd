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
	"errors"
	"fmt"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/controller"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// watchCanary is the name of the object of an event, which the selector of a
// map function holds, and the error of the cache can quote.
const watchCanary = renderCanary + "-name"

// mapFunction is a map function of a watch of the controller, the object
// whose event it maps, the message it logs and the selector it lists with.
type mapFunction struct {
	build    func(reader client.Reader, logger logr.Logger) handler.MapFunc
	object   client.Object
	msg      string
	selector string
}

func mapFunctions() map[string]mapFunction {
	named := metav1.ObjectMeta{Namespace: "apps", Name: watchCanary}
	clusterNamed := metav1.ObjectMeta{Name: watchCanary}
	return map[string]mapFunction{
		"findReplicationFromResource": {func(r client.Reader, l logr.Logger) handler.MapFunc {
			return findReplicationFromResource(r, l, controller.KindSecret)
		}, &corev1.Secret{ObjectMeta: named}, "findReplicationFromResource(): Unable to find replications",
			controller.ResourceIndexOnReplication + "=" + controller.ResourceKey("apps", controller.KindSecret, watchCanary)},
		"findConnectionFromContract": {findConnectionFromContract, &kubocdv1alpha1.Contract{ObjectMeta: named},
			"findConnectionFromContract(): Unable to find contract bindings",
			controller.ContractIndexOnConnection + "=" + watchCanary},
		"findClusterConnectionFromClusterContract": {findClusterConnectionFromClusterContract,
			&kubocdv1alpha1.ClusterContract{ObjectMeta: clusterNamed},
			"findClusterConnectionFromClusterContract(): Unable to find contract bindings",
			controller.ContractIndexOnClusterConnection + "=" + watchCanary},
		"findReleaseFromOutputClusterConnection": {findReleaseFromOutputClusterConnection,
			&kubocdv1alpha1.ClusterConnection{ObjectMeta: clusterNamed},
			"findReleaseFromOutputClusterConnection(): Unable to find ClusterConnection bindings",
			outputClusterConnectionIndexOnRelease + "=" + watchCanary},
		"findReleaseFromWatchedInputConnection": {findReleaseFromWatchedInputConnection,
			&kubocdv1alpha1.Connection{ObjectMeta: named},
			"findReleaseFromWatchedInputConnection(): Unable to find Connection bindings",
			watchedInputConnectionIndexOnRelease + "=apps:" + watchCanary},
		"findReleaseFromWatchedInputClusterConnection": {findReleaseFromWatchedInputClusterConnection,
			&kubocdv1alpha1.ClusterConnection{ObjectMeta: clusterNamed},
			"findReleaseFromWatchedInputClusterConnection(): Unable to find ClusterConnection bindings",
			watchedInputClusterConnectionIndexOnRelease + "=" + watchCanary},
		"findReleaseFromContext": {func(r client.Reader, l logr.Logger) handler.MapFunc {
			return findReleaseFromContext(r, configstore.New(), l)
		}, &kubocdv1alpha1.Context{ObjectMeta: named},
			"findReleaseFromContext(): Unable to find Context bindings", contextIndexOnRelease + "=apps:" + watchCanary},
		"findReleaseFromConfig": {findReleaseFromConfig, &kubocdv1alpha1.Config{ObjectMeta: named},
			"findReleaseFromConfig(): Unable to find releases", ""},
		"findChildFromParent": {findChildFromParent, &kubocdv1alpha1.Context{ObjectMeta: named},
			"findChildFromParent(): Unable to find Context bindings", parentIndexOnChild + "=apps:" + watchCanary},
	}
}

// listing is a client whose lists answer list, with the selector they ask
// for.
func listing(t *testing.T, list func(list client.ObjectList, selector string) error) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubocdv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return interceptor.NewClient(fake.NewClientBuilder().WithScheme(scheme).Build(), interceptor.Funcs{
		List: func(_ context.Context, _ client.WithWatch, objects client.ObjectList, opts ...client.ListOption) error {
			options := client.ListOptions{}
			options.ApplyOptions(opts)
			selector := ""
			if options.FieldSelector != nil {
				selector = options.FieldSelector.String()
			}
			return list(objects, selector)
		},
	})
}

// twoObjects fills list with the objects ns-a/a and ns-b/b.
func twoObjects(list client.ObjectList) error {
	items := make([]runtime.Object, 0, 2)
	for _, name := range []string{"a", "b"} {
		var item client.Object
		switch list.(type) {
		case *kubocdv1alpha1.ReplicationList:
			item = &kubocdv1alpha1.Replication{}
		case *kubocdv1alpha1.ConnectionList:
			item = &kubocdv1alpha1.Connection{}
		case *kubocdv1alpha1.ClusterConnectionList:
			item = &kubocdv1alpha1.ClusterConnection{}
		case *kubocdv1alpha1.ReleaseList:
			item = &kubocdv1alpha1.Release{}
		case *kubocdv1alpha1.ContextList:
			item = &kubocdv1alpha1.Context{}
		default:
			return fmt.Errorf("unexpected list %T", list)
		}
		item.SetNamespace("ns-" + name)
		item.SetName(name)
		items = append(items, item)
	}
	return apimeta.SetList(list, items)
}

// logLines collects the lines of a logger.
type logLines struct {
	lines []string
}

func (l *logLines) write(prefix, args string) {
	l.lines = append(l.lines, prefix+" "+args)
}

// releases is the resource of the lists that the API server refuses.
var releases = schema.GroupResource{Group: "kubocd.kubotal.io", Resource: "releases"}

func TestAFailedListOfAWatchIsLoggedWithoutItsMessage(t *testing.T) {
	failures := map[string]struct {
		err  error
		want string
	}{
		"cache error": {fmt.Errorf("unable to list the objects of %s: no such index in the cache", watchCanary),
			"the request to the API server failed, message withheld, as it can quote the object"},
		"refused by the API server": {apierrors.NewForbidden(releases, watchCanary, errors.New("access denied")),
			"the API server answered Forbidden, message withheld, as it can quote the object"},
	}
	for name, m := range mapFunctions() {
		for failure, f := range failures {
			t.Run(name+", "+failure, func(t *testing.T) {
				if !strings.Contains(f.err.Error(), watchCanary) {
					t.Fatalf("the error %q does not quote the canary", f.err)
				}
				log := &logLines{}
				reader := listing(t, func(client.ObjectList, string) error { return f.err })
				got := m.build(reader, funcr.New(log.write, funcr.Options{}))(context.Background(), m.object)
				text := strings.Join(log.lines, "\n")
				if len(got) != 0 || len(log.lines) != 1 || strings.Contains(text, watchCanary) ||
					!strings.Contains(text, `"msg"="`+m.msg+`"`) || !strings.Contains(text, `"error"="`+f.want+`"`) {
					t.Fatalf("got requests %v and log\n%s\nwant no request and one line %q with %q", got, text, m.msg, f.want)
				}
			})
		}
	}
}

func TestAWatchThatFindsNothingLogsNothing(t *testing.T) {
	for name, m := range mapFunctions() {
		t.Run(name, func(t *testing.T) {
			log := &logLines{}
			reader := listing(t, func(client.ObjectList, string) error { return apierrors.NewNotFound(releases, watchCanary) })
			got := m.build(reader, funcr.New(log.write, funcr.Options{}))(context.Background(), m.object)
			if len(got) != 0 || len(log.lines) != 0 {
				t.Fatalf("got requests %v and log %v, want neither", got, log.lines)
			}
		})
	}
}

func TestAWatchRequestsTheObjectsItLists(t *testing.T) {
	want := []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "ns-a", Name: "a"}},
		{NamespacedName: types.NamespacedName{Namespace: "ns-b", Name: "b"}}}
	for name, m := range mapFunctions() {
		t.Run(name, func(t *testing.T) {
			selectors := make([]string, 0, 1)
			reader := listing(t, func(list client.ObjectList, selector string) error {
				selectors = append(selectors, selector)
				return twoObjects(list)
			})
			got := m.build(reader, logr.Discard())(context.Background(), m.object)
			if !slices.Equal(got, want) || !slices.Equal(selectors, []string{m.selector}) {
				t.Fatalf("got requests %v from the lists %q, want %v from %q", got, selectors, want, m.selector)
			}
		})
	}
}

// failingIndexer records the indexes it creates, and fails to create the one
// named failing.
type failingIndexer struct {
	failing string
	created []string
}

func (f *failingIndexer) IndexField(_ context.Context, _ client.Object, field string, _ client.IndexerFunc) error {
	if field == f.failing {
		return errors.New("unable to get the informer")
	}
	f.created = append(f.created, field)
	return nil
}

func TestAnIndexOfTheReleasesThatCannotBeCreatedIsReturned(t *testing.T) {
	names := []string{outputClusterConnectionIndexOnRelease, watchedInputConnectionIndexOnRelease,
		watchedInputClusterConnectionIndexOnRelease, contextIndexOnRelease}
	for _, name := range names {
		err := indexReleases(&failingIndexer{failing: name})
		want := "could not create the index " + name + " of the Releases: unable to get the informer"
		if err == nil || err.Error() != want {
			t.Errorf("got %v, want %q", err, want)
		}
	}
	indexer := &failingIndexer{}
	if err := indexReleases(indexer); err != nil || !slices.Equal(indexer.created, names) {
		t.Fatalf("got error %v and the indexes %q, want %q", err, indexer.created, names)
	}
}
