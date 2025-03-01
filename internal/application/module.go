package application

import (
	"fmt"
	"kubocd/internal/global"
	"kubocd/internal/misc"
)

type Module struct {
	// required:true
	// default: id<idx>
	Name   string `yaml:"name" json:"name"`
	Type   string `yaml:"type" json:"type"` // HelmChart or Application
	Source struct {
		// Only for HelmChart
		HelmRepository *struct {
			// required:true
			Url string `yaml:"url" json:"url"`
			// required:true
			Chart   string `yaml:"chart" json:"chart"`
			Version string `yaml:"version" json:"version"`
		} `yaml:"helmRepository" json:"helmRepository"`
		Oci *struct {
			// required:true
			Repository string `yaml:"repository" json:"repository"`
			// required:true
			Tag      string `yaml:"tag" json:"tag"`
			Insecure bool   `yaml:"insecure" json:"insecure"`
		} `yaml:"oci" json:"oci"`
		Git *struct {
			// required:true
			Url    string `yaml:"url" json:"url"`
			Branch string `yaml:"branch" json:"branch"`
			Tag    string `yaml:"tag" json:"tag"`
			// The folder where is located 'Chart.yaml'
			// required:true
			Path string `yaml:"path" json:"path"`
		} `yaml:"git" json:"git"`
	} `yaml:"source" json:"source"`
	// For Type == Application
	Parameters KcdTemplateMap `yaml:"parameters" json:"parameters"`
	// For Type == HelmChart
	Values KcdTemplateMap `yaml:"values" json:"values"`
	// Rendered value must be a Map, which will be  inserted in the configuration of fluxCD helmRelease.spec
	Config KcdTemplateMap `yaml:"config" json:"config"`
	// Default: {{ .Release.namespace }}
	Namespace KcdTemplateString `yaml:"namespace" json:"namespace"`
	// Default: {{ .Release.enabled }}
	Enabled KcdTemplateBool `yaml:"enabled" json:"enabled"`
	// Default: {{ .Release.suspended }}
	Suspended KcdTemplateBool `yaml:"suspended" json:"suspended"`
	// Default: {{ .Release.protected }}
	Protected KcdTemplateBool `yaml:"protected" json:"protected"`
	// Default: {{ .Release.createNamespace }}
	CreateNamespace KcdTemplateBool `yaml:"createNamespace" json:"createNamespace"`
	// Intra-application dependency. List of module names
	DependsOn []string `yaml:"dependsOn" json:"dependsOn"`
}

func (m *Module) groom(idx int) error {
	if m.Name == "" {
		m.Name = fmt.Sprintf("module%02d", idx)
	}
	if m.Type == "" {
		m.Type = global.HelmChartType
		//return fmt.Errorf("module type is required")
	}
	if m.Type != global.ApplicationType && m.Type != global.HelmChartType {
		return fmt.Errorf("invalid application type: %s", m.Type)
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
	if m.Type == global.ApplicationType && !misc.IsZero(m.Values) {
		return fmt.Errorf("'values' should not be defined for 'type.Application'")

	}
	if m.Type == global.HelmChartType && !misc.IsZero(m.Parameters) {
		return fmt.Errorf("'parameters' should not be defined for 'type.HelmChart'")
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
	return nil

}
