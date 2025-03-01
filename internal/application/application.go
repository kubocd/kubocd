package application

import (
	"fmt"
	"kubocd/internal/global"
	"kubocd/internal/misc"
)

// KcdTemplateMap A template where expected result is a map[string]interface{}
type KcdTemplateMap string

// KcdTemplateBool A template where expected result is a boolean
type KcdTemplateBool string

// KcdTemplateString A template where expected result is a string
type KcdTemplateString string

type KcdRole string

type KcdSchema map[string]interface{}

// ------------------------------------------------

type Application struct {
	// required:true
	ApiVersion string `yaml:"apiVersion" json:"apiVersion"` // v1alpha1
	// This is NOT a k8s object. To overemphasis this, we use Type instead of Kind
	// required:true
	Type     string `yaml:"type" json:"type"` // Always 'Application'
	Metadata struct {
		// required:true
		Name string `yaml:"name" json:"name"`
		// required:true
		Version string `yaml:"version" json:"version"`
	} `yaml:"metadata" json:"metadata"`
	Spec struct {
		// A template aimed to be rendered on deployment.
		// Intended to provide user with usage information // (Access link, configuration, ....)
		// 0ne and only one of the properties must be defined
		Usage           KcdTemplateString `yaml:"usage" json:"usage"`
		ReleaseDefaults struct {
			// Set of default values for the Parameters provided by the release.
			Parameters map[string]interface{} `yaml:"parameters" json:"parameters"`
			// Default value for the corresponding release property
			// default: false
			Protected bool `yaml:"protected" json:"protected"`
		} `yaml:"releaseDefaults" json:"releaseDefaults"`
		ParametersSchema KcdSchema `yaml:"parametersSchema" json:"parametersSchema"`
		ContextSchema    KcdSchema `yaml:"contextSchema" json:"contextSchema"`
		Modules          []Module  `yaml:"modules" json:"modules"`
		Roles            []KcdRole `yaml:"roles" json:"roles"`
		DependsOn        []KcdRole `yaml:"dependsOn" json:"dependsOn"`
	} `yaml:"spec" json:"spec"`
	Status struct {
		// Fulfilled when packaging
		ChartByModule map[string]ChartRef `yaml:"chartByModule" json:"chartByModule"`
		// TODO: extract from schema
		DefaultParameters map[string]interface{} `yaml:"defaultParameters" json:"defaultParameters"`
	} `yaml:"status" json:"status"`
}

type ChartRef struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version" json:"version"`
}

func (app *Application) Groom() error {
	if app.ApiVersion != global.ApplicationApiVersion {
		return fmt.Errorf("'apiVersion' must be %s", global.ApplicationApiVersion)
	}
	if app.Type != global.ApplicationType {
		return fmt.Errorf("'type' must be %s", global.ApplicationType)
	}
	x := misc.CountNonZero(app.Metadata.Name, app.Metadata.Version)
	if x != 2 {
		return fmt.Errorf("'name' and 'version' should be set")
	}
	if !misc.ValidateK8sName(app.Metadata.Name) {
		return fmt.Errorf("invalid 'name'. Must contain only alphanumeric characters, dashes and underscores")
	}
	if app.Spec.ParametersSchema != nil {
		// TODO: Validate schema (And make it required)
	}
	if app.Spec.ContextSchema != nil {
		// TODO: Validate schema
	}
	if app.Spec.Modules == nil || len(app.Spec.Modules) == 0 {
		return fmt.Errorf("an application must have at least one module")
	}
	// ------------------------ Normalize
	if app.Spec.Roles == nil {
		app.Spec.Roles = []KcdRole{}
	}
	if app.Spec.DependsOn == nil {
		app.Spec.DependsOn = []KcdRole{}
	}
	if app.Spec.ReleaseDefaults.Parameters == nil {
		app.Spec.ReleaseDefaults.Parameters = map[string]interface{}{}
	}
	// ------- Now, check modules
	moduleByName := make(map[string]*Module)
	for idx := range app.Spec.Modules {
		module := &app.Spec.Modules[idx]
		err := app.Spec.Modules[idx].groom(idx)
		if err != nil {
			return fmt.Errorf("module '%s': %w", app.Spec.Modules[idx].Name, err)
		}
		_, ok := moduleByName[module.Name]
		if ok {
			return fmt.Errorf("duplicate module name: %s", module.Name)
		}
		moduleByName[module.Name] = &app.Spec.Modules[idx]
	}
	// And another loop to check internal dependencies
	for _, module := range app.Spec.Modules {
		for _, dependency := range module.DependsOn {
			_, ok := moduleByName[dependency]
			if !ok {
				return fmt.Errorf("module '%s' depends on unknown module '%s'", module.Name, dependency)
			}
		}
	}
	return nil
}
