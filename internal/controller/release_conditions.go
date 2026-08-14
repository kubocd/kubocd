package controller

import (
	"context"
	"fmt"
	"kubocd/internal/kubopackage"
	"strings"

	"github.com/google/cel-go/cel"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

/*
	A condition if fulfill if:
	- The referenced object (group, kind and namespace:name) exists
 	- If CEL is defined, this Common Expression must be true, applied on the main object

	If !ok, the message must contains a human-readable explanation of the error

	Some examples:
	- kind: Secret
      name: myapp-secret
	- kind: Secret
      name: myapp-secret2
	  cel: "'password' in data"		# data.password exists
	- group: kubocd.kubotal.io
      kind: Connection
      name: cnpg-db
      namespace: app1
      cel: "status.phase == 'READY'"
      # No namespace, as resources is cluster scoped.
	- group: kubocd.kubotal.io
      kind: ClusterConnection
      name: cert-manager
      cel: "status.phase == 'READY'"
*/

// CheckCondition This function is intended to be called both the Release controller and the 'render' CLI subcommand
func CheckCondition(ctx context.Context, k8sClient client.Client, condition *kubopackage.ConditionRendered) (ok bool, message string, err ReconcileError) {
	// The condition does not carry any version. So, lookup for the preferred one on the cluster.
	mapping, mErr := k8sClient.RESTMapper().RESTMapping(schema.GroupKind{
		Group: condition.Group,
		Kind:  string(condition.Kind),
	})
	if mErr != nil {
		if meta.IsNoMatchError(mErr) {
			// The CRD may be deployed later. So, this is not an error, just an unfulfilled condition.
			return false, fmt.Sprintf("%s: this kind is not defined on this cluster", conditionRef(condition)), nil
		}
		return false, "", NewReconcileError(fmt.Errorf("unable to lookup for kind '%s': %w", conditionRef(condition), mErr), false, "ConditionError")
	}
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(mapping.GroupVersionKind)
	objectKey := client.ObjectKey{
		Namespace: condition.Namespace,
		Name:      condition.Name,
	}
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		// The namespace has been defaulted to the release target namespace by the rendering. Just drop it.
		objectKey.Namespace = ""
	}
	gErr := k8sClient.Get(ctx, objectKey, object)
	if gErr != nil {
		if k8serrors.IsNotFound(gErr) {
			return false, fmt.Sprintf("%s: object does not exists", conditionRef(condition)), nil
		}
		return false, "", NewReconcileError(fmt.Errorf("unable to fetch %s: %w", conditionRef(condition), gErr), false, "ConditionError")
	}
	if strings.TrimSpace(condition.Cel) == "" {
		// Existence of the object was the only requirement
		return true, "", nil
	}
	return evalConditionCel(condition, object.Object)
}

// evalConditionCel evaluates the CEL expression against the object, each of its root attributes
// (apiVersion, kind, metadata, spec, status, data, ...) being exposed as a variable.
func evalConditionCel(condition *kubopackage.ConditionRendered, object map[string]interface{}) (ok bool, message string, err ReconcileError) {
	envOptions := make([]cel.EnvOption, 0, len(object))
	for attribute := range object {
		envOptions = append(envOptions, cel.Variable(attribute, cel.DynType))
	}
	env, cErr := cel.NewEnv(envOptions...)
	if cErr != nil {
		return false, "", NewReconcileError(fmt.Errorf("%s: unable to build the cel environment: %w", conditionRef(condition), cErr), false, "ConditionError") // Should not occur
	}
	ast, issues := env.Parse(condition.Cel)
	if issues != nil && issues.Err() != nil {
		// A broken expression will not fix itself. This is a package definition error.
		return false, "", NewReconcileError(fmt.Errorf("%s: invalid cel expression '%s': %w", conditionRef(condition), condition.Cel, issues.Err()), true, "ConditionError")
	}
	ast, issues = env.Check(ast)
	if issues != nil && issues.Err() != nil {
		// Most of the time, the expression refers to an attribute not (yet) set on the object.
		// Only keep the first line of the cel report, as the following ones are just a graphical location of the problem.
		return false, fmt.Sprintf("%s: unable to evaluate cel expression '%s' on this object: %s", conditionRef(condition), condition.Cel, firstLine(issues.Err().Error())), nil
	}
	program, cErr := env.Program(ast)
	if cErr != nil {
		return false, "", NewReconcileError(fmt.Errorf("%s: unable to build a program from cel expression '%s': %w", conditionRef(condition), condition.Cel, cErr), true, "ConditionError")
	}
	value, _, cErr := program.Eval(object)
	if cErr != nil {
		// Typically a missing sub-attribute (i.e 'status.phase' while status is set, but empty)
		return false, fmt.Sprintf("%s: unable to evaluate cel expression '%s' on this object: %s", conditionRef(condition), condition.Cel, cErr), nil
	}
	result, isBool := value.Value().(bool)
	if !isBool {
		return false, "", NewReconcileError(fmt.Errorf("%s: cel expression '%s' does not evaluate to a boolean", conditionRef(condition), condition.Cel), true, "ConditionError")
	}
	if !result {
		return false, fmt.Sprintf("%s: cel expression '%s' is false", conditionRef(condition), condition.Cel), nil
	}
	return true, "", nil
}

func firstLine(txt string) string {
	return strings.TrimSpace(strings.SplitN(txt, "\n", 2)[0])
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
