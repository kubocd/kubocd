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

type Input struct {
	// required:true
	Interface KcdTemplateString `json:"interface"`
	//
	Alias KcdTemplateString `json:"alias,omitempty"`
	//
	Connection struct {
		// Default to release namespace
		Namespace KcdTemplateString `json:"namespace,omitempty"`
		// k8s connection name. Mainly for unmanaged connection
		Name KcdTemplateString `json:"name,omitempty"`
		// The release managing this connection.
		Release KcdTemplateString `json:"release,omitempty"`
		// Used if the release manage several connection with the same interface
		OutputName KcdTemplateString `json:"outputName,omitempty"`
		// Connection or ClusterConnection
		Kind KcdTemplateString `json:"kind,omitempty"`
	}
	// ------------------------------- Private part
	templates *inputTemplates
}

type inputTemplates struct {
	iface      tmpl.Tmpl
	alias      tmpl.Tmpl
	connection struct {
		namespace  tmpl.Tmpl
		name       tmpl.Tmpl
		release    tmpl.Tmpl
		outputName tmpl.Tmpl
		kind       tmpl.Tmpl
	}
}

func (i *Input) groom(pck *Package) error {
	if i.Interface == "" {
		return fmt.Errorf("'interface' is a required parameters")
	}
	if i.Connection.Name != "" && i.Connection.Release != "" {
		return fmt.Errorf("'connection' and 'release' can't be defined at the same time")
	}
	// ---------------- Now, handle templates
	i.templates = &inputTemplates{}
	var err error
	i.templates.iface, err = tmpl.New("", string(i.Interface), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'interface' parameter: %w", err)
	}
	i.templates.alias, err = tmpl.New("", string(i.Alias), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'alias' parameter: %w", err)
	}
	i.templates.connection.namespace, err = tmpl.New("", string(i.Connection.Namespace), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'alias' connection.namespace: %w", err)
	}
	i.templates.connection.name, err = tmpl.New("", string(i.Connection.Name), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'alias' connection.name: %w", err)
	}
	i.templates.connection.release, err = tmpl.New("", string(i.Connection.Release), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'alias' connection.release: %w", err)
	}
	i.templates.connection.outputName, err = tmpl.New("", string(i.Connection.OutputName), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'alias' connection.outputName: %w", err)
	}
	i.templates.connection.kind, err = tmpl.New("", string(i.Connection.Kind), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'alias' connection.kind: %w", err)
	}
	return nil
}

// InputRendered NB: This is yaml/json serializable for dump on render kubocd CLI command
type InputRendered struct {
	Iface      string `json:"iface"`
	Alias      string `json:"alias,omitempty"`
	Connection struct {
		Namespace  string `json:"namespace,omitempty"`
		Name       string `json:"name,omitempty"`
		Release    string `json:"release,omitempty"`
		OutputName string `json:"outputName,omitempty"`
		Kind       string `json:"kind,omitempty"`
	} `json:"connection,omitempty"`
}

func (i *Input) Render(model map[string]interface{}) (*InputRendered, error) {
	ir := &InputRendered{}
	var err error
	ir.Iface, err = i.templates.iface.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'interface' parameter: %w", err)
	}
	ir.Alias, err = i.templates.alias.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'alias' parameter: %w", err)
	}
	ir.Connection.Namespace, err = i.templates.connection.namespace.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'connection.namespace' parameter: %w", err)
	}
	ir.Connection.Name, err = i.templates.connection.name.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'connection.name' parameter: %w", err)
	}
	ir.Connection.Release, err = i.templates.connection.release.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'connection.release' parameter: %w", err)
	}
	ir.Connection.OutputName, err = i.templates.connection.outputName.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'connection.outputName' parameter: %w", err)
	}
	ir.Connection.Kind, err = i.templates.connection.kind.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'connection.kind' parameter: %w", err)
	}
	return ir, nil
}
