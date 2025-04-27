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
	"github.com/xeipuuv/gojsonschema"
	"kubocd/internal/cache"
	"kubocd/internal/kuboschema"
	"strings"
	"time"
)

type PckContainer struct {
	insertionTime     time.Time
	Package           *Package               `json:"package"` // The groomed version
	Revision          string                 `json:"revision"`
	Status            *Status                `json:"status"`
	DefaultParameters map[string]interface{} `json:"defaultParameters"`
	DefaultContext    map[string]interface{} `json:"defaultContext"`
	ParameterSchema   *gojsonschema.Schema   `json:"parameterSchema"`
	ContextSchema     *gojsonschema.Schema   `json:"contextSchema"`
}

var _ cache.Entry = &PckContainer{}

func (p *PckContainer) GetInsertionTime() time.Time {
	return p.insertionTime
}

func (p *PckContainer) SetInsertionTime(t time.Time) {
	p.insertionTime = t
}

func (p *PckContainer) String() string {
	return fmt.Sprintf("Package %s:%s (%s)", p.Package.Name, p.Package.Tag, p.Revision)
}

func (p *PckContainer) SetPackage(pck *Package, status *Status, revision string) error {
	p.Package = pck
	split := strings.Split(revision, ":") // Revision pattern: 0.1.1@sha256:3d8dd2f0a9a0015fa13a7e52ae449707b4f0f77b4da0fc6427d9ed949d159265
	p.Revision = split[1][:12]            // First 12 chart should be enough for uniqueness
	p.Status = status
	err := p.Package.Groom()
	if err != nil {
		return err
	}
	if pck.Schema != nil {
		p.DefaultParameters, err = kuboschema.Defaulter(pck.Schema.Parameters)
		if err != nil {
			return fmt.Errorf("defaultParameters: %w", err)
		}
		p.DefaultContext, err = kuboschema.Defaulter(pck.Schema.Context)
		if err != nil {
			return fmt.Errorf("defaultContext: %w", err)
		}
		if pck.Schema.Parameters != nil && len(pck.Schema.Parameters) > 0 {
			p.ParameterSchema, err = gojsonschema.NewSchema(gojsonschema.NewGoLoader(pck.Schema.Parameters))
			if err != nil {
				return fmt.Errorf("parameterSchema: %w", err)
			}
		}
		if pck.Schema.Context != nil && len(pck.Schema.Context) > 0 {
			p.ContextSchema, err = gojsonschema.NewSchema(gojsonschema.NewGoLoader(pck.Schema.Context))
			if err != nil {
				return fmt.Errorf("contextSchema: %w", err)
			}
		}
	} else {
		p.DefaultParameters = map[string]interface{}{}
		p.DefaultContext = map[string]interface{}{}
	}
	return nil
}

func (p *PckContainer) ValidateParameters(params map[string]interface{}) error {
	if p.ParameterSchema == nil {
		return nil
	}
	validate, err := p.ParameterSchema.Validate(gojsonschema.NewGoLoader(params))
	if err != nil {
		return err
	}
	if len(validate.Errors()) > 0 {
		return fmt.Errorf("parameters schema validation error: %s", validate.Errors()[0])
	}
	return nil
}

func (p *PckContainer) ValidateContext(context map[string]interface{}) error {
	if p.ContextSchema == nil {
		return nil
	}
	validate, err := p.ContextSchema.Validate(gojsonschema.NewGoLoader(context))
	if err != nil {
		return err
	}
	if len(validate.Errors()) > 0 {
		return fmt.Errorf("context schema validation error: %s", validate.Errors()[0])
	}
	return nil
}
