/*
Copyright 2025 Kubotal

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

package kubopackage

import (
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/global"
	"kubocd/internal/kuboschema"
	"kubocd/internal/misc"
	"kubocd/internal/tmpl"

	"sigs.k8s.io/yaml"
)

// KcdTemplateMap A template where expected result is a map[string]interface{}.
// Maybe a string or a map[string]interface{}
type KcdTemplateMap interface{}

// KcdTemplateBool A template where expected result is a boolean
type KcdTemplateBool string

// KcdTemplateString A template where expected result is a string
type KcdTemplateString string

// KcdTemplateDuration A template where expected result is a string
type KcdTemplateDuration string

// KcdTemplateStringList A template where expected result is a []string
// May be a string or a []string
type KcdTemplateStringList interface{}

// KcdTemplateObjectList A template where expected result is a []interface{}
// May be a string or a []string
type KcdTemplateObjectList interface{}

// KcdTemplateInt A template where expected result is an integer
type KcdTemplateInt string

// ------------------------------------------------

type PackageSchema struct {
	// Allow Release.spec.parameters validation. And provide default values
	Parameters kuboschema.KuboSchema `json:"parameters,omitempty"`
	// Allow context validation. And provide default values
	Context kuboschema.KuboSchema `json:"context,omitempty"`
}

// ------------------------------------------------

type Package struct {
	// required:true
	ApiVersion string `json:"apiVersion"` // v1alpha1
	// required:false.
	// Default: Package
	Type string `json:"type"` // Always 'Package'
	// required:true
	Name string `json:"name"`
	// required:true
	Tag string `json:"tag"`
	// Package description.
	Description KcdTemplateString `json:"description,omitempty"`
	// A template aimed to be rendered on deployment.
	// Intended to provide user with usage information // (Access link, configuration, ....)
	Usage map[string]KcdTemplateString `json:"usage,omitempty"`
	//Usage KcdTemplateString `json:"usage,omitempty"`
	// Prevent deletion
	// Default: false
	// Act as default for corresponding Release value
	Protected bool           `json:"protected"`
	Schema    *PackageSchema `json:"schema,omitempty"`
	// List of modules (HelmChart) included in the package
	// required: true
	Modules []*Module `json:"modules"`
	// List if role we provide
	Roles KcdTemplateStringList `json:"roles,omitempty"`
	// List of role we depend on
	Dependencies KcdTemplateStringList `json:"dependencies,omitempty"`
	// A template snippet which will be added at the beginning of all templates
	// Intended to be used to compute some global values
	TemplateHeader string `json:"templateHeader,omitempty"`
	// List of inputs referencing connections.
	Inputs KcdTemplateObjectList `json:"inputs,omitempty"`
	// List of conditions
	Conditions KcdTemplateObjectList `json:"conditions,omitempty"`
	// List of outputs, to generate connections
	Outputs KcdTemplateObjectList `json:"outputs,omitempty"`
	// ------------------- Private part
	templates *packageTemplates
}

type ChartRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

var noParamSchema = map[string]interface{}{
	"$schema":              "http://json-schema.org/draft-04/schema#",
	"type":                 "object",
	"properties":           map[string]interface{}{},
	"required":             []string{},
	"additionalProperties": false,
}

func (pck *Package) Groom(configSore configstore.ConfigStore) error {
	// ------------------------ Normalize
	if pck.Roles == nil {
		pck.Roles = []string{}
	}
	if pck.Dependencies == nil {
		pck.Dependencies = []string{}
	}
	// Validation
	if pck.ApiVersion != global.PackageApiVersion {
		return fmt.Errorf("'apiVersion' must be %s (is '%s')", global.PackageApiVersion, pck.ApiVersion)
	}
	if pck.Type == "" {
		pck.Type = global.PackageType
	}
	if pck.Type != global.PackageType {
		return fmt.Errorf("'type' must be %s", global.PackageType)
	}
	x := misc.CountNonZero(pck.Name, pck.Tag)
	if x != 2 {
		return fmt.Errorf("'name' and 'tag' must be set")
	}
	if !misc.ValidateK8sName(pck.Name) {
		return fmt.Errorf("invalid 'name'. Must contain only alphanumeric characters, dashes and underscores")
	}
	var err error
	if pck.Schema == nil {
		pck.Schema = &PackageSchema{}
	}
	if pck.Schema.Parameters != nil {
		pck.Schema.Parameters, err = kuboschema.Kubo2openAPI(pck.Schema.Parameters, false)
		if err != nil {
			return fmt.Errorf("invalid 'schema.parameters': %w", err)
		}
	} else {
		pck.Schema.Parameters = noParamSchema
	}

	if pck.Schema.Context != nil {
		pck.Schema.Context, err = kuboschema.Kubo2openAPI(pck.Schema.Context, true)
		if err != nil {
			return fmt.Errorf("invalid 'schema.context': %w", err)
		}
	}
	if pck.Modules == nil || len(pck.Modules) == 0 {
		return fmt.Errorf("a package must have at least one module")
	}
	// ------- Now, check modules
	moduleByName := make(map[string]*Module)
	for idx := range pck.Modules {
		module := pck.Modules[idx]
		err := pck.Modules[idx].groom(pck, idx, configSore)
		if err != nil {
			return fmt.Errorf("module '%s': %w", pck.Modules[idx].Name, err)
		}
		_, ok := moduleByName[module.Name]
		if ok {
			return fmt.Errorf("duplicate module name: %s", module.Name)
		}
		moduleByName[module.Name] = pck.Modules[idx]
	}
	pck.templates = &packageTemplates{}
	pck.templates.description, err = tmpl.New("", string(pck.Description), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'description' template: %w", err)
	}
	pck.templates.roles, err = tmpl.NewFromAny("", pck.Roles, pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'roles' template: %w", err)
	}
	pck.templates.dependencies, err = tmpl.NewFromAny("", pck.Dependencies, pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'dependencies' template: %w", err)
	}
	pck.templates.usage = make(map[string]tmpl.Tmpl)
	for key, usage := range pck.Usage {
		pck.templates.usage[key], err = tmpl.New("", string(usage), pck.TemplateHeader)
		if err != nil {
			return fmt.Errorf("could not parse 'usage[%s]' template: %w", key, err)
		}
	}

	pck.templates.inputs, err = tmpl.NewFromAny("", pck.Inputs, pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'inputs' template: %w", err)
	}

	pck.templates.outputs, err = tmpl.NewFromAny("", pck.Outputs, pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'outputs' template: %w", err)
	}

	pck.templates.conditions, err = tmpl.NewFromAny("", pck.Conditions, pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'inputs' template: %w", err)
	}

	// NB We can't test intra-module dependencies here, as it is a template. Will be checked after rendering
	return nil
}

type packageTemplates struct {
	usage        map[string]tmpl.Tmpl
	roles        tmpl.Tmpl
	dependencies tmpl.Tmpl
	description  tmpl.Tmpl
	outputs      tmpl.Tmpl
	inputs       tmpl.Tmpl
	conditions   tmpl.Tmpl
}

// Rendered object is a proxy for a release of a package.
// Aim is to concentrate all error detection in its constructor
// Standard way should be to have Getters on package and module object.
// But each getter may generate an error, thus complicate the code.
type Rendered struct {
	Usage                map[string]string
	Roles                []string
	Dependencies         []string
	ModuleRenderedByName map[string]*ModuleRendered
	Description          string
	Inputs               []*InputRendered // Warning: Lifecycle is different. Computed in advance
	Conditions           []*ConditionRendered
	Outputs              []*OutputRendered
}

// OutputRendered NB: This is yaml/json serializable for dump on render kubocd CLI command
type OutputRendered struct {
	Name        string                 `json:"name"`
	Contract    string                 `json:"contract"`
	Kind        kv1alpha1.Kind         `json:"kind"`
	DisplayName string                 `json:"displayName,omitempty"`
	Priority    int                    `json:"priority,omitempty"`
	Description string                 `json:"description,omitempty"`
	Values      map[string]interface{} `json:"values,omitempty"`
}

// InputRendered NB: This is yaml/json serializable for dump on render kubocd CLI command
type InputRendered struct {
	Contract      string         `json:"contract"`
	Kind          kv1alpha1.Kind `json:"kind,omitempty"`
	ConnectionRef struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"connectionRef"`
	ReleaseRef struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Output    string `json:"output,omitempty"`
	} `json:"releaseRef"`
	Alias         string `json:"alias"`
	Optional      bool   `json:"optional"`
	AllowMultiple bool   `json:"allowMultiple"`
	// Private field
	contractLookupNamespace string
}

func (ir *InputRendered) GetContractLookupNamespace() string {
	return ir.contractLookupNamespace
}

type ConditionRendered struct {
	Group     string         `json:"group"`
	Kind      kv1alpha1.Kind `json:"kind"`
	Name      string         `json:"name"`
	Namespace string         `json:"namespace"`
	Cel       string         `json:"cel"`
}

func (pck *Package) Render(model map[string]interface{}, release *kv1alpha1.Release) (*Rendered, error) {
	r := &Rendered{
		ModuleRenderedByName: make(map[string]*ModuleRendered),
	}
	var err error
	r.Usage = make(map[string]string)
	for key, template := range pck.templates.usage {
		r.Usage[key], err = template.RenderToText(model)
		if err != nil {
			return nil, fmt.Errorf("could not render 'usage[%s]' template: %w", key, err)
		}
	}
	var txt string
	r.Roles, txt, err = pck.templates.roles.RenderToStringList(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'roles' template: %w (%s)", err, txt)
	}
	r.Dependencies, txt, err = pck.templates.dependencies.RenderToStringList(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'dependencies' template: %w (%s)", err, txt)
	}
	r.Description, err = pck.templates.description.RenderToText(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'description' template: %w", err)
	}
	for _, module := range pck.Modules {
		//fmt.Printf("*********************** module.name %s\n", module.Name)
		r.ModuleRenderedByName[module.Name], err = module.Render(model)
		if err != nil {
			return nil, fmt.Errorf("module '%s': %w", module.Name, err)
		}
	}
	// Render intra module dependencies
	for _, module := range pck.Modules {
		rendered, ok := r.ModuleRenderedByName[module.Name]
		if !ok {
			panic(fmt.Sprintf("missing module of name %s", module.Name)) // Should not occur
		}
		for _, dep := range rendered.DependsOn {
			_, ok = r.ModuleRenderedByName[dep]
			if !ok {
				return nil, fmt.Errorf("module '%s': dependsOn '%s': module does not exists", module.Name, dep)
			}
		}
	}
	// ---------------------------- Render outputs
	txt, err = pck.templates.outputs.RenderToText(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'outputs' template: %w", err)
	}
	a := make([]*OutputRendered, 0)
	err = yaml.UnmarshalStrict([]byte(txt), &a)
	if err != nil {
		return nil, fmt.Errorf("could not parse 'outputs' template: %w", err)
	}
	r.Outputs = a
	// ------- Adjust each output
	for idx, ro := range r.Outputs {
		// WARNING: This works for []*OutputRendered. []OutputRendered will be bogus.
		if ro.Contract == "" {
			return nil, fmt.Errorf("output[%d]: 'contract' is a required parameters", idx)
		}
		if ro.Name == "" {
			ro.Name = ro.Contract
		}
		if ro.DisplayName == "" {
			ro.DisplayName = ro.Name
		}
		if ro.Kind == "" {
			ro.Kind = kv1alpha1.KindConnection
		}
		if ro.Kind != kv1alpha1.KindConnection && ro.Kind != kv1alpha1.KindClusterConnection {
			return nil, fmt.Errorf("output[%d]: 'kind' Must be one of 'Connection' or 'ClusterConnection'", idx)
		}
		if ro.Priority == 0 {
			ro.Priority = 100
		}
	}

	// --------------- Must ensure output name are uniques
	dupDetect := make(map[string]struct{})
	for _, output := range r.Outputs {
		if _, ok := dupDetect[output.Name]; ok {
			return nil, fmt.Errorf("duplicate output name '%s'", output.Name)
		}
		dupDetect[output.Name] = struct{}{}
	}

	// ------------------------- Render conditions
	txt, err = pck.templates.conditions.RenderToText(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'conditions' template: %w", err)
	}
	conditions := make([]*ConditionRendered, 0)
	err = yaml.UnmarshalStrict([]byte(txt), &conditions)
	if err != nil {
		return nil, fmt.Errorf("could not parse 'conditions' template: %w", err)
	}
	r.Conditions = conditions

	for idx, condition := range r.Conditions {
		if condition.Kind == "" {
			return nil, fmt.Errorf("condition[%d]: kind is a required parameters", idx)
		}
		if condition.Name == "" {
			return nil, fmt.Errorf("condition[%d]: name is a required parameters", idx)
		}
		if condition.Namespace == "" {
			condition.Namespace = release.Spec.TargetNamespace
		}
	}

	return r, nil
}

func (pck *Package) RenderInputs(model map[string]interface{}, defaultNamespace string) ([]*InputRendered, error) {
	txt, err := pck.templates.inputs.RenderToText(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'inputs' template: %w", err)
	}
	result := make([]*InputRendered, 0)
	err = yaml.UnmarshalStrict([]byte(txt), &result)
	if err != nil {
		return nil, fmt.Errorf("could not parse 'inputs' template: %w", err)
	}

	//fmt.Printf("***************************************** Rendering %d inputs\n%v\n", len(result), result)

	for idx, ir := range result {
		if ir.Alias == "" {
			ir.Alias = ir.Contract
		}
		if ir.Contract == "" {
			return nil, fmt.Errorf("input[%d]: contract is required", idx)
		}
		if ir.Kind != "" && ir.Kind != kv1alpha1.KindConnection && ir.Kind != kv1alpha1.KindClusterConnection {
			return nil, fmt.Errorf("input[%d]: invalid kind '%s' value ", idx, ir.Kind)
		}
		if ir.ConnectionRef.Name != "" {
			if ir.ConnectionRef.Namespace == "" {
				if ir.Kind == "" || ir.Kind == kv1alpha1.KindConnection {
					ir.ConnectionRef.Namespace = defaultNamespace
				} // else "" is ok
			} else {
				if ir.Kind == kv1alpha1.KindClusterConnection {
					return nil, fmt.Errorf("input[%d]: connectionRef.namespace must be empty if kind is ClusterConnection", idx)
				}
			}
		}
		if ir.ReleaseRef.Name != "" && ir.ReleaseRef.Namespace == "" {
			ir.ReleaseRef.Namespace = defaultNamespace
		}

		x := misc.CountNonZero(ir.ConnectionRef.Name, ir.ReleaseRef.Name)
		if x > 1 {
			return nil, fmt.Errorf("input[%d]: 0 or one of 'connectionRef.name' or 'releaseRef.name' sub element may be specified", idx)
		}
		if x == 0 {
			// No name specified. Search will fallback by contract on specified or default namespace
			x := misc.CountNonZero(ir.ConnectionRef.Namespace, ir.ReleaseRef.Namespace)
			if x > 1 {
				return nil, fmt.Errorf("input[%d]: only one of 'connectionRef.namespace' or 'releaseRef.namespace' sub element may be specified", idx)
			}
			if x == 0 {
				ir.contractLookupNamespace = defaultNamespace
			} else {
				// x == 1
				if ir.ConnectionRef.Namespace != "" {
					ir.contractLookupNamespace = ir.ConnectionRef.Namespace
				} else {
					ir.contractLookupNamespace = ir.ReleaseRef.Namespace
				}
			}
		}
	}
	return result, nil
}

//func (pck *Package) RenderOutputs(model map[string]interface{}) ([]OutputRendered, error) {
//	result := make([]OutputRendered, len(pck.Outputs))
//	for idx, output := range pck.Outputs {
//		or, err := output.Render(model)
//		if err != nil {
//			return nil, fmt.Errorf("could not render 'output[%d]': %w", idx, err)
//		}
//		result[idx] = *or
//	}
//	// Must ensure name are uniques
//	dupDetect := make(map[string]struct{})
//	for _, outp := range result {
//		if _, ok := dupDetect[outp.Name]; ok {
//			return nil, fmt.Errorf("duplicate output name '%s'", outp.Name)
//		}
//		dupDetect[outp.Name] = struct{}{}
//	}
//	return result, nil
//}
