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
	"kubocd/internal/tmpl"
)

type Kind string

const KindConnection = Kind("Connection")
const KindClusterConnection = Kind("ClusterConnection")

type Input struct {
	// required:true
	Interface KcdTemplateString `json:"interface"`
	// Connection or ClusterConnection. If empty, both are looked up.
	Kind KcdTemplateString `json:"kind,omitempty"`
	// If kind == Connection. For UnmanagedConnection or Release lookup. Default to release namespace
	Namespace           KcdTemplateString `json:"namespace,omitempty"`
	UnmanagedConnection struct {
		// Must be "" if Relesae.name != nil
		Name KcdTemplateString `json:"name,omitempty"`
	} `json:"unmanagedConnection,omitempty"`
	Release struct {
		// Must be "" if UnmanagedConnection.name != nil
		Name KcdTemplateString `json:"name,omitempty"`
		// Optional. Used if the release manage several connection with the same interface
		OutputName KcdTemplateString `json:"outputName,omitempty"`
	} `json:"release,omitempty"`
	// default to interface
	Alias KcdTemplateString `json:"alias,omitempty"`
	// ------------------------------- Private part
	templates *inputTemplates
}

type inputTemplates struct {
	iface               tmpl.Tmpl
	kind                tmpl.Tmpl
	namespace           tmpl.Tmpl
	unmanagedConnection struct {
		name tmpl.Tmpl
	}
	release struct {
		name       tmpl.Tmpl
		outputName tmpl.Tmpl
	}
	alias tmpl.Tmpl
}

func (i *Input) groom(pck *Package) error {
	// ---------------- Now, handle templates
	i.templates = &inputTemplates{}
	var err error
	i.templates.iface, err = tmpl.New("", string(i.Interface), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'interface' parameter: %w", err)
	}
	i.templates.kind, err = tmpl.New("", string(i.Kind), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'kind' parameter: %w", err)
	}
	i.templates.namespace, err = tmpl.New("", string(i.Namespace), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'namespace' parameter: %w", err)
	}
	i.templates.unmanagedConnection.name, err = tmpl.New("", string(i.UnmanagedConnection.Name), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'unmanagedConnection.name' parameter: %w", err)
	}
	i.templates.release.name, err = tmpl.New("", string(i.Release.Name), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'release.name' parameter: %w", err)
	}
	i.templates.release.outputName, err = tmpl.New("", string(i.Release.OutputName), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'release.outputName' parameter: %w", err)
	}
	i.templates.alias, err = tmpl.New("", string(i.Alias), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'alias' parameter: %w", err)
	}
	return nil
}

// InputRendered NB: This is yaml/json serializable for dump on render kubocd CLI command
type InputRendered struct {
	Interface           string `json:"interface"`
	Kind                Kind   `json:"kind,omitempty"`
	Namespace           string `json:"namespace,omitempty"`
	UnmanagedConnection struct {
		Name string `json:"name,omitempty"`
	} `json:"unmanagedConnection,omitempty"`
	Release struct {
		Name       string `json:"name,omitempty"`
		OutputName string `json:"outputName,omitempty"`
	} `json:"release,omitempty"`
	Alias string `json:"alias,omitempty"`
}

func (i *Input) Render(model map[string]interface{}, defaultNamespace string) (*InputRendered, error) {
	ir := &InputRendered{}
	var err error
	ir.Interface, err = i.templates.iface.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'interface' parameter: %w", err)
	}
	k, err := i.templates.kind.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'kind' parameter: %w", err)
	}
	ir.Namespace, err = i.templates.namespace.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'namespace' parameter: %w", err)
	}
	ir.UnmanagedConnection.Name, err = i.templates.unmanagedConnection.name.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'unmanagedConnection.name' parameter: %w", err)
	}
	ir.Release.Name, err = i.templates.release.name.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'release.name' parameter: %w", err)
	}
	ir.Release.OutputName, err = i.templates.release.outputName.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'release.outputName' parameter: %w", err)
	}
	ir.Alias, err = i.templates.alias.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'alias' parameter: %w", err)
	}
	ir.Kind = Kind(k)
	if ir.Kind == "" {
		ir.Kind = KindConnection
	}
	if ir.Kind != KindConnection && ir.Kind != KindClusterConnection {
		return nil, fmt.Errorf("'kind' should be either 'Connection' or 'ClusterConnection'")
	}
	if ir.Kind == KindClusterConnection && ir.Namespace != "" {
		return nil, fmt.Errorf("'namespace' should be be empty if kink == ClusterConnection")
	}
	if ir.Interface == "" {
		return nil, fmt.Errorf("'interface' is a required parameters")
	}
	if ir.UnmanagedConnection.Name != "" && ir.Release.Name != "" {
		return nil, fmt.Errorf("'unmanagedConnection.name' and 'release.name' can't be defined at the same time")
	}
	if ir.Alias == "" {
		ir.Alias = ir.Interface
	}
	if ir.Namespace == "" && ir.Kind == KindConnection {
		ir.Namespace = defaultNamespace
	}
	return ir, nil
}
