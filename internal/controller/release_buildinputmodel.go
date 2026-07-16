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
	FindOutputConnectionFromRelease(ctx context.Context, release types.NamespacedName) ([]kv1alpha1.Connection, ReconcileError)
}

type BuildInputModelResult struct {
	// The map to be inserted in the data model
	InputModel map[string]interface{}
	// For multiple result (When allowMultiple)
	InputListModel map[string]interface{}
	// A list of the input connection, used to managed reconciliation triggering.
	WatchedInputConnections []kv1alpha1.WatchedInputConnection
	// A list of missing (unmanaged) connection, to be set in status for human display
	Messages []string
}

// BuildInputModel build the '.Inputs' in the data model for rendering values.
func BuildInputModel(ctx context.Context, helper BuildInputModelHelper, inputs []kubopackage.InputRendered) (*BuildInputModelResult, ReconcileError) {
	resultCollector := &BuildInputModelResult{
		InputModel:              make(map[string]interface{}),
		InputListModel:          make(map[string]interface{}),
		WatchedInputConnections: make([]kv1alpha1.WatchedInputConnection, len(inputs)),
		Messages:                make([]string, len(inputs)),
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
		}
		// ---------------------------------------------------------- We lookup connections by interface

		//r.findConnectionsFromInterface()
		// TODO: Lookup connection based on interface
		return nil, NewReconcileError(fmt.Errorf("input #%d: connection by interface not yet implemented", idx), true, "")
	}
	return resultCollector, nil
}

func bimHandleNamedConnection(ctx context.Context, idx int, input kubopackage.InputRendered, helper BuildInputModelHelper, resultCollector *BuildInputModelResult) ReconcileError {
	connection := &kv1alpha1.Connection{}
	// User target an unmanaged connection. Just read it
	nsName := types.NamespacedName{Namespace: input.Namespace, Name: input.NamedConnection.Name}
	// We set in the status list even if not found or in error. As we want to be notified if created.
	resultCollector.WatchedInputConnections[idx] = kv1alpha1.WatchedInputConnection{
		Name:      nsName.Name,
		Namespace: nsName.Namespace,
	}
	err := helper.Get(ctx, nsName, connection)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			if !input.Optional {
				resultCollector.Messages = append(resultCollector.Messages, fmt.Sprintf("#%d: namedConnection '%s' not found", idx, nsName.String()))
			}
			return nil
		}
		return NewReconcileError(fmt.Errorf("input#%d: could not get connection '%s': %w", idx, nsName.String(), err), false, "")
	}
	if connection.Spec.Interface != input.Interface {
		return NewReconcileError(fmt.Errorf("input#%d: Interface mismatch: '%s' != '%s'", idx, input.Interface, connection.Spec.Interface), false, "")
	}
	if connection.Status.Phase != kv1alpha1.ConnectionPhaseReady {
		if !input.Optional {
			resultCollector.Messages = append(resultCollector.Messages, fmt.Sprintf("#%d: namedConnection '%s' not ready", idx, nsName.String()))
		}
		return nil
	}
	values := make(map[string]interface{})
	err = yaml.UnmarshalStrict(connection.Spec.Values.Raw, &values)
	if err != nil {
		return NewReconcileError(fmt.Errorf("input #%d: could not unmarshal connection '%s' values: %w", idx, nsName.String(), err), false, "")
	}
	resultCollector.InputModel[input.Alias] = values
	return nil
}

func bimHandleReleaseConnection(ctx context.Context, idx int, input kubopackage.InputRendered, helper BuildInputModelHelper, resultCollector *BuildInputModelResult) ReconcileError {
	connections, err := helper.FindOutputConnectionFromRelease(ctx, types.NamespacedName{Namespace: input.Namespace, Name: input.Release.Name})
	if err != nil {
		return err
	}
	electedConnections := make([]*kv1alpha1.Connection, 0, len(connections))
	possibleConnectionNames := make([]string, 0, len(connections))
	for _, connection := range connections {
		if connection.Spec.Interface != input.Interface {
			continue
		}
		if input.Release.OutputName != "" && connection.Spec.OutputName != input.Release.OutputName {
			continue
		}
		possibleConnectionNames = append(possibleConnectionNames, connection.Name)
		resultCollector.WatchedInputConnections = append(resultCollector.WatchedInputConnections, kv1alpha1.WatchedInputConnection{})
		if connection.Status.Phase == kv1alpha1.ConnectionPhaseReady {
			electedConnections = append(electedConnections, &connection)
		}
	}
	if len(electedConnections) == 0 {
		if !input.Optional {
			resultCollector.Messages = append(resultCollector.Messages, fmt.Sprintf("#%d: : %s", idx, strings.Join(possibleConnectionNames, ",")))
		}
		return nil
	}
	if !input.AllowMultiple {
		if len(possibleConnectionNames) > 1 {
			resultCollector.Messages = append(resultCollector.Messages, fmt.Sprintf("#%d: Too many possible connections: %s", idx, strings.Join(possibleConnectionNames, ",")))
			return nil
		}
		// len(electedConnections) == 1, by construction
		values, err := parseValue(idx, electedConnections[0])
		if err != nil {
			return err
		}
		resultCollector.InputModel[input.Alias] = values
		return nil
	}
	sort.Slice(electedConnections, func(i, j int) bool {
		if electedConnections[i].Spec.Priority == electedConnections[j].Spec.Priority {
			return electedConnections[i].Name < electedConnections[j].Name
		}
		return electedConnections[i].Spec.Priority < electedConnections[j].Spec.Priority
	})
	// Set the elected value (Higher priority)
	values, err := parseValue(idx, electedConnections[0])
	if err != nil {
		return err
	}
	resultCollector.InputModel[input.Alias] = values
	// And set the list of connectors
	inputList := make([]map[string]interface{}, 0, len(electedConnections))
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

func parseValue(idx int, conn *kv1alpha1.Connection) (map[string]interface{}, ReconcileError) {
	values := make(map[string]interface{})
	err := yaml.UnmarshalStrict(conn.Spec.Values.Raw, &values)
	if err != nil {
		return nil, NewReconcileError(fmt.Errorf("input #%d: could not unmarshal connection '%s' values: %w", idx, types.NamespacedName{Namespace: conn.Namespace, Name: conn.Name}.String(), err), false, "")
	}
	return values, nil
}
