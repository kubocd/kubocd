package controller

import (
	"context"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"sort"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

type BuildInputModelHelper interface {
	client.Client
	FindOutputConnectionsFromRelease(ctx context.Context, release types.NamespacedName) ([]kv1alpha1.Connection, ReconcileError)
	FindOutputClusterConnectionsFromRelease(ctx context.Context, release types.NamespacedName) ([]kv1alpha1.ClusterConnection, ReconcileError)
	FindConnectionsFromInterface(ctx context.Context, namespace string, iface string) ([]kv1alpha1.Connection, ReconcileError)
	FindClusterConnectionsFromInterface(ctx context.Context, iface string) ([]kv1alpha1.ClusterConnection, ReconcileError)
}

type BuildInputModelResult struct {
	// The map to be inserted in the data model
	InputModel map[string]interface{}
	// For multiple result (When allowMultiple)
	InputListModel map[string]interface{}
	// A list of the input connection, used to managed reconciliation triggering.
	WatchedInputConnections []kv1alpha1.InputConnectionReference
	// Array by input#. The selected connection for each input
	EffectiveInputConnections []kv1alpha1.InputConnectionReference `json:"effectiveInputConnections"`
	// A list of missing (unmanaged) connection, to be set in status for human display
	Messages []string
}

// BuildInputModel build the '.Inputs' in the data model for rendering values.
func BuildInputModel(ctx context.Context, helper BuildInputModelHelper, inputs []kubopackage.InputRendered) (*BuildInputModelResult, ReconcileError) {
	resultCollector := &BuildInputModelResult{
		InputModel:                make(map[string]interface{}),
		InputListModel:            make(map[string]interface{}),
		WatchedInputConnections:   make([]kv1alpha1.InputConnectionReference, 0, len(inputs)),
		EffectiveInputConnections: make([]kv1alpha1.InputConnectionReference, len(inputs)),
		Messages:                  make([]string, 0, len(inputs)),
	}
	for idx, input := range inputs {
		if input.NamedConnection.Name != "" {
			// ----------------------------------------------------------Connection is explicit.
			err := bimHandleNamedConnection(ctx, idx, input, helper, resultCollector)
			if err != nil {
				return resultCollector, err
			}
		} else if input.Release.Name != "" {
			err := bimHandleReleaseConnection(ctx, idx, input, helper, resultCollector)
			if err != nil {
				return resultCollector, err
			}
		} else {
			// ---------------------------------------------------------- We lookup connections by interface
			err := bimHandleInterfaceConnection(ctx, idx, input, helper, resultCollector)
			if err != nil {
				return resultCollector, err
			}
		}
	}
	return resultCollector, nil
}

func bimHandleNamedConnection(ctx context.Context, idx int, input kubopackage.InputRendered, helper BuildInputModelHelper, resultCollector *BuildInputModelResult) ReconcileError {

	var bimFetchNamedConnection = func(kind kv1alpha1.Kind) (kv1alpha1.ConnectionFacade, ReconcileError) {
		var connectionFacade kv1alpha1.ConnectionFacade
		var nsName types.NamespacedName
		if kind == kv1alpha1.KindConnection {
			connectionFacade = &kv1alpha1.Connection{}
			nsName = types.NamespacedName{Namespace: input.NamedConnection.Namespace, Name: input.NamedConnection.Name}
		} else {
			// A ClusterConnection is cluster-scoped: lookup by name only
			connectionFacade = &kv1alpha1.ClusterConnection{}
			nsName = types.NamespacedName{Name: input.NamedConnection.Name}
		}
		err := helper.Get(ctx, nsName, connectionFacade)
		if err != nil {
			if k8serrors.IsNotFound(err) {
				// We set in the status list even if not found or in error. As we want to be notified if created.
				resultCollector.WatchedInputConnections = append(resultCollector.WatchedInputConnections, kv1alpha1.InputConnectionReference{
					Kind:      kind,
					Name:      nsName.Name,
					Namespace: nsName.Namespace,
				})
				return nil, nil
			}
			return nil, NewReconcileError(fmt.Errorf("input#%d: could not get %s '%s': %w", idx+1, kind, nsName.String(), err), false, "")
		}
		if connectionFacade.GetInterface() != input.Interface {
			if input.Kind == "" {
				// Dual lookup: discard this candidate, the other kind may
				// carry the right interface. If none does, the release waits
				// with the 'Waiting for namedConnection' message.
				return nil, nil
			}
			return connectionFacade, NewReconcileError(fmt.Errorf("input#%d: Interface mismatch: '%s' != '%s'", idx+1, input.Interface, connectionFacade.GetInterface()), false, "")
		}
		return connectionFacade, nil
	}

	collectionFacades := make([]kv1alpha1.ConnectionFacade, 0, 2)
	if input.Kind == "" || input.Kind == kv1alpha1.KindConnection {
		cf, err := bimFetchNamedConnection(kv1alpha1.KindConnection)
		if err != nil {
			return err
		}
		if cf != nil {
			collectionFacades = append(collectionFacades, cf)
		}
	}
	if input.Kind == "" || input.Kind == kv1alpha1.KindClusterConnection {
		cf, err := bimFetchNamedConnection(kv1alpha1.KindClusterConnection)
		if err != nil {
			return err
		}
		if cf != nil {
			collectionFacades = append(collectionFacades, cf)
		}
	}
	return bimFilterConnection(collectionFacades, idx, input, resultCollector)
}

func bimHandleReleaseConnection(ctx context.Context, idx int, input kubopackage.InputRendered, helper BuildInputModelHelper, resultCollector *BuildInputModelResult) ReconcileError {
	collectionFacades := make([]kv1alpha1.ConnectionFacade, 0, 5)
	if input.Kind == "" || input.Kind == kv1alpha1.KindConnection {
		cnx, err := helper.FindOutputConnectionsFromRelease(ctx, types.NamespacedName{Namespace: input.Release.Namespace, Name: input.Release.Name})
		if err != nil {
			return err
		}
		for i := range cnx {
			collectionFacades = append(collectionFacades, &cnx[i])
		}
	}
	if input.Kind == "" || input.Kind == kv1alpha1.KindClusterConnection {
		cnx, err := helper.FindOutputClusterConnectionsFromRelease(ctx, types.NamespacedName{Namespace: input.Release.Namespace, Name: input.Release.Name})
		if err != nil {
			return err
		}
		for i := range cnx {
			collectionFacades = append(collectionFacades, &cnx[i])
		}
	}
	return bimFilterConnection(collectionFacades, idx, input, resultCollector)
}

func bimHandleInterfaceConnection(ctx context.Context, idx int, input kubopackage.InputRendered, helper BuildInputModelHelper, resultCollector *BuildInputModelResult) ReconcileError {
	collectionFacades := make([]kv1alpha1.ConnectionFacade, 0, 5)
	if input.Kind == "" || input.Kind == kv1alpha1.KindConnection {
		cnx, err := helper.FindConnectionsFromInterface(ctx, input.InterfaceLookup.Namespace, input.Interface)
		if err != nil {
			return err
		}
		for i := range cnx {
			collectionFacades = append(collectionFacades, &cnx[i])
		}
	}
	if input.Kind == "" || input.Kind == kv1alpha1.KindClusterConnection {
		cnx, err := helper.FindClusterConnectionsFromInterface(ctx, input.Interface)
		if err != nil {
			return err
		}
		for i := range cnx {
			collectionFacades = append(collectionFacades, &cnx[i])
		}
	}
	return bimFilterConnection(collectionFacades, idx, input, resultCollector)
}

// Called in case of search by Release or by interface
func bimFilterConnection(connections []kv1alpha1.ConnectionFacade, idx int, input kubopackage.InputRendered, resultCollector *BuildInputModelResult) ReconcileError {
	electedConnections := make([]kv1alpha1.ConnectionFacade, 0, len(connections))
	possibleConnectionNames := make([]string, 0, len(connections))
	notReady := make([]kv1alpha1.ConnectionFacade, 0, len(connections))
	for _, connection := range connections {
		if connection.GetInterface() != input.Interface {
			continue
		}
		if input.Release.Name != "" && input.Release.Output != "" {
			// Must filter also on output
			if input.Release.Output != connection.GetOutputName() {
				continue
			}
		}
		// Watch every candidate of the right interface, label-matching or not:
		// a label ADDED later must wake the release up
		resultCollector.WatchedInputConnections = append(resultCollector.WatchedInputConnections, kv1alpha1.InputConnectionReference{
			Kind:      connection.GetKind(),
			Name:      connection.GetName(),
			Namespace: connection.GetNamespace(),
		})
		if !matchesLabels(connection.GetLabels(), input.MatchLabels) {
			continue
		}
		possibleConnectionNames = append(possibleConnectionNames, connection.GetName())
		if connection.GetStatusPhase() == kv1alpha1.ConnectionPhaseReady {
			electedConnections = append(electedConnections, connection)
		} else {
			notReady = append(notReady, connection)
		}
	}
	if len(electedConnections) == 0 {
		if !input.Optional {
			var mess string
			if input.Release.Name != "" {
				mess = fmt.Sprintf("input#%d (%s): Waiting for connection from release '%s:%s'", idx+1, input.Alias, input.Release.Namespace, input.Release.Name)
			} else if input.NamedConnection.Name != "" {
				mess = fmt.Sprintf("input#%d (%s): Waiting for namedConnection '%s:%s'", idx+1, input.Alias, input.NamedConnection.Namespace, input.NamedConnection.Name)
			} else {
				mess = fmt.Sprintf("input#%d (%s): Waiting for a connection with interface '%s'", idx+1, input.Alias, input.Interface)
			}
			if len(notReady) > 0 {
				// Surface the ROOT CAUSE: a consumer must not just say
				// 'waiting' while its producer is broken
				details := make([]string, 0, len(notReady))
				for _, c := range notReady {
					d := fmt.Sprintf("%s is %s", c.GetName(), c.GetStatusPhase())
					if parent := c.GetParent(); parent != "" {
						d = fmt.Sprintf("%s (producer release %s)", d, parent)
					}
					if msg := c.GetStatusMessage(); msg != "" {
						d = fmt.Sprintf("%s: %s", d, msg)
					}
					details = append(details, d)
				}
				mess = fmt.Sprintf("%s  [%s]", mess, strings.Join(details, " | "))
			}
			resultCollector.Messages = append(resultCollector.Messages, mess)
		}
		return nil
	}
	if !input.AllowMultiple {
		if len(possibleConnectionNames) > 1 {
			resultCollector.Messages = append(resultCollector.Messages, fmt.Sprintf("input#%d: Too many possible connections: %s", idx+1, strings.Join(possibleConnectionNames, ",")))
			return nil
		}
		// len(electedConnections) == 1, by construction
		values, err := parseValue(idx, electedConnections[0])
		if err != nil {
			return err
		}
		resultCollector.InputModel[input.Alias] = values
		resultCollector.EffectiveInputConnections[idx] = kv1alpha1.InputConnectionReference{
			Kind:      electedConnections[0].GetKind(),
			Name:      electedConnections[0].GetName(),
			Namespace: electedConnections[0].GetNamespace(),
		}
		return nil
	}
	sort.Slice(electedConnections, func(i, j int) bool {
		if electedConnections[i].GetPriority() == electedConnections[j].GetPriority() {
			// Deterministic tie-break: ascending name order
			return electedConnections[i].GetName() < electedConnections[j].GetName()
		}
		return electedConnections[i].GetPriority() > electedConnections[j].GetPriority()
	})
	// Set the elected value (Higher priority)
	values, err := parseValue(idx, electedConnections[0])
	if err != nil {
		return err
	}
	resultCollector.InputModel[input.Alias] = values
	resultCollector.EffectiveInputConnections[idx] = kv1alpha1.InputConnectionReference{
		Kind:      electedConnections[0].GetKind(),
		Name:      electedConnections[0].GetName(),
		Namespace: electedConnections[0].GetNamespace(),
	}
	// And set the list of connectors
	inputList := make([]map[string]interface{}, len(electedConnections))
	for idx, electedConnection := range electedConnections {
		values, err := parseValue(idx, electedConnection)
		if err != nil {
			return err
		}
		inputList[idx] = values
	}
	resultCollector.InputListModel[input.Alias] = inputList
	return nil
}

// matchesLabels tells if the labels of a connection satisfy the (possibly
// empty) matchLabels filter of a generated selector input.
func matchesLabels(labels map[string]string, matchLabels map[string]string) bool {
	for k, v := range matchLabels {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func parseValue(idx int, conn kv1alpha1.ConnectionFacade) (map[string]interface{}, ReconcileError) {
	values := make(map[string]interface{})
	if conn.GetValuesRaw() != nil {
		err := yaml.UnmarshalStrict(conn.GetValuesRaw(), &values)
		if err != nil {
			return nil, NewReconcileError(fmt.Errorf("input #%d: could not unmarshal connection '%s' values: %w", idx, types.NamespacedName{Namespace: conn.GetNamespace(), Name: conn.GetName()}.String(), err), false, "")
		}
	}
	return values, nil
}
