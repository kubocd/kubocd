package kubopackage

import (
	"fmt"
	"kubocd/internal/global"
	"kubocd/internal/kuboschema"
	"kubocd/internal/misc"
	"kubocd/internal/tmpl"
)

// KcdTemplateMap A template where expected result is a map[string]interface{}.
// Maybe a string or a map[string]interface{}
type KcdTemplateMap interface{}

// KcdTemplateBool A template where expected result is a boolean
type KcdTemplateBool string

// KcdTemplateString A template where expected result is a string
type KcdTemplateString string

// KcdTemplateStringList A template where expected result is a []string
// May be a string or a []string
type KcdTemplateStringList interface{}

// ------------------------------------------------

type Package struct {
	// required:true
	ApiVersion string `json:"apiVersion"` // v1alpha1
	Metadata   struct {
		// This is NOT a k8s object. To overemphasis this, we use Type instead of Kind. and define it as metadata
		// required:true
		Type string `json:"type"` // Always 'Package'
		// required:true
		Name string `json:"name"`
		// required:true
		Version string `json:"version"`
	} `json:"metadata"`
	Spec struct {
		// Package description. Can be completed by ParametersSchema descriptions
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
		// List of modules (HelmChart) included in the package
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
	templates *packageTemplates
}

type ChartRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (pck *Package) Groom() error {
	// ------------------------ Normalize
	if pck.Spec.Roles == nil {
		pck.Spec.Roles = []string{}
	}
	if pck.Spec.Dependencies == nil {
		pck.Spec.Dependencies = []string{}
	}
	// Validation
	if pck.ApiVersion != global.PackageApiVersion {
		return fmt.Errorf("'apiVersion' must be %s (is '%s')", global.PackageApiVersion, pck.ApiVersion)
	}
	if pck.Metadata.Type != global.PackageType {
		return fmt.Errorf("'type' must be %s", global.PackageType)
	}
	x := misc.CountNonZero(pck.Metadata.Name, pck.Metadata.Version)
	if x != 2 {
		return fmt.Errorf("'name' and 'version' must be set")
	}
	if !misc.ValidateK8sName(pck.Metadata.Name) {
		return fmt.Errorf("invalid 'name'. Must contain only alphanumeric characters, dashes and underscores")
	}
	var err error
	if pck.Spec.ParametersSchema != nil {
		pck.Spec.ParametersSchema, err = kuboschema.Kubo2openAPI(pck.Spec.ParametersSchema, false)
		if err != nil {
			return fmt.Errorf("invalid 'parametersSchema': %w", err)
		}
	}
	if pck.Spec.ContextSchema != nil {
		pck.Spec.ContextSchema, err = kuboschema.Kubo2openAPI(pck.Spec.ContextSchema, true)
		if err != nil {
			return fmt.Errorf("invalid 'contextSchema': %w", err)
		}
	}
	if pck.Spec.Modules == nil || len(pck.Spec.Modules) == 0 {
		return fmt.Errorf("a package must have at least one module")
	}
	// ------- Now, check modules
	moduleByName := make(map[string]*Module)
	for idx := range pck.Spec.Modules {
		module := pck.Spec.Modules[idx]
		err := pck.Spec.Modules[idx].groom(pck, idx)
		if err != nil {
			return fmt.Errorf("module '%s': %w", pck.Spec.Modules[idx].Name, err)
		}
		_, ok := moduleByName[module.Name]
		if ok {
			return fmt.Errorf("duplicate module name: %s", module.Name)
		}
		moduleByName[module.Name] = pck.Spec.Modules[idx]
	}
	pck.templates = &packageTemplates{}
	pck.templates.usage, err = tmpl.New("", string(pck.Spec.Usage), pck.Spec.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'usage' template: %w", err)
	}
	pck.templates.roles, err = tmpl.NewFromAny("", pck.Spec.Roles, pck.Spec.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'roles' template: %w", err)
	}
	pck.templates.dependencies, err = tmpl.NewFromAny("", pck.Spec.Dependencies, pck.Spec.TemplateHeader)
	if err != nil {
		return fmt.Errorf("could not parse 'dependencies' template: %w", err)
	}
	// NB We can't test intra-module dependencies here, as it is a template. Will be checked after rendering
	return nil
}

type packageTemplates struct {
	usage        tmpl.Tmpl
	roles        tmpl.Tmpl
	dependencies tmpl.Tmpl
}

// Rendered object is a proxy for a release e of a package.
// Aim is to concentrate all error detection in its constructor
// Standard way should be to have Getters on package and module object.
// But each getter may generate an error, thus complicate the code.
type Rendered struct {
	Usage                string
	Roles                []string
	Dependencies         []string
	ModuleRenderedByName map[string]*ModuleRendered
}

func (pck *Package) Render(model map[string]interface{}) (*Rendered, error) {
	r := &Rendered{
		ModuleRenderedByName: make(map[string]*ModuleRendered),
	}
	var err error
	r.Usage, err = pck.templates.usage.RenderToText(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'usage' template: %w", err)
	}
	var txt string
	r.Roles, txt, err = pck.templates.roles.RenderToStringList(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'roles' template: %w (%s)", err, txt)
	}
	r.Dependencies, txt, err = pck.templates.dependencies.RenderToStringList(model)
	if err != nil {
		return nil, fmt.Errorf("could not render 'dependencies' template: %w (%s)", err, txt)
	}
	for _, module := range pck.Spec.Modules {
		//fmt.Printf("*********************** module.name %s\n", module.Name)
		r.ModuleRenderedByName[module.Name], err = module.Render(model)
		if err != nil {
			return nil, fmt.Errorf("module '%s': %w", module.Name, err)
		}
	}
	// Check intra module dependencies
	for _, module := range pck.Spec.Modules {
		rendered, ok := r.ModuleRenderedByName[module.Name]
		if !ok {
			panic(fmt.Sprintf("missng module of name %s", module.Name)) // Should not occur
		}
		for _, dep := range rendered.DependsOn {
			_, ok = r.ModuleRenderedByName[dep]
			if !ok {
				return nil, fmt.Errorf("module '%s': dependsOn '%s': module does not exists", module.Name, dep)
			}
		}
	}
	return r, nil
}
