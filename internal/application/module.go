package application

import (
	"fmt"
	"kubocd/internal/global"
	"kubocd/internal/misc"
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
	} `yaml:"source" json:"source"`
	// For Type == Application
	Parameters KcdTemplateMap `json:"parameters,omitempty"`
	// For Type == HelmChart
	Values KcdTemplateMap `json:"values,omitempty"`
	// Rendered value must be a Map, which will be  inserted in the configuration of fluxCD helmRelease.spec
	Config KcdTemplateMap `json:"config,omitempty"`
	// Default: {{ .Release.namespace }}
	Namespace KcdTemplateString `json:"namespace,omitempty"`
	// Default: {{ .Release.enabled }}
	Enabled KcdTemplateBool `json:"enabled,omitempty"`
	// Default: {{ .Release.suspended }}
	Suspended KcdTemplateBool `json:"suspended,omitempty"`
	// Default: {{ .Release.protected }}
	Protected KcdTemplateBool `json:"protected,omitempty"`
	// Default: {{ .Release.createNamespace }}
	CreateNamespace KcdTemplateBool `json:"createNamespace,omitempty"`
	// Intra-application dependency. List of module names
	DependsOn []string `json:"dependsOn,omitempty"`
}

func (m *Module) validate(idx int) error {
	// We don't want validate() to alter the Module. So, use local variable
	var mType string = m.Type
	if mType == "" {
		mType = global.HelmChartType
	}

	if mType != global.ApplicationType && mType != global.HelmChartType {
		return fmt.Errorf("invalid application type: %s", mType)
	}
	x := misc.CountNonZero(m.Source.HelmRepository, m.Source.Oci, m.Source.Git)
	if x != 1 {
		return fmt.Errorf("one and only one of 'helmRepository' and 'oci' should be set")
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
	if mType == global.ApplicationType && !misc.IsZero(m.Values) {
		return fmt.Errorf("'values' should not be defined for 'type.Application'")

	}
	if mType == global.HelmChartType && !misc.IsZero(m.Parameters) {
		return fmt.Errorf("'parameters' should not be defined for 'type.HelmChart'")
	}
	return nil
}

func (m *Module) groom(idx int) {
	if m.Name == "" {
		m.Name = fmt.Sprintf("module%02d", idx)
	}
	if m.Type == "" {
		m.Type = global.HelmChartType
		//return fmt.Errorf("module type is required")
	}
	if misc.IsZero(m.Namespace) {
		m.Namespace = "{{ .Release.namespace }}"
	}
	if misc.IsZero(m.Enabled) {
		m.Enabled = "{{ .Release.enabled }}"
	}
	if misc.IsZero(m.Suspended) {
		m.Suspended = "{{ .Release.suspended }}"
	}
	if misc.IsZero(m.Protected) {
		m.Protected = "{{ .Release.protected }}"
	}
	if misc.IsZero(m.CreateNamespace) {
		m.CreateNamespace = "{{ .Release.createNamespace }}"
	}
	// Normalize
	if m.DependsOn == nil {
		m.DependsOn = []string{}
	}
}
