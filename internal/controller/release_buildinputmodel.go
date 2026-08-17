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
	FindConnectionsFromContract(ctx context.Context, namespace string, contract string) ([]kv1alpha1.Connection, ReconcileError)
	FindClusterConnectionsFromContract(ctx context.Context, contract string) ([]kv1alpha1.ClusterConnection, ReconcileError)
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
func BuildInputModel(ctx context.Context, helper BuildInputModelHelper, inputs []*kubopackage.InputRendered) (*BuildInputModelResult, ReconcileError) {
	resultCollector := &BuildInputModelResult{
		InputModel:                make(map[string]interface{}),
		InputListModel:            make(map[string]interface{}),
		WatchedInputConnections:   make([]kv1alpha1.InputConnectionReference, 0, len(inputs)),
		EffectiveInputConnections: make([]kv1alpha1.InputConnectionReference, len(inputs)),
		Messages:                  make([]string, 0, len(inputs)),
	}
	for idx, input := range inputs {
		if input.ConnectionRef.Name != "" {
			// ----------------------------------------------------------Connection is explicit.
			err := bimHandleConnectionRef(ctx, idx, input, helper, resultCollector)
			if err != nil {
				return resultCollector, err
			}
		} else if input.ReleaseRef.Name != "" {
			err := bimHandleReleaseRef(ctx, idx, input, helper, resultCollector)
			if err != nil {
				return resultCollector, err
			}
		} else if input.GetContractLookupNamespace() != "" {
			// ---------------------------------------------------------- We lookup connections by contract
			err := bimHandleContractConnection(ctx, idx, input, helper, resultCollector)
			if err != nil {
				return resultCollector, err
			}
		} else {
			// Should never happen, See input rendering
			return nil, NewReconcileError(fmt.Errorf("input[%d]: Missing information", idx), true, "InputError")
		}
	}
	return resultCollector, nil
}

func bimHandleConnectionRef(ctx context.Context, idx int, input *kubopackage.InputRendered, helper BuildInputModelHelper, resultCollector *BuildInputModelResult) ReconcileError {

	var bimFetchConnectionRef = func(kind kv1alpha1.Kind) (kv1alpha1.ConnectionFacade, ReconcileError) {
		var connectionFacade kv1alpha1.ConnectionFacade
		if kind == kv1alpha1.KindConnection {
			connectionFacade = &kv1alpha1.Connection{}
		} else {
			connectionFacade = &kv1alpha1.ClusterConnection{}
		}
		nsName := types.NamespacedName{Namespace: input.ConnectionRef.Namespace, Name: input.ConnectionRef.Name}
		err := helper.Get(ctx, nsName, connectionFacade)
		if err != nil {
			if k8serrors.IsNotFound(err) {
				// We set in the status list even if not found or in error. As we want to be notified if created.
				resultCollector.WatchedInputConnections = append(resultCollector.WatchedInputConnections, kv1alpha1.InputConnectionReference{
					Kind:      connectionFacade.GetKind(),
					Name:      nsName.Name,
					Namespace: nsName.Namespace,
				})
				return nil, nil
			}
			return nil, NewReconcileError(fmt.Errorf("input#%d: could not get %s '%s': %w", idx+1, kind, nsName.String(), err), false, "")
		}
		if connectionFacade.GetContract() != input.Contract {
			return connectionFacade, NewReconcileError(fmt.Errorf("input#%d: Contract mismatch: '%s' != '%s'", idx+1, input.Contract, connectionFacade.GetContract()), false, "")
		}
		return connectionFacade, nil
	}

	collectionFacades := make([]kv1alpha1.ConnectionFacade, 0, 2)
	if input.Kind == "" || input.Kind == kv1alpha1.KindConnection {
		cf, err := bimFetchConnectionRef(kv1alpha1.KindConnection)
		if err != nil {
			return err
		}
		if cf != nil {
			collectionFacades = append(collectionFacades, cf)
		}
	}
	if input.Kind == "" || input.Kind == kv1alpha1.KindClusterConnection {
		cf, err := bimFetchConnectionRef(kv1alpha1.KindClusterConnection)
		if err != nil {
			return err
		}
		if cf != nil {
			collectionFacades = append(collectionFacades, cf)
		}
	}
	return bimFilterConnection(collectionFacades, idx, input, resultCollector)
}

func bimHandleReleaseRef(ctx context.Context, idx int, input *kubopackage.InputRendered, helper BuildInputModelHelper, resultCollector *BuildInputModelResult) ReconcileError {
	collectionFacades := make([]kv1alpha1.ConnectionFacade, 0, 5)
	if input.Kind == "" || input.Kind == kv1alpha1.KindConnection {
		cnx, err := helper.FindOutputConnectionsFromRelease(ctx, types.NamespacedName{Namespace: input.ReleaseRef.Namespace, Name: input.ReleaseRef.Name})
		if err != nil {
			return err
		}
		for i := range cnx {
			collectionFacades = append(collectionFacades, &cnx[i])
		}
	}
	if input.Kind == "" || input.Kind == kv1alpha1.KindClusterConnection {
		cnx, err := helper.FindOutputClusterConnectionsFromRelease(ctx, types.NamespacedName{Namespace: input.ReleaseRef.Namespace, Name: input.ReleaseRef.Name})
		if err != nil {
			return err
		}
		for i := range cnx {
			collectionFacades = append(collectionFacades, &cnx[i])
		}
	}
	return bimFilterConnection(collectionFacades, idx, input, resultCollector)
}

func bimHandleContractConnection(ctx context.Context, idx int, input *kubopackage.InputRendered, helper BuildInputModelHelper, resultCollector *BuildInputModelResult) ReconcileError {
	collectionFacades := make([]kv1alpha1.ConnectionFacade, 0, 5)
	if input.Kind == "" || input.Kind == kv1alpha1.KindConnection {
		cnx, err := helper.FindConnectionsFromContract(ctx, input.GetContractLookupNamespace(), input.Contract)
		if err != nil {
			return err
		}
		for i := range cnx {
			collectionFacades = append(collectionFacades, &cnx[i])
		}
	}
	if input.Kind == "" || input.Kind == kv1alpha1.KindClusterConnection {
		cnx, err := helper.FindClusterConnectionsFromContract(ctx, input.Contract)
		if err != nil {
			return err
		}
		for i := range cnx {
			collectionFacades = append(collectionFacades, &cnx[i])
		}
	}
	return bimFilterConnection(collectionFacades, idx, input, resultCollector)
}

// Called in case of search by Release or by contract
func bimFilterConnection(connections []kv1alpha1.ConnectionFacade, idx int, input *kubopackage.InputRendered, resultCollector *BuildInputModelResult) ReconcileError {
	electedConnections := make([]kv1alpha1.ConnectionFacade, 0, len(connections))
	possibleConnectionNames := make([]string, 0, len(connections))
	for _, connection := range connections {
		if connection.GetContract() != input.Contract {
			continue
		}
		if input.ReleaseRef.Name != "" && input.ReleaseRef.Output != "" {
			// Must filter also on output
			if input.ReleaseRef.Output != connection.GetOutputName() {
				continue
			}
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
			if input.ReleaseRef.Name != "" {
				mess = fmt.Sprintf("input#%d: Waiting for connection from release '%s:%s'", idx+1, input.ReleaseRef.Namespace, input.ReleaseRef.Name)
			} else if input.ConnectionRef.Name != "" {
				mess = fmt.Sprintf("input#%d: Waiting for connection '%s:%s'", idx+1, input.ConnectionRef.Namespace, input.ConnectionRef.Name)
			} else if input.GetContractLookupNamespace() != "" {
				mess = fmt.Sprintf("input#%d: Waiting for a connection with contract '%s' in namespace '%s' or cluster scoped", idx+1, input.Contract, input.GetContractLookupNamespace())
			} else {
				mess = fmt.Sprintf("input#%d: Waiting for a connection with contract '%s'", idx+1, input.Contract)
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
	if conn.GetValuesRaw() != nil {
		err := yaml.UnmarshalStrict(conn.GetValuesRaw(), &values)
		if err != nil {
			return nil, NewReconcileError(fmt.Errorf("input #%d: could not unmarshal connection '%s' values: %w", idx, types.NamespacedName{Namespace: conn.GetNamespace(), Name: conn.GetName()}.String(), err), false, "")
		}
	}
	return values, nil
}
