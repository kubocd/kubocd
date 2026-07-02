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
	var connectionFacade kv1alpha1.ConnectionFacade
	if input.Kind == kv1alpha1.KindConnection {
		connectionFacade = &kv1alpha1.ClusterConnection{}
	} else {
		connectionFacade = &kv1alpha1.Connection{}
	}
	// User target an unmanaged connection/clusterConnection. Just read it
	nsName := types.NamespacedName{Namespace: input.Namespace, Name: input.NamedConnection.Name}
	// We set in the status list even if not found or in error. As we want to be notified if created.
	resultCollector.WatchedInputConnections = append(resultCollector.WatchedInputConnections, kv1alpha1.InputConnectionReference{
		Kind:      connectionFacade.GetKind(),
		Name:      nsName.Name,
		Namespace: nsName.Namespace,
	})
	err := helper.Get(ctx, nsName, connectionFacade)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			if !input.Optional {
				resultCollector.Messages = append(resultCollector.Messages, fmt.Sprintf("input#%d: Waiting for namedConnection '%s'.", idx, nsName.String()))
			}
			return nil
		}
		return NewReconcileError(fmt.Errorf("input#%d: could not get connection '%s': %w", idx, nsName.String(), err), false, "")
	}
	if connectionFacade.GetInterface() != input.Interface {
		return NewReconcileError(fmt.Errorf("input#%d: Interface mismatch: '%s' != '%s'", idx, input.Interface, connectionFacade.GetInterface()), false, "")
	}
	if connectionFacade.GetStatusPhase() != kv1alpha1.ConnectionPhaseReady {
		if !input.Optional {
			resultCollector.Messages = append(resultCollector.Messages, fmt.Sprintf("input#%d: Waiting for namedConnection '%s' to be ready", idx, nsName.String()))
		}
		return nil
	}
	values := make(map[string]interface{})
	err = yaml.UnmarshalStrict(connectionFacade.GetValues().Raw, &values)
	if err != nil {
		return NewReconcileError(fmt.Errorf("input #%d: could not unmarshal connection '%s' values: %w", idx, nsName.String(), err), false, "")
	}
	resultCollector.EffectiveInputConnections[idx] = kv1alpha1.InputConnectionReference{
		Kind:      connectionFacade.GetKind(),
		Name:      nsName.Name,
		Namespace: nsName.Namespace,
	}
	resultCollector.InputModel[input.Alias] = values
	return nil
}

func bimHandleReleaseConnection(ctx context.Context, idx int, input kubopackage.InputRendered, helper BuildInputModelHelper, resultCollector *BuildInputModelResult) ReconcileError {
	collectionFacades := make([]kv1alpha1.ConnectionFacade, 0, 5)
	if input.Kind == "" || input.Kind == kv1alpha1.KindConnection {
		cnx, err := helper.FindOutputConnectionsFromRelease(ctx, types.NamespacedName{Namespace: input.Namespace, Name: input.Release.Name})
		if err != nil {
			return err
		}
		for i := range cnx {
			collectionFacades = append(collectionFacades, &cnx[i])
		}
	}
	if input.Kind == "" || input.Kind == kv1alpha1.KindClusterConnection {
		cnx, err := helper.FindOutputClusterConnectionsFromRelease(ctx, types.NamespacedName{Namespace: input.Namespace, Name: input.Release.Name})
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
		cnx, err := helper.FindConnectionsFromInterface(ctx, input.Namespace, input.Interface)
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

func bimFilterConnection(connections []kv1alpha1.ConnectionFacade, idx int, input kubopackage.InputRendered, resultCollector *BuildInputModelResult) ReconcileError {
	electedConnections := make([]kv1alpha1.ConnectionFacade, 0, len(connections))
	possibleConnectionNames := make([]string, 0, len(connections))
	for _, connection := range connections {
		if connection.GetInterface() != input.Interface {
			continue
		}
		if input.Release.Name != "" && input.Release.OutputName != "" && connection.GetOutputName() != input.Release.OutputName {
			continue
		}
		possibleConnectionNames = append(possibleConnectionNames, connection.GetName())
		resultCollector.WatchedInputConnections = append(resultCollector.WatchedInputConnections, kv1alpha1.InputConnectionReference{
			Kind:      connection.GetKind(),
			Name:      connection.GetName(),
			Namespace: connection.GetNamespace(),
		})
		if connection.GetStatusPhase() == kv1alpha1.ConnectionPhaseReady {
			electedConnections = append(electedConnections, connection)
		}
	}
	if len(electedConnections) == 0 {
		if !input.Optional {
			var mess string
			if input.Release.Name != "" {
				mess = fmt.Sprintf("input#%d: Waiting for connection for release '%s:%s'", idx, input.Namespace, input.Release.Name)
			} else {
				mess = fmt.Sprintf("input#%d: Waiting for connection for interface '%s'", idx, input.Interface)
			}
			if len(possibleConnectionNames) > 0 {
				mess = fmt.Sprintf("%s  (%s not ready)", mess, strings.Join(possibleConnectionNames, ", "))
			}
			resultCollector.Messages = append(resultCollector.Messages, mess)
		}
		return nil
	}
	if !input.AllowMultiple {
		if len(possibleConnectionNames) > 1 {
			resultCollector.Messages = append(resultCollector.Messages, fmt.Sprintf("input#%d: Too many possible connections: %s", idx, strings.Join(possibleConnectionNames, ",")))
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
			return electedConnections[i].GetName() > electedConnections[j].GetName()
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

func parseValue(idx int, conn kv1alpha1.ConnectionFacade) (map[string]interface{}, ReconcileError) {
	values := make(map[string]interface{})
	err := yaml.UnmarshalStrict(conn.GetValues().Raw, &values)
	if err != nil {
		return nil, NewReconcileError(fmt.Errorf("input #%d: could not unmarshal connection '%s' values: %w", idx, types.NamespacedName{Namespace: conn.GetNamespace(), Name: conn.GetName()}.String(), err), false, "")
	}
	return values, nil
}
