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

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

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
// below read, and returns the first error.
func indexReleases(indexer client.FieldIndexer, configStore configstore.ConfigStore) error {
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
		// Releases by Contexts, the default ones included
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
			for _, ctx := range configStore.GetDefaultContexts() {
				contexts = append(contexts, fmt.Sprintf("%s:%s", ctx.Namespace, ctx.Name))
			}
			for _, context := range configStore.GetDefaultNamespaceContexts() {
				contexts = append(contexts, fmt.Sprintf("%s:%s", release.Namespace, context))
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

// findReleaseFromContext maps a Context to the Releases that use it.
func findReleaseFromContext(reader client.Reader, logger logr.Logger) handler.MapFunc {
	return func(ctx context.Context, kcontext client.Object) []reconcile.Request {
		return requestsFromList(ctx, reader, logger, &kubocdv1alpha1.ReleaseList{},
			"findReleaseFromContext(): Unable to find Context bindings",
			selecting(contextIndexOnRelease, fmt.Sprintf("%s:%s", kcontext.GetNamespace(), kcontext.GetName())))
	}
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
