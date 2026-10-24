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

package cmd

import (
	"context"
	"fmt"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/controller"
	"slices"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// releaseController builds the controller of the Releases, with the objects
// it owns and the events of the objects that a Release reads. A change of the
// Configs enqueues every Release at its event, and again once the Config
// controller has published the configuration, which configPublished signals.
// Connections are watched twice: as outputs of a Release, which owns them,
// and as inputs.
func releaseController(mgr ctrl.Manager, reconciler reconcile.Reconciler, configStore configstore.ConfigStore,
	configPublished <-chan event.GenericEvent, logger logr.Logger) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubocdv1alpha1.Release{}).
		Named("kubocd-release").
		Owns(&sourcev1.OCIRepository{}).
		Owns(&sourcev1.HelmRepository{}).
		Owns(&fluxv2.HelmRelease{}).
		Owns(&kubocdv1alpha1.Connection{}).
		Watches(&kubocdv1alpha1.ClusterConnection{}, handler.EnqueueRequestsFromMapFunc(
			findReleaseFromOutputClusterConnection(mgr.GetClient(), logger))).
		Watches(&kubocdv1alpha1.Context{}, handler.EnqueueRequestsFromMapFunc(
			findReleaseFromContext(mgr.GetClient(), configStore, logger))).
		Watches(&kubocdv1alpha1.Config{}, handler.EnqueueRequestsFromMapFunc(
			findReleaseFromConfig(mgr.GetClient(), logger))).
		WatchesRawSource(source.Channel(configPublished, handler.EnqueueRequestsFromMapFunc(
			findReleaseFromConfig(mgr.GetClient(), logger)))).
		Watches(&kubocdv1alpha1.Connection{}, handler.EnqueueRequestsFromMapFunc(
			findReleaseFromWatchedInputConnection(mgr.GetClient(), logger))).
		Watches(&kubocdv1alpha1.ClusterConnection{}, handler.EnqueueRequestsFromMapFunc(
			findReleaseFromWatchedInputClusterConnection(mgr.GetClient(), logger))).
		Complete(reconciler)
}

// The indexes of the cache, on Releases and Contexts, that the map functions
// below read.
const (
	outputClusterConnectionIndexOnRelease       = "outputClusterConnectionIndexOnRelease"
	watchedInputConnectionIndexOnRelease        = "watchedInputConnectionIndexOnRelease"
	watchedInputClusterConnectionIndexOnRelease = "watchedInputClusterConnectionIndexOnRelease"
	contextIndexOnRelease                       = "contextIndexOnRelease"
	parentIndexOnChild                          = "parentIndexOnChild"
)

// indexReleases creates the indexes of the Releases that the map functions
// below read, and returns the first error. An index reads the Release only:
// the cache removes the keys of an object with the keys that the function
// gives at that time.
func indexReleases(indexer client.FieldIndexer) error {
	indexes := []struct {
		name    string
		extract client.IndexerFunc
	}{
		// Releases by output ClusterConnections
		{outputClusterConnectionIndexOnRelease, func(rawObj client.Object) []string {
			release := rawObj.(*kubocdv1alpha1.Release)
			clusterConnections := make([]string, 0, len(release.Status.OutputConnectionByName))
			for _, clusterConnection := range release.Status.OutputConnectionByName {
				if clusterConnection.Kind == kubocdv1alpha1.KindClusterConnection {
					clusterConnections = append(clusterConnections, clusterConnection.Name)
				}
			}
			return clusterConnections
		}},
		// Releases by input Connections
		{watchedInputConnectionIndexOnRelease, func(rawObj client.Object) []string {
			release := rawObj.(*kubocdv1alpha1.Release)
			connections := make([]string, len(release.Status.WatchedInputConnections))
			for idx, connection := range release.Status.WatchedInputConnections {
				if connection.Kind == kubocdv1alpha1.KindConnection {
					connections[idx] = fmt.Sprintf("%s:%s", connection.Namespace, connection.Name)
				}
			}
			return connections
		}},
		// Releases by input ClusterConnections
		{watchedInputClusterConnectionIndexOnRelease, func(rawObj client.Object) []string {
			release := rawObj.(*kubocdv1alpha1.Release)
			connections := make([]string, len(release.Status.WatchedInputConnections))
			for idx, connection := range release.Status.WatchedInputConnections {
				if connection.Kind == kubocdv1alpha1.KindClusterConnection {
					connections[idx] = connection.Name
				}
			}
			return connections
		}},
		// Releases by the Contexts of their spec
		{contextIndexOnRelease, func(rawObj client.Object) []string {
			release := rawObj.(*kubocdv1alpha1.Release)
			contexts := make([]string, 0, len(release.Spec.Contexts))
			for _, context := range release.Spec.Contexts {
				ns := context.Namespace
				if ns == "" {
					ns = release.Namespace
				}
				contexts = append(contexts, fmt.Sprintf("%s:%s", ns, context.Name))
			}
			return contexts
		}},
	}
	for _, index := range indexes {
		if err := indexer.IndexField(context.Background(), &kubocdv1alpha1.Release{}, index.name, index.extract); err != nil {
			return fmt.Errorf("could not create the index %s of the Releases: %w", index.name, err)
		}
	}
	return nil
}

// requestsFromList requests the reconciliation of each object that reader
// lists in list with options. A failed list is logged with msg, without its
// message, which can quote the selector, made from object names.
func requestsFromList(ctx context.Context, reader client.Reader, logger logr.Logger, list client.ObjectList, msg string,
	options ...client.ListOption) []reconcile.Request {
	if err := reader.List(ctx, list, options...); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(controller.ClientError(err), msg)
		}
		return []reconcile.Request{}
	}
	items, err := apimeta.ExtractList(list)
	if err != nil {
		logger.Error(err, msg)
		return []reconcile.Request{}
	}
	requests := make([]reconcile.Request, 0, len(items))
	for _, item := range items {
		if object, ok := item.(client.Object); ok {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: object.GetName(), Namespace: object.GetNamespace()},
			})
		}
	}
	return requests
}

// selecting is the option of a list of the objects that index maps to value.
func selecting(index string, value string) client.ListOption {
	return &client.ListOptions{FieldSelector: fields.OneTermEqualSelector(index, value)}
}

// findReplicationFromResource maps a Secret or a ConfigMap, of the kind given,
// to the Replications whose source or destination it is. The event does not
// carry the kind, so each watched kind gets its own map function.
func findReplicationFromResource(reader client.Reader, logger logr.Logger, kind string) handler.MapFunc {
	return func(ctx context.Context, resource client.Object) []reconcile.Request {
		key := controller.ResourceKey(resource.GetNamespace(), kind, resource.GetName())
		return requestsFromList(ctx, reader, logger.WithValues("kind", kind), &kubocdv1alpha1.ReplicationList{},
			"findReplicationFromResource(): Unable to find replications", selecting(controller.ResourceIndexOnReplication, key))
	}
}

// findConnectionFromContract maps a Contract or a ClusterContract to the
// Connections that name it.
func findConnectionFromContract(reader client.Reader, logger logr.Logger) handler.MapFunc {
	return func(ctx context.Context, contract client.Object) []reconcile.Request {
		return requestsFromList(ctx, reader, logger, &kubocdv1alpha1.ConnectionList{},
			"findConnectionFromContract(): Unable to find contract bindings",
			selecting(controller.ContractIndexOnConnection, contract.GetName()))
	}
}

// findClusterConnectionFromClusterContract maps a ClusterContract to the
// ClusterConnections that name it.
func findClusterConnectionFromClusterContract(reader client.Reader, logger logr.Logger) handler.MapFunc {
	return func(ctx context.Context, clusterContract client.Object) []reconcile.Request {
		return requestsFromList(ctx, reader, logger, &kubocdv1alpha1.ClusterConnectionList{},
			"findClusterConnectionFromClusterContract(): Unable to find contract bindings",
			selecting(controller.ContractIndexOnClusterConnection, clusterContract.GetName()))
	}
}

// findReleaseFromOutputClusterConnection maps a ClusterConnection to the
// Release whose output it is. It stands for an owner reference, which cannot
// cross namespaces.
func findReleaseFromOutputClusterConnection(reader client.Reader, logger logr.Logger) handler.MapFunc {
	return func(ctx context.Context, clusterConnection client.Object) []reconcile.Request {
		return requestsFromList(ctx, reader, logger, &kubocdv1alpha1.ReleaseList{},
			"findReleaseFromOutputClusterConnection(): Unable to find ClusterConnection bindings",
			selecting(outputClusterConnectionIndexOnRelease, clusterConnection.GetName()))
	}
}

// findReleaseFromWatchedInputConnection maps a Connection to the Releases that
// watch it as an input.
func findReleaseFromWatchedInputConnection(reader client.Reader, logger logr.Logger) handler.MapFunc {
	return func(ctx context.Context, connection client.Object) []reconcile.Request {
		key := fmt.Sprintf("%s:%s", connection.GetNamespace(), connection.GetName())
		return requestsFromList(ctx, reader, logger, &kubocdv1alpha1.ReleaseList{},
			"findReleaseFromWatchedInputConnection(): Unable to find Connection bindings",
			selecting(watchedInputConnectionIndexOnRelease, key))
	}
}

// findReleaseFromWatchedInputClusterConnection maps a ClusterConnection to the
// Releases that watch it as an input.
func findReleaseFromWatchedInputClusterConnection(reader client.Reader, logger logr.Logger) handler.MapFunc {
	return func(ctx context.Context, connection client.Object) []reconcile.Request {
		return requestsFromList(ctx, reader, logger, &kubocdv1alpha1.ReleaseList{},
			"findReleaseFromWatchedInputClusterConnection(): Unable to find ClusterConnection bindings",
			selecting(watchedInputClusterConnectionIndexOnRelease, connection.GetName()))
	}
}

// findReleaseFromContext maps a Context to the Releases that use it: those
// that name it in their spec, and, when the configuration in effect makes it
// a default Context, every Release, or every Release of its namespace.
func findReleaseFromContext(reader client.Reader, configStore configstore.ConfigStore,
	logger logr.Logger) handler.MapFunc {
	const msg = "findReleaseFromContext(): Unable to find Context bindings"
	return func(ctx context.Context, kcontext client.Object) []reconcile.Request {
		requests := requestsFromList(ctx, reader, logger, &kubocdv1alpha1.ReleaseList{}, msg,
			selecting(contextIndexOnRelease, fmt.Sprintf("%s:%s", kcontext.GetNamespace(), kcontext.GetName())))
		configuration := configStore.Current()
		name := kubocdv1alpha1.NamespacedName{Namespace: kcontext.GetNamespace(), Name: kcontext.GetName()}
		if slices.Contains(configuration.GetDefaultContexts(), name) {
			requests = append(requests, requestsFromList(ctx, reader, logger, &kubocdv1alpha1.ReleaseList{}, msg)...)
		} else if slices.Contains(configuration.GetDefaultNamespaceContexts(), name.Name) {
			requests = append(requests, requestsFromList(ctx, reader, logger, &kubocdv1alpha1.ReleaseList{}, msg,
				client.InNamespace(name.Namespace))...)
		}
		return uniqueRequests(requests)
	}
}

func uniqueRequests(requests []reconcile.Request) []reconcile.Request {
	seen := make(map[reconcile.Request]bool, len(requests))
	unique := make([]reconcile.Request, 0, len(requests))
	for _, request := range requests {
		if !seen[request] {
			seen[request] = true
			unique = append(unique, request)
		}
	}
	return unique
}

// findReleaseFromConfig maps a Config to every Release.
func findReleaseFromConfig(reader client.Reader, logger logr.Logger) handler.MapFunc {
	return func(ctx context.Context, _ client.Object) []reconcile.Request {
		return requestsFromList(ctx, reader, logger, &kubocdv1alpha1.ReleaseList{},
			"findReleaseFromConfig(): Unable to find releases")
	}
}

// findChildFromParent maps a Context to the Contexts whose parent it is.
func findChildFromParent(reader client.Reader, logger logr.Logger) handler.MapFunc {
	return func(ctx context.Context, parentContext client.Object) []reconcile.Request {
		return requestsFromList(ctx, reader, logger, &kubocdv1alpha1.ContextList{},
			"findChildFromParent(): Unable to find Context bindings",
			selecting(parentIndexOnChild, fmt.Sprintf("%s:%s", parentContext.GetNamespace(), parentContext.GetName())))
	}
}
