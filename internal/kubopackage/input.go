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
	"kubocd/internal/misc"
	"kubocd/internal/tmpl"
)

type Input struct {
	// required:true
	Interface KcdTemplateString `json:"interface"`
	// required:false
	// Connection or ClusterConnection. If "", then lookup both
	Kind KcdTemplateString `json:"kind,omitempty"`
	// required:false
	InterfaceLookup struct {
		// required:false
		// default to releaseNamespace
		Namespace KcdTemplateString `json:"namespace"`
	} `json:"interfaceLookup,omitempty"`
	NamedConnection struct {
		// required:true
		Name KcdTemplateString `json:"name"`
		// required:false
		// If kind == Connection, default to releaseNamespace. Error if defined and kind == ClusterConnection
		Namespace KcdTemplateString `json:"namespace"`
	} `json:"namedConnection,omitempty"`
	Release struct {
		// required:true
		Name KcdTemplateString `json:"name"`
		// required:false
		// Default to releaseNamespace
		Namespace KcdTemplateString `json:"namespace"`
		// required:false
		// In case the target Release have several output with same interface
		Output KcdTemplateString `json:"output,omitempty"`
	} `json:"release,omitempty"`
	// default to interface
	Alias KcdTemplateString `json:"alias,omitempty"`
	// Default: false.
	// If true and the connection is missing, there is no error, and `.Inputs.<alias>` does not exist.
	Optional KcdTemplateBool `json:"optional,omitempty"`
	// Default: false.
	// If false, error in case of multiple providers on a binding
	AllowMultiple KcdTemplateBool `json:"allowMultiple"`

	// ------------------------------- Private part
	templates *inputTemplates
}

type releaseTmpl struct {
}

type inputTemplates struct {
	iface           tmpl.Tmpl
	kind            tmpl.Tmpl
	interfaceLookup struct {
		namespace tmpl.Tmpl
	}
	namedConnection struct {
		name      tmpl.Tmpl
		namespace tmpl.Tmpl
	}
	release struct {
		name      tmpl.Tmpl
		namespace tmpl.Tmpl
		output    tmpl.Tmpl
	}
	alias         tmpl.Tmpl
	optional      tmpl.Tmpl
	allowMultiple tmpl.Tmpl
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
		return fmt.Errorf("could not parse 'namedConnection.kind' parameter: %w", err)
	}

	// ---------------------------
	i.templates.interfaceLookup.namespace, err = tmpl.New("", string(i.InterfaceLookup.Namespace), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'nterfaceLookup.namespace' parameter: %w", err)
	}

	// ---------------------------
	i.templates.namedConnection.name, err = tmpl.New("", string(i.NamedConnection.Name), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'namedConnection.name' parameter: %w", err)
	}
	i.templates.namedConnection.namespace, err = tmpl.New("", string(i.NamedConnection.Namespace), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'namedConnection.namespace' parameter: %w", err)
	}

	// ------------------------
	i.templates.release.name, err = tmpl.New("", string(i.Release.Name), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'release.name' parameter: %w", err)
	}
	i.templates.release.namespace, err = tmpl.New("", string(i.Release.Namespace), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'release.namespace' parameter: %w", err)
	}
	i.templates.release.output, err = tmpl.New("", string(i.Release.Output), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'release.output' parameter: %w", err)
	}

	// ------------------------
	i.templates.alias, err = tmpl.New("", string(i.Alias), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'alias' parameter: %w", err)
	}
	i.templates.optional, err = tmpl.New("", string(i.Optional), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'optional' parameter: %w", err)
	}
	i.templates.allowMultiple, err = tmpl.New("", string(i.AllowMultiple), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'allowMultiple' parameter: %w", err)
	}
	return nil
}

// InputRendered NB: This is yaml/json serializable for dump on render kubocd CLI command
type InputRendered struct {
	Interface       string         `json:"interface"`
	Kind            kv1alpha1.Kind `json:"kind,omitempty"`
	InterfaceLookup struct {
		Namespace string `json:"namespace"`
	} `json:"interfaceLookup"`
	NamedConnection struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"namedConnection,omitempty"`
	Release struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Output    string `json:"output,omitempty"`
	} `json:"release,omitempty"`
	Alias         string `json:"alias"`
	Optional      bool   `json:"optional"`
	AllowMultiple bool   `json:"allowMultiple"`
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
	ir.Kind = kv1alpha1.Kind(k)

	// -----------------------------------------
	ir.InterfaceLookup.Namespace, err = i.templates.interfaceLookup.namespace.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'interfaceLookup.namespace' parameter: %w", err)
	}
	// -----------------------------------------
	ir.NamedConnection.Name, err = i.templates.namedConnection.name.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'namedConnection.name' parameter: %w", err)
	}
	ir.NamedConnection.Namespace, err = i.templates.namedConnection.namespace.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'namedConnection.namespace' parameter: %w", err)
	}
	// -----------------------------------------
	ir.Release.Name, err = i.templates.release.name.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'release.name' parameter: %w", err)
	}
	ir.Release.Namespace, err = i.templates.release.namespace.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'release.namespace' parameter: %w", err)
	}
	ir.Release.Output, err = i.templates.release.output.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'release.output' parameter: %w", err)
	}
	// ------------------------------------
	ir.Alias, err = i.templates.alias.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'alias' parameter: %w", err)
	}
	if ir.Alias == "" {
		ir.Alias = ir.Interface
	}
	ir.Optional, _, err = i.templates.optional.RenderToBool(model, false)
	if err != nil {
		return nil, fmt.Errorf("could not render 'optional' parameter: %w", err)
	}
	ir.AllowMultiple, _, err = i.templates.allowMultiple.RenderToBool(model, false)
	if err != nil {
		return nil, fmt.Errorf("could not render 'allowMultiple' parameter: %w", err)
	}

	// ----------------------------- Check
	if ir.Interface == "" {
		return nil, fmt.Errorf("interface is required")
	}
	if ir.Kind != "" && ir.Kind != kv1alpha1.KindConnection && ir.Kind != kv1alpha1.KindClusterConnection {
		return nil, fmt.Errorf("invalid kind '%s' value ", ir.Kind)
	}
	// ---------- Checks and default
	if ir.NamedConnection.Name != "" {
		if ir.NamedConnection.Namespace == "" {
			if ir.Kind != kv1alpha1.KindClusterConnection {
				// The (possible) Connection lookup needs a namespace, both for an
				// explicit kind: Connection and for the dual lookup (kind unset).
				// The ClusterConnection lookup ignores it.
				ir.NamedConnection.Namespace = defaultNamespace
			}
		} else {
			if ir.Kind == kv1alpha1.KindClusterConnection {
				return nil, fmt.Errorf("namedConnection.namespace must be empty if kind is ClusterConnection")
			}
		}
	}
	if ir.Release.Name != "" && ir.Release.Namespace == "" {
		ir.Release.Namespace = defaultNamespace
	}
	x := misc.CountNonZero(ir.InterfaceLookup.Namespace, ir.NamedConnection.Name, ir.Release.Name)
	if x > 1 {
		return nil, fmt.Errorf("0 or one of 'interfaceLookup.namespace', 'namedConnection.name' or 'release.name' sub element may be specified")
	}
	if x == 0 {
		ir.InterfaceLookup.Namespace = defaultNamespace
	}
	return ir, nil
}
