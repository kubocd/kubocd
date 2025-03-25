package application

import (
	"fmt"
	"kubocd/internal/global"
	"kubocd/internal/kuboschema"
	"kubocd/internal/misc"
	"kubocd/internal/tmpl"
)

// KcdTemplateMap A template where expected result is a map[string]interface{}.
// May be a string or a map[string]interface{}
type KcdTemplateMap interface{}

// KcdTemplateBool A template where expected result is a boolean
type KcdTemplateBool string

// KcdTemplateString A template where expected result is a string
type KcdTemplateString string

// KcdTemplateStringList A template where expected result is a []string
// May be a string or a []string
type KcdTemplateStringList interface{}

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
		// Application description. Can be completed by ParametersSchema descriptions
		Description string `json:"description,omitempty"`
		// A template aimed to be rendered on deployment.
		// Intended to provide user with usage information // (Access link, configuration, ....)
		// 0ne and only one of the properties must be defined
		Usage KcdTemplateString `json:"usage,omitempty"`
		// Prevent deletion
		// Default: false
		// Act as default for corresponding Release value
		Protected bool `json:"protected"`
		// Allow Release.spec.parameters validation. And provide default values
		ParametersSchema kuboschema.KuboSchema `json:"parametersSchema,omitempty"`
		// Allow context validation. And provide default values
		ContextSchema kuboschema.KuboSchema `json:"contextSchema,omitempty"`
		// List of modules (HelmChart) included in the application
		// required: true
		Modules []*Module `json:"modules"`
		// List if role we provide
		Roles KcdTemplateStringList `json:"roles,omitempty"`
		// List of role we depend on
		Dependencies KcdTemplateStringList `json:"dependencies,omitempty"`
		// A template snippet which will be added at the beginning of all templates
		// Intended to be used to compute some global values
		TemplateHeader string `json:"templateHeader,omitempty"`
	} `yaml:"spec" json:"spec"`
	// ------------------- Private part
	templates *applicationTemplates
}

type ChartRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (app *Application) Groom() error {
	// ------------------------ Normalize
	if app.Spec.Roles == nil {
		app.Spec.Roles = []string{}
	}
	if app.Spec.Dependencies == nil {
		app.Spec.Dependencies = []string{}
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
		module := app.Spec.Modules[idx]
		err := app.Spec.Modules[idx].groom(app, idx)
		if err != nil {
			return fmt.Errorf("module '%s': %w", app.Spec.Modules[idx].Name, err)
		}
		_, ok := moduleByName[module.Name]
		if ok {
			return fmt.Errorf("duplicate module name: %s", module.Name)
		}
		moduleByName[module.Name] = app.Spec.Modules[idx]
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
	app.templates = &applicationTemplates{}
	app.templates.usage, err = tmpl.New("", string(app.Spec.Usage), app.Spec.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'usage' template: %w", err)
	}
	app.templates.roles, err = tmpl.NewFromAny("", app.Spec.Roles, app.Spec.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'roles' template: %w", err)
	}
	app.templates.dependencies, err = tmpl.NewFromAny("", app.Spec.Dependencies, app.Spec.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'dependencies' template: %w", err)
	}
	return nil
}

type applicationTemplates struct {
	usage        tmpl.Tmpl
	roles        tmpl.Tmpl
	dependencies tmpl.Tmpl
}

// Rendered object is a proxy for a release e of an application.
// Aim is to concentrate all error detection in its constructor
// Standard way should be to have Getters on application and module object.
// But each getter may generate an error, thus complicate the code.
type Rendered struct {
	Usage                string
	Roles                []string
	Dependencies         []string
	ModuleRenderedByName map[string]*ModuleRendered
}

func (app *Application) Render(model map[string]interface{}) (*Rendered, error) {
	r := &Rendered{
		ModuleRenderedByName: make(map[string]*ModuleRendered),
	}
	var err error
	r.Usage, err = app.templates.usage.RenderToText(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'usage' template: %w", err)
	}
	var txt string
	r.Roles, txt, err = app.templates.roles.RenderToStringList(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'roles' template: %w (%s)", err, txt)
	}
	r.Dependencies, txt, err = app.templates.dependencies.RenderToStringList(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'dependencies' template: %w (%s)", err, txt)
	}

	for _, module := range app.Spec.Modules {
		//fmt.Printf("*********************** module.name %s\n", module.Name)
		r.ModuleRenderedByName[module.Name], err = module.Render(model)
		if err != nil {
			return nil, fmt.Errorf("module '%s': %w", module.Name, err)
		}
	}
	return r, nil
}
