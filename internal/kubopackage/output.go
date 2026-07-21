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
	"kubocd/internal/tmpl"
)

type Output struct {
	// required:true
	Interface KcdTemplateString `json:"interface"`
	// optional. Default to interface
	Name KcdTemplateString `json:"Name"`
	// Connection or ClusterConnection. Default to Connection
	Kind KcdTemplateString `json:"Kind,omitempty"`
	// optional. Default to Name. For a potential front end
	DisplayName KcdTemplateString `json:"displayName,omitempty"`
	// Optional. Default to 100
	Priority KcdTemplateInt `json:"priority,omitempty"`
	// Optional
	Description KcdTemplateString `json:"description,omitempty"`
	// Optional. Default to true
	Enabled KcdTemplateBool `json:"enabled,omitempty"`
	// Optional. A connection without values can be used to mark dependencies
	Values KcdTemplateMap `json:"values,omitempty"`
	// ------------------------------- Private part
	templates *outputTemplates
}

type outputTemplates struct {
	iface       tmpl.Tmpl
	name        tmpl.Tmpl
	kind        tmpl.Tmpl
	displayName tmpl.Tmpl
	priority    tmpl.Tmpl
	description tmpl.Tmpl
	enabled     tmpl.Tmpl
	values      tmpl.Tmpl
}

func (o *Output) groom(pck *Package) error {
	if o.Interface == "" {
		return fmt.Errorf("'interface' is a required parameters")
	}
	//if o.Enabled == "" {
	//	o.Enabled = "true"
	//}
	//if o.Priority == "" {
	//	o.Priority = "100"
	//}
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
	o.templates.enabled, err = tmpl.New("", string(o.Enabled), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'disabled' parameter: %w", err)
	}
	o.templates.values, err = tmpl.NewFromAny("", o.Values, pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'values' parameter: %w", err)
	}
	return nil
}

// OutputRendered NB: This is yaml/json serializable for dump on render kubocd CLI command
type OutputRendered struct {
	Name        string                 `json:"name"`
	Interface   string                 `json:"interface"`
	Kind        string                 `json:"kind"`
	DisplayName string                 `json:"displayName,omitempty"`
	Priority    int                    `json:"priority,omitempty"`
	Description string                 `json:"description,omitempty"`
	Enabled     bool                   `json:"enabled"`
	Values      map[string]interface{} `json:"values,omitempty"`
}

func (o *Output) Render(model map[string]interface{}, defaultNamespace string) (*OutputRendered, error) {
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
	or.Kind, err = o.templates.kind.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'kind' parameter: %w", err)
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
	or.Enabled, _, err = o.templates.enabled.RenderToBool(model, true)
	if err != nil {
		return nil, fmt.Errorf("could not render 'enabled' parameter: %w", err)
	}
	or.Values, _, err = o.templates.values.RenderToMap(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'values' parameter: %w", err)
	}
	if or.Interface == "" {
		return nil, fmt.Errorf("'interface' is a required parameters")
	}
	if or.Name == "" {
		or.Name = or.Interface
	}
	if or.DisplayName == "" {
		or.DisplayName = or.Name
	}
	if or.Kind == "" {
		or.Kind = kv1alpha1.ConnectionKind
	}
	if or.Kind != kv1alpha1.ConnectionKind && or.Kind != kv1alpha1.ClusterConnectionKind {
		return nil, fmt.Errorf("'kind' Must be one of 'Connection' or 'ClusterConnection'")
	}
	return or, nil
}
