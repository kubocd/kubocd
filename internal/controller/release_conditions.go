package controller

import (
	"context"
	"errors"
	"fmt"
	"kubocd/internal/kubopackage"
	"strings"

	"github.com/google/cel-go/cel"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

/*
Conditions read only core Secrets or ConfigMaps (case-insensitive kinds) in the Release target namespace,
which defaults to its metadata namespace. Package rendering validates every condition's scope.
An object must exist and its optional CEL expression must be true. Reads need get permission:
the chart grants Secret/ConfigMap access only with the replications gate; render uses its caller's rights.
*/

// CheckCondition This function is intended to be called both the Release controller and the 'render' CLI subcommand
func CheckCondition(ctx context.Context, k8sClient client.Client, condition *kubopackage.ConditionRendered) (ok bool, message string, err ReconcileError) {
	kind, policyErr := condition.CoreKind()
	if policyErr != nil {
		return false, "", NewReconcileError(policyErr, true, "ConditionError")
	}
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: string(kind)})
	objectKey := client.ObjectKey{Namespace: condition.Namespace, Name: condition.Name}
	// Keep this read unstructured so conditions do not start cluster-wide Secret or ConfigMap informers.
	gErr := k8sClient.Get(ctx, objectKey, object)
	if gErr != nil {
		if k8serrors.IsNotFound(gErr) {
			return false, fmt.Sprintf("%s: object does not exists", conditionRef(condition)), nil
		}
		if k8serrors.IsForbidden(gErr) {
			return false, "", NewReconcileError(errors.New(
				"condition read forbidden: grant get access to the condition kind in the Release target namespace"), false, "ConditionError")
		}
		return false, "", NewReconcileError(fmt.Errorf("unable to fetch the object: %s", apiError(gErr)), false, "ConditionError")
	}
	if strings.TrimSpace(condition.Cel) == "" {
		// Existence of the object was the only requirement
		return true, "", nil
	}
	return evalConditionCel(condition, object.Object)
}

// conditionWithheld stands for a message of cel-go,
// which can quote the condition, rendered from data, or a value of the object.
const conditionWithheld = "message withheld, as it can quote the condition or the object"

// evalConditionCel evaluates the CEL expression against the object, each of its root attributes
// (apiVersion, kind, metadata, spec, status, data, ...) being exposed as a variable.
// Its messages name the object, never the expression nor the message of cel-go.
func evalConditionCel(condition *kubopackage.ConditionRendered, object map[string]interface{}) (ok bool, message string, err ReconcileError) {
	envOptions := make([]cel.EnvOption, 0, len(object))
	for attribute := range object {
		envOptions = append(envOptions, cel.Variable(attribute, cel.DynType))
	}
	env, cErr := cel.NewEnv(envOptions...)
	if cErr != nil {
		return false, "", NewReconcileError(fmt.Errorf("unable to build the cel environment, %s", conditionWithheld), false, "ConditionError") // Should not occur
	}
	ast, issues := env.Parse(condition.Cel)
	if issues != nil && issues.Err() != nil {
		// A broken expression will not fix itself. This is a package definition error.
		return false, "", NewReconcileError(fmt.Errorf("the cel expression is not valid, %s", conditionWithheld), true, "ConditionError")
	}
	ast, issues = env.Check(ast)
	if issues != nil && issues.Err() != nil {
		// Most of the time, the expression refers to an attribute not (yet) set on the object.
		return false, fmt.Sprintf("%s: unable to check the cel expression against this object, %s", conditionRef(condition), conditionWithheld), nil
	}
	program, cErr := env.Program(ast)
	if cErr != nil {
		return false, "", NewReconcileError(fmt.Errorf("unable to build a program from the cel expression, %s", conditionWithheld), true, "ConditionError")
	}
	value, _, cErr := program.Eval(object)
	if cErr != nil {
		// Typically a missing sub-attribute (i.e 'status.phase' while status is set, but empty)
		return false, fmt.Sprintf("%s: unable to evaluate the cel expression on this object, %s", conditionRef(condition), conditionWithheld), nil
	}
	result, isBool := value.Value().(bool)
	if !isBool {
		return false, "", NewReconcileError(errors.New("the cel expression does not evaluate to a boolean"), true, "ConditionError")
	}
	if !result {
		return false, fmt.Sprintf("%s: the cel expression is false", conditionRef(condition)), nil
	}
	return true, "", nil
}

// conditionRef build a human-readable reference of the object targeted by the condition
func conditionRef(condition *kubopackage.ConditionRendered) string {
	kind := string(condition.Kind)
	if condition.Group != "" {
		kind = fmt.Sprintf("%s.%s", kind, condition.Group)
	}
	if condition.Namespace != "" {
		return fmt.Sprintf("%s '%s/%s'", kind, condition.Namespace, condition.Name)
	}
	return fmt.Sprintf("%s '%s'", kind, condition.Name)
}
