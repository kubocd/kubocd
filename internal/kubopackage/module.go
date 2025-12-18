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
	"kubocd/internal/configstore"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"kubocd/internal/tmpl"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Module struct {
	// required:true
	// default: id<idx>
	Name string `json:"name,omitempty"`
	// required:true
	// default: HelmChart
	Type   string `json:"type,omitempty"` // HelmChart or Package
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
	// For Type == Package
	Parameters KcdTemplateMap `json:"parameters,omitempty"`
	// For Type == HelmChart
	Values KcdTemplateMap `json:"values,omitempty"`
	// Rendered value must be a Map, which will be applied on top of fluxCD helmRelease.spec
	SpecPatch KcdTemplateMap `json:"specPatch,omitempty"`
	//
	OnFailureStrategy KcdTemplateString `json:"onFailureStrategy,omitempty"`
	// Timeout is the time to wait for any individual Kubernetes operation
	// during the performance of a Helm action.
	//Defaults to the package value
	Timeout KcdTemplateDuration `json:"timeout,omitempty"`
	// Default: {{ .Release.spec.targetNamespace }}
	TargetNamespace KcdTemplateString `json:"targetNamespace,omitempty"`
	// Effective value is And-ed with the release corresponding value
	// Default: "true"
	Enabled KcdTemplateBool `json:"enabled,omitempty"`
	// Intra-package dependency. List of module names
	DependsOn KcdTemplateStringList `json:"dependsOn,omitempty"`
	// ------------------- Private part
	templates *moduleTemplates
}

func (m *Module) groom(pck *Package, idx int, configStore configstore.ConfigStore) error {
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
	if misc.IsZero(m.Timeout) {
		if configStore == nil {
			// When used in packaging
			m.Timeout = "2m0s"
		} else {
			m.Timeout = KcdTemplateDuration(configStore.GetDefaultHelmTimeout().String())
		}
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
	if m.Type == global.PackageType && !misc.IsZero(m.Values) {
		return fmt.Errorf("'values' should not be defined for 'type.Package'")

	}
	if m.Type == global.HelmChartType && !misc.IsZero(m.Parameters) {
		return fmt.Errorf("'parameters' should not be defined for 'type.HelmChart'")
	}
	// ---------------- Now, handle templates
	m.templates = &moduleTemplates{}
	var err error
	m.templates.parameters, err = tmpl.NewFromAny("", m.Parameters, pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'parameters' template: %w", err)
	}
	m.templates.values, err = tmpl.NewFromAny("", m.Values, pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'values' template: %w", err)
	}
	m.templates.specPatch, err = tmpl.NewFromAny("", m.SpecPatch, pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'specPatch' template: %w", err)
	}
	m.templates.timeout, err = tmpl.New("", string(m.Timeout), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'timeout' template: %w", err)
	}
	m.templates.targetNamespace, err = tmpl.New("", string(m.TargetNamespace), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'targetNamespace' template: %w", err)
	}
	m.templates.enabled, err = tmpl.New("", string(m.Enabled), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'enabled' template: %w", err)
	}
	m.templates.dependsOn, err = tmpl.NewFromAny("", m.DependsOn, pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'dependsOn' template: %w", err)
	}
	m.templates.onFailureStrategy, err = tmpl.New("", string(m.OnFailureStrategy), pck.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'onFailureStrategy' template: %w", err)
	}
	return nil
}

type moduleTemplates struct {
	parameters        tmpl.Tmpl
	values            tmpl.Tmpl
	specPatch         tmpl.Tmpl
	targetNamespace   tmpl.Tmpl
	enabled           tmpl.Tmpl
	dependsOn         tmpl.Tmpl
	timeout           tmpl.Tmpl
	onFailureStrategy tmpl.Tmpl
}

// ModuleRendered object is a proxy for module. Aim is to concentrate all error detection in its constructor
// Standard way should be to hev Getters on module object.
// But each getter may generate an error, thus complicate the code.
type ModuleRendered struct {
	Parameters        map[string]interface{}
	Values            map[string]interface{}
	SpecPatch         map[string]interface{}
	TargetNamespace   string
	Enabled           bool
	DependsOn         []string
	Timeout           metav1.Duration
	OnFailureStrategy string
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
	mr.SpecPatch, txt, err = m.templates.specPatch.RenderToMap(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'specPatch' template: %w (%s)", err, txt)
	}
	mr.Timeout, txt, err = m.templates.timeout.RenderToDuration(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'timeout' template: %w (%s)", err, txt)
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
		mr.SpecPatch = misc.MergeMaps(mr.SpecPatch, createNamespacePatch)
	}
	mr.OnFailureStrategy, err = m.templates.onFailureStrategy.RenderToSingleLine(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'onFailureStrategy' template: %w", err)
	}

	//fmt.Printf("****** config:\n%s\n", misc.Map2Yaml(mr.Config))
	//fmt.Printf("****** values:\n%s\n", misc.Map2Yaml(mr.Values))
	//fmt.Printf("****** namespace:\n%s\n", misc.Map2Yaml(mr.Namespace))
	return mr, nil
}
