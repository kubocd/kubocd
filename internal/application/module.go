package application

import (
	"fmt"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"kubocd/internal/tmpl"
)

type Module struct {
	// required:true
	// default: id<idx>
	Name string `json:"name,omitempty"`
	// required:true
	// default: HelmChart
	Type   string `json:"type,omitempty"` // HelmChart or Application
	Source struct {
		// Only for HelmChart
		HelmRepository *struct {
			// required:true
			Url string `json:"url"`
			// required:true
			Chart   string `yaml:"chart" json:"chart"`
			Version string `json:"version,omitempty"`
		} `json:"helmRepository,omitempty"`
		Oci *struct {
			// required:true
			Repository string `json:"repository"`
			// required:true
			Tag      string `json:"tag"`
			Insecure bool   `json:"insecure"`
		} `json:"oci,omitempty"`
		Git *struct {
			// required:true
			Url    string `json:"url"`
			Branch string `json:"branch,omitempty"`
			Tag    string `json:"tag,omitempty"`
			// The folder where is located 'Chart.yaml'
			// required:true
			Path string `json:"path"`
		} `json:"git,omitempty"`
		Local *struct {
			Path string `json:"path"`
		} `json:"local,omitempty"`
	} `yaml:"source" json:"source"`
	// For Type == Application
	Parameters KcdTemplateMap `json:"parameters,omitempty"`
	// For Type == HelmChart
	Values KcdTemplateMap `json:"values,omitempty"`
	// Rendered value must be a Map, which will be applied on top of fluxCD helmRelease.spec
	SpecAddon KcdTemplateMap `json:"specAddon,omitempty"`
	// Default: {{ .Release.spec.targetNamespace }}
	TargetNamespace KcdTemplateString `json:"targetNamespace,omitempty"`
	// Effective value is And-ed with the release corresponding value
	// Default: "true"
	Enabled KcdTemplateBool `json:"enabled,omitempty"`
	// Intra-application dependency. List of module names
	DependsOn KcdTemplateStringList `json:"dependsOn,omitempty"`
	// ------------------- Private part
	templates *moduleTemplates
}

func (m *Module) groom(application *Application, idx int) error {
	if m.Name == "" {
		m.Name = fmt.Sprintf("module%02d", idx)
	}
	if m.Type == "" {
		m.Type = global.HelmChartType
		//return fmt.Errorf("module type is required")
	}
	if misc.IsZero(m.TargetNamespace) {
		m.TargetNamespace = "{{.Release.spec.targetNamespace}}"
	}
	if m.Enabled == "" {
		m.Enabled = "true"
	}
	// Normalize
	if m.DependsOn == nil {
		m.DependsOn = []string{}
	}
	x := misc.CountNonZero(m.Source.HelmRepository, m.Source.Oci, m.Source.Git, m.Source.Local)
	if x != 1 {
		return fmt.Errorf("one and only one of 'helmRepository', 'git', 'loacl' and 'oci' should be set")
	}
	if m.Source.HelmRepository != nil {
		x := misc.CountNonZero(m.Source.HelmRepository.Url, m.Source.HelmRepository.Chart, m.Source.HelmRepository.Version)
		if x != 3 {
			return fmt.Errorf("'url', 'chart' and 'version' should be set for 'source.helmRepository")
		}
	}
	if m.Source.Oci != nil {
		x := misc.CountNonZero(m.Source.Oci.Repository, m.Source.Oci.Tag)
		if x != 2 {
			return fmt.Errorf("both 'repository' and 'tag' should be set for 'source.oci")
		}
	}
	if m.Source.Git != nil {
		if m.Source.Git.Url == "" {
			return fmt.Errorf("'url' should be set for 'source.git'")
		}
		if m.Source.Git.Path == "" {
			return fmt.Errorf("'path' should be set for 'source.git'")
		}
		x := misc.CountNonZero(m.Source.Git.Branch, m.Source.Git.Tag)
		if x != 1 {
			return fmt.Errorf("one and only one of 'branch' and 'tag' should be set for 'source.git'")
		}
	}
	if m.Type == global.ApplicationType && !misc.IsZero(m.Values) {
		return fmt.Errorf("'values' should not be defined for 'type.Application'")

	}
	if m.Type == global.HelmChartType && !misc.IsZero(m.Parameters) {
		return fmt.Errorf("'parameters' should not be defined for 'type.HelmChart'")
	}
	// ---------------- Now, handle templates
	m.templates = &moduleTemplates{}
	var err error
	m.templates.parameters, err = tmpl.NewFromAny("", m.Parameters, application.Spec.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'parameters' template: %w", err)
	}
	m.templates.values, err = tmpl.NewFromAny("", m.Values, application.Spec.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'values' template: %w", err)
	}
	m.templates.specAddon, err = tmpl.NewFromAny("", m.SpecAddon, application.Spec.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'specAddon' template: %w", err)
	}
	m.templates.targetNamespace, err = tmpl.New("", string(m.TargetNamespace), application.Spec.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'targetNamespace' template: %w", err)
	}
	m.templates.enabled, err = tmpl.New("", string(m.Enabled), application.Spec.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'enabled' template: %w", err)
	}
	m.templates.dependsOn, err = tmpl.NewFromAny("", m.DependsOn, application.Spec.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'dependsOn' template: %w", err)
	}
	return nil
}

type moduleTemplates struct {
	parameters      tmpl.Tmpl
	values          tmpl.Tmpl
	specAddon       tmpl.Tmpl
	targetNamespace tmpl.Tmpl
	enabled         tmpl.Tmpl
	dependsOn       tmpl.Tmpl
}

// ModuleRendered object is a proxy for module. Aim is to concentrate all error detection in its constructor
// Standard way should be to hev Getters on module object.
// But each getter may generate an error, thus complicate the code.
type ModuleRendered struct {
	Parameters      map[string]interface{}
	Values          map[string]interface{}
	SpecAddon       map[string]interface{}
	TargetNamespace string
	Enabled         bool
	DependsOn       []string
}

var createNamespacePatch = map[string]interface{}{
	"install": map[string]interface{}{
		"createNamespace": true,
	},
}

func (m *Module) Render(model map[string]interface{}) (*ModuleRendered, error) {
	mr := &ModuleRendered{}
	var err error
	var txt string
	mr.Parameters, txt, err = m.templates.parameters.RenderToMap(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'parameters' template: %w (%s)", err, txt)
	}
	mr.Values, txt, err = m.templates.values.RenderToMap(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'values' template: %w (%s)", err, txt)
	}
	mr.SpecAddon, txt, err = m.templates.specAddon.RenderToMap(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'specAddon' template: %w (%s)", err, txt)
	}
	mr.TargetNamespace, err = m.templates.targetNamespace.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'targetNamespace' template: %w", err)
	}
	mr.Enabled, txt, err = m.templates.enabled.RenderToBool(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'enabled' template: %w (%s)", err, txt)
	}
	mr.DependsOn, txt, err = m.templates.dependsOn.RenderToStringList(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'dependsOn' template: %w (%s)", err, txt)
	}
	if model["Release"].(map[string]interface{})["spec"].(map[string]interface{})["createNamespace"].(bool) {
		mr.SpecAddon = misc.MergeMaps(mr.SpecAddon, createNamespacePatch)
	}
	//fmt.Printf("****** config:\n%s\n", misc.Map2Yaml(mr.Config))
	//fmt.Printf("****** values:\n%s\n", misc.Map2Yaml(mr.Values))
	//fmt.Printf("****** namespace:\n%s\n", misc.Map2Yaml(mr.Namespace))
	return mr, nil
}
