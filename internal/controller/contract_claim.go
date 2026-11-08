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

package controller

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"kubocd/internal/kuboschema"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/interpreter"
	"github.com/xeipuuv/gojsonschema"
	"sigs.k8s.io/yaml"
)

// contractClaim is the claim section of a contract, compiled.
type contractClaim struct {
	schema   map[string]interface{}
	goSchema *gojsonschema.Schema
	env      *cel.Env
	checks   map[string]*cel.Ast
}

// resolveContractClaim compiles the claim section of contract. Without one, a claim can hold no requirement.
func resolveContractClaim(contract kv1alpha1.ContractFacade) (*contractClaim, error) {
	claim := contract.GetClaim()
	raw := []byte(`{ "properties": {} }`)
	checks := map[string]string{}
	if claim != nil {
		if claim.Requirements != nil {
			raw = claim.Requirements.Raw
		}
		checks = claim.Checks
	}
	sch := make(kuboschema.KuboSchema)
	if err := yaml.UnmarshalStrict(raw, &sch); err != nil {
		return nil, fmt.Errorf("claim: unable to parse the requirements schema: %w", err)
	}
	schema, err := kuboschema.Kubo2openAPI(sch, false)
	if err != nil {
		return nil, fmt.Errorf("claim: error on the requirements schema conversion: %w", err)
	}
	goSchema, err := gojsonschema.NewSchema(gojsonschema.NewGoLoader(schema))
	if err != nil {
		return nil, fmt.Errorf("claim: error on the requirements schema compilation: %w", err)
	}
	properties, _ := schema["properties"].(map[string]interface{})
	for _, name := range slices.Sorted(maps.Keys(properties)) {
		if _, ok := checks[name]; !ok {
			return nil, fmt.Errorf("claim: the requirement '%s' has no check", name)
		}
	}
	env, err := cel.NewEnv(cel.Variable("connection", cel.DynType), cel.Variable("claim", cel.DynType), versionSatisfiesFunction)
	if err != nil {
		return nil, fmt.Errorf("claim: unable to build the cel environment: %w", err) // Should not occur
	}
	compiled := make(map[string]*cel.Ast, len(checks))
	for _, name := range slices.Sorted(maps.Keys(checks)) {
		if _, ok := properties[name]; !ok {
			return nil, fmt.Errorf("claim: the check '%s' names no requirement of the schema", name)
		}
		ast, issues := env.Compile(checks[name])
		if issues != nil && issues.Err() != nil {
			return nil, fmt.Errorf("claim: the check '%s' is not valid: %w", name, issues.Err())
		}
		if !ast.OutputType().IsExactType(cel.BoolType) && !ast.OutputType().IsExactType(cel.DynType) {
			return nil, fmt.Errorf("claim: the check '%s' does not evaluate to a boolean", name)
		}
		compiled[name] = ast
	}
	return &contractClaim{schema: schema, goSchema: goSchema, env: env, checks: compiled}, nil
}

// unmetRequirement returns the first requirement, by name, that the connection values do not meet, or "".
// A requirement missing from requirements is not checked. An evaluation error means not met.
func (c *contractClaim) unmetRequirement(ctx context.Context, requirements, connection map[string]interface{}, limits ConditionLimits) (string, error) {
	if requirements == nil {
		requirements = map[string]interface{}{}
	}
	result, err := c.goSchema.Validate(gojsonschema.NewGoLoader(requirements))
	if err != nil {
		return "", errors.New("unable to check the requirements against the contract")
	}
	if !result.Valid() {
		return "", fmt.Errorf("invalid requirements: %s", kubopackage.SchemaError(result, c.schema, requirements))
	}
	for _, name := range slices.Sorted(maps.Keys(requirements)) {
		met, err := c.check(ctx, name, requirements, connection, limits)
		if err != nil {
			return "", err
		}
		if !met {
			return name, nil
		}
	}
	return "", nil
}

func (c *contractClaim) check(ctx context.Context, name string, requirements, connection map[string]interface{}, limits ConditionLimits) (bool, error) {
	ast, ok := c.checks[name]
	if !ok {
		return false, fmt.Errorf("the requirement '%s' has no check", name)
	}
	options := []cel.ProgramOption{cel.InterruptCheckFrequency(100)}
	if limits.CostLimit > 0 {
		options = append(options, cel.CostLimit(uint64(limits.CostLimit)))
	}
	program, err := c.env.Program(ast, options...)
	if err != nil {
		return false, fmt.Errorf("unable to build a program from the check '%s'", name)
	}
	evalCtx := ctx
	if limits.Timeout > 0 {
		var cancel context.CancelFunc
		evalCtx, cancel = context.WithTimeout(ctx, limits.Timeout)
		defer cancel()
	}
	value, _, err := program.ContextEval(evalCtx, map[string]interface{}{"connection": connection, "claim": requirements})
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if errors.Is(evalCtx.Err(), context.DeadlineExceeded) {
		return false, fmt.Errorf("the check '%s': CEL evaluation deadline exceeded", name)
	}
	var stopped interpreter.EvalCancelledError
	if errors.As(err, &stopped) && stopped.Cause == interpreter.CostLimitExceeded {
		return false, fmt.Errorf("the check '%s': CEL cost limit exceeded", name)
	}
	if err != nil {
		return false, nil
	}
	met, isBool := value.Value().(bool)
	return isBool && met, nil
}

var versionSatisfiesFunction = cel.Function("versionSatisfies",
	cel.Overload("versionSatisfies_string_string", []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType,
		cel.BinaryBinding(func(version, constraint ref.Val) ref.Val {
			v, vOk := version.Value().(string)
			c, cOk := constraint.Value().(string)
			if !vOk || !cOk {
				return types.NewErr("versionSatisfies expects two strings")
			}
			satisfied, err := versionSatisfies(v, c)
			if err != nil {
				return types.NewErr("%s", err.Error())
			}
			return types.Bool(satisfied)
		})))

type versionOperator struct {
	symbol string
	holds  func(int) bool
}

// The longer symbols first, as they start with the shorter ones
var versionOperators = []versionOperator{
	{">=", func(c int) bool { return c >= 0 }},
	{"<=", func(c int) bool { return c <= 0 }},
	{"!=", func(c int) bool { return c != 0 }},
	{">", func(c int) bool { return c > 0 }},
	{"<", func(c int) bool { return c < 0 }},
	{"=", func(c int) bool { return c == 0 }},
}

// versionSatisfies tells whether version meets constraint, a list of comparisons separated by commas
// (">=15, <18"). Versions are compared number by number, a missing number counting as 0.
func versionSatisfies(version, constraint string) (bool, error) {
	v, err := parseVersion(version)
	if err != nil {
		return false, err
	}
	for _, comparison := range strings.Split(constraint, ",") {
		comparison = strings.TrimSpace(comparison)
		idx := slices.IndexFunc(versionOperators, func(op versionOperator) bool {
			return strings.HasPrefix(comparison, op.symbol)
		})
		if idx < 0 {
			return false, fmt.Errorf("the comparison '%s' has no operator", comparison)
		}
		bound, err := parseVersion(strings.TrimPrefix(comparison, versionOperators[idx].symbol))
		if err != nil {
			return false, err
		}
		if !versionOperators[idx].holds(compareVersions(v, bound)) {
			return false, nil
		}
	}
	return true, nil
}

func parseVersion(version string) ([]uint64, error) {
	parts := strings.Split(strings.TrimSpace(version), ".")
	numbers := make([]uint64, len(parts))
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid version '%s'", version)
		}
		numbers[i] = n
	}
	return numbers, nil
}

func compareVersions(a, b []uint64) int {
	for i := 0; i < max(len(a), len(b)); i++ {
		var x, y uint64
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if c := cmp.Compare(x, y); c != 0 {
			return c
		}
	}
	return 0
}
