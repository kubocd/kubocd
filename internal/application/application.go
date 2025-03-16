package application

import (
	"fmt"
	"kubocd/internal/global"
	"kubocd/internal/kuboschema"
	"kubocd/internal/misc"
)

// KcdTemplateMap A template where expected result is a map[string]interface{}
type KcdTemplateMap string

// KcdTemplateBool A template where expected result is a boolean
type KcdTemplateBool string

// KcdTemplateString A template where expected result is a string
type KcdTemplateString string

type KcdRole string

// ------------------------------------------------

type Application struct {
	// required:true
	ApiVersion string `json:"apiVersion"` // v1alpha1
	Metadata   struct {
		// This is NOT a k8s object. To overemphasis this, we use Type instead of Kind. and define it as metadata
		// required:true
		Type string `json:"type"` // Always 'Application'
		// required:true
		Name string `json:"name"`
		// required:true
		Version string `json:"version"`
	} `json:"metadata"`
	Spec struct {
		// A template aimed to be rendered on deployment.
		// Intended to provide user with usage information // (Access link, configuration, ....)
		// 0ne and only one of the properties must be defined
		Usage KcdTemplateString `json:"usage,omitempty"`
		// Prevent deletion
		// Default: {{ .Release.protected }}
		Protected        KcdTemplateBool       `json:"protected,omitempty"`
		ParametersSchema kuboschema.KuboSchema `json:"parametersSchema,omitempty"`
		ContextSchema    kuboschema.KuboSchema `json:"contextSchema,omitempty"`
		// required: true
		Modules   []Module  `json:"modules"`
		Roles     []KcdRole `json:"roles,omitempty"`
		DependsOn []KcdRole `json:"dependsOn,omitempty"`
	} `yaml:"spec" json:"spec"`
}

type ChartRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (app *Application) Groom() error {
	// ------------------------ Normalize
	if app.Spec.Roles == nil {
		app.Spec.Roles = []KcdRole{}
	}
	if app.Spec.DependsOn == nil {
		app.Spec.DependsOn = []KcdRole{}
	}
	if app.Spec.Protected == "" {
		app.Spec.Protected = "{{ .Release.protected }}"
	}
	// Validation
	if app.ApiVersion != global.ApplicationApiVersion {
		return fmt.Errorf("'apiVersion' must be %s (is '%s')", global.ApplicationApiVersion, app.ApiVersion)
	}
	if app.Metadata.Type != global.ApplicationType {
		return fmt.Errorf("'type' must be %s", global.ApplicationType)
	}
	x := misc.CountNonZero(app.Metadata.Name, app.Metadata.Version)
	if x != 2 {
		return fmt.Errorf("'name' and 'version' must be set")
	}
	if !misc.ValidateK8sName(app.Metadata.Name) {
		return fmt.Errorf("invalid 'name'. Must contain only alphanumeric characters, dashes and underscores")
	}
	var err error
	if app.Spec.ParametersSchema != nil {
		app.Spec.ParametersSchema, err = kuboschema.Kubo2openAPI(app.Spec.ParametersSchema, false)
		if err != nil {
			return fmt.Errorf("invalid 'parametersSchema': %w", err)
		}
	}
	if app.Spec.ContextSchema != nil {
		app.Spec.ContextSchema, err = kuboschema.Kubo2openAPI(app.Spec.ContextSchema, true)
		if err != nil {
			return fmt.Errorf("invalid 'contextSchema': %w", err)
		}
	}
	if app.Spec.Modules == nil || len(app.Spec.Modules) == 0 {
		return fmt.Errorf("an application must have at least one module")
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
