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
	"bytes"
	"encoding/json"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/tmpl"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"
)

type Output struct {
	// required:true
	Interface KcdTemplateString `json:"interface"`
	// optional. Default to interface
	Name KcdTemplateString `json:"name"`
	// Connection or ClusterConnection. Default to Connection
	Kind KcdTemplateString `json:"kind,omitempty"`
	// optional. Explicit name of the created (Cluster)Connection. When unset,
	// the name is generated (kcd-<release>-<output>). An explicit name gives a
	// stable platform reference, independent of the producer release name.
	ConnectionName KcdTemplateString `json:"connectionName,omitempty"`
	// optional. Default to Name. For a potential front end
	DisplayName KcdTemplateString `json:"displayName,omitempty"`
	// Optional. Default to 100
	Priority KcdTemplateInt `json:"priority,omitempty"`
	// Optional
	Description KcdTemplateString `json:"description,omitempty"`
	// Optional. Default to false
	Disabled KcdTemplateBool `json:"disabled,omitempty"`
	// Optional. Labels set on the created (Cluster)Connection, for operators
	// and tooling ('kubectl get connections -l backup=enabled')
	Labels KcdTemplateMap `json:"labels,omitempty"`
	// Optional. A connection without values can be used to mark dependencies
	Values KcdTemplateMap `json:"values,omitempty"`
	// ------------------------------- Private part
	templates *outputTemplates
}

type outputTemplates struct {
	iface          tmpl.Tmpl
	name           tmpl.Tmpl
	kind           tmpl.Tmpl
	connectionName tmpl.Tmpl
	displayName    tmpl.Tmpl
	priority       tmpl.Tmpl
	description    tmpl.Tmpl
	disabled       tmpl.Tmpl
	labels         tmpl.Tmpl
	values         tmpl.Tmpl
}

func (o *Output) groom(pck *Package) error {
	if o.Interface == "" {
		return fmt.Errorf("'interface' is a required parameters")
	}
	o.templates = &outputTemplates{}
	var err error
	// ------- Now, handle templates
	o.templates.iface, err = tmpl.New("", string(o.Interface), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'interface' parameter: %w", err)
	}
	o.templates.name, err = tmpl.New("", string(o.Name), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'name' parameter: %w", err)
	}
	o.templates.kind, err = tmpl.New("", string(o.Kind), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'kind' parameter: %w", err)
	}
	o.templates.connectionName, err = tmpl.New("", string(o.ConnectionName), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'connectionName' parameter: %w", err)
	}
	o.templates.displayName, err = tmpl.New("", string(o.DisplayName), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'displayName' parameter: %w", err)
	}
	o.templates.priority, err = tmpl.New("", string(o.Priority), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'priority' parameter: %w", err)
	}
	o.templates.description, err = tmpl.New("", string(o.Description), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'description' parameter: %w", err)
	}
	o.templates.disabled, err = tmpl.New("", string(o.Disabled), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'disabled' parameter: %w", err)
	}
	o.templates.labels, err = tmpl.NewFromAny("", o.Labels, pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'labels' parameter: %w", err)
	}
	o.templates.values, err = tmpl.NewFromAny("", o.Values, pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'values' parameter: %w", err)
	}
	return nil
}

// OutputRendered NB: This is yaml/json serializable for dump on render kubocd CLI command
type OutputRendered struct {
	Name           string                 `json:"name"`
	Interface      string                 `json:"interface"`
	Kind           kv1alpha1.Kind         `json:"kind"`
	ConnectionName string                 `json:"connectionName,omitempty"`
	DisplayName    string                 `json:"displayName,omitempty"`
	Priority       int                    `json:"priority,omitempty"`
	Description    string                 `json:"description,omitempty"`
	Disabled       bool                   `json:"disabled,omitempty"`
	Labels         map[string]string      `json:"labels,omitempty"`
	Values         map[string]interface{} `json:"values,omitempty"`
}

// finalize applies the defaults and validates the rendered output, whatever
// the form (per-field templates or block-templated stanza) it came from.
func (or *OutputRendered) finalize() error {
	if or.Interface == "" {
		return fmt.Errorf("'interface' is a required parameters")
	}
	if or.Name == "" {
		or.Name = or.Interface
	}
	if or.DisplayName == "" {
		or.DisplayName = or.Name
	}
	if or.Kind == "" {
		or.Kind = kv1alpha1.KindConnection
	}
	if or.Kind != kv1alpha1.KindConnection && or.Kind != kv1alpha1.KindClusterConnection {
		return fmt.Errorf("'kind' Must be one of 'Connection' or 'ClusterConnection'")
	}
	for k, v := range or.Labels {
		if errs := validation.IsQualifiedName(k); len(errs) > 0 {
			return fmt.Errorf("invalid label key '%s': %s", k, strings.Join(errs, ", "))
		}
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			return fmt.Errorf("invalid value '%s' for label '%s': %s", v, k, strings.Join(errs, ", "))
		}
	}
	return nil
}

func (o *Output) Render(model map[string]interface{}) (*OutputRendered, error) {
	or := &OutputRendered{}
	var err error
	or.Interface, err = o.templates.iface.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'interface' parameter: %w", err)
	}
	or.Name, err = o.templates.name.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'name' parameter: %w", err)
	}
	k, err := o.templates.kind.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'kind' parameter: %w", err)
	}
	or.Kind = kv1alpha1.Kind(k)
	or.ConnectionName, err = o.templates.connectionName.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'connectionName' parameter: %w", err)
	}
	or.DisplayName, err = o.templates.displayName.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'displayName' parameter: %w", err)
	}
	or.Priority, _, err = o.templates.priority.RenderToInt(model, 100)
	if err != nil {
		return nil, fmt.Errorf("could not render 'priority' parameter: %w", err)
	}
	or.Description, err = o.templates.description.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'description' parameter: %w", err)
	}
	or.Disabled, _, err = o.templates.disabled.RenderToBool(model, false)
	if err != nil {
		return nil, fmt.Errorf("could not render 'enabled' parameter: %w", err)
	}
	labelsAny, _, err := o.templates.labels.RenderToMap(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'labels' parameter: %w", err)
	}
	or.Labels, err = labelsMap(labelsAny)
	if err != nil {
		return nil, err
	}
	or.Values, _, err = o.templates.values.RenderToMap(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'values' parameter: %w", err)
	}
	if err := or.finalize(); err != nil {
		return nil, err
	}
	return or, nil
}

// labelsMap converts a rendered labels map into map[string]string, rejecting
// non-string values.
func labelsMap(src map[string]interface{}) (map[string]string, error) {
	if len(src) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(src))
	for k, v := range src {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("label '%s': value must be a string, got %T", k, v)
		}
		result[k] = s
	}
	return result, nil
}

// OutputsStanza is the 'outputs:' package stanza. Two forms: a plain list of
// outputs (each field individually templated), or a single templated string
// rendering to a list, to produce N outputs from a parameter.
type OutputsStanza struct {
	List     []Output `json:"-"`
	Template string   `json:"-"`
	// ------------------------------- Private part
	template tmpl.Tmpl
}

func (os *OutputsStanza) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		os.Template = s
		return nil
	}
	// Every package load path is strict (yaml.UnmarshalStrict), but strictness
	// does not propagate into a custom unmarshaler: enforce it here, so a typo
	// in an output field keeps being rejected
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(&os.List)
}

// IsZero makes 'outputs,omitzero' omit an absent stanza in the serialized
// package (original.yaml, manifest.json), as the []Output shape did.
func (os OutputsStanza) IsZero() bool {
	return os.Template == "" && len(os.List) == 0
}

func (os OutputsStanza) MarshalJSON() ([]byte, error) {
	if os.Template != "" {
		return json.Marshal(os.Template)
	}
	return json.Marshal(os.List)
}

func (os *OutputsStanza) groom(pck *Package) error {
	if os.Template != "" {
		var err error
		os.template, err = tmpl.New("", os.Template, pck.TemplateHeader)
		if err != nil {
			return fmt.Errorf("could not parse the outputs block template: %w", err)
		}
		return nil
	}
	for idx := range os.List {
		if err := os.List[idx].groom(pck); err != nil {
			return fmt.Errorf("error on 'outputs[%d]': %w", idx, err)
		}
	}
	return nil
}

// outputLiteral is the shape of one entry of a block-templated outputs
// stanza, after rendering: every field carries its final value.
type outputLiteral struct {
	Interface      string                 `json:"interface"`
	Name           string                 `json:"name"`
	Kind           string                 `json:"kind"`
	ConnectionName string                 `json:"connectionName"`
	DisplayName    string                 `json:"displayName"`
	Priority       *int                   `json:"priority"`
	Description    string                 `json:"description"`
	Disabled       bool                   `json:"disabled"`
	Labels         map[string]string      `json:"labels"`
	Values         map[string]interface{} `json:"values"`
}

func (os *OutputsStanza) Render(model map[string]interface{}) ([]*OutputRendered, error) {
	if os.Template != "" {
		text, err := os.template.RenderToText(model)
		if err != nil {
			return nil, fmt.Errorf("could not render the outputs block template: %w", err)
		}
		var literals []outputLiteral
		if strings.TrimSpace(text) != "" {
			if err := yaml.UnmarshalStrict([]byte(text), &literals); err != nil {
				return nil, fmt.Errorf("the outputs block template must render to a list of outputs: %w", err)
			}
		}
		result := make([]*OutputRendered, len(literals))
		for idx, lit := range literals {
			or := &OutputRendered{
				Name:           lit.Name,
				Interface:      lit.Interface,
				Kind:           kv1alpha1.Kind(lit.Kind),
				ConnectionName: lit.ConnectionName,
				DisplayName:    lit.DisplayName,
				Priority:       100,
				Description:    lit.Description,
				Disabled:       lit.Disabled,
				Labels:         lit.Labels,
				Values:         lit.Values,
			}
			if lit.Priority != nil {
				or.Priority = *lit.Priority
			}
			if err := or.finalize(); err != nil {
				return nil, fmt.Errorf("outputs[%d]: %w", idx, err)
			}
			result[idx] = or
		}
		return result, nil
	}
	result := make([]*OutputRendered, len(os.List))
	for idx := range os.List {
		or, err := os.List[idx].Render(model)
		if err != nil {
			return nil, fmt.Errorf("could not render 'output[%d]': %w", idx, err)
		}
		result[idx] = or
	}
	return result, nil
}
