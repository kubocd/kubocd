package application

import (
	"fmt"
	"kubocd/internal/cache"
	"kubocd/internal/kuboschema"
	"strings"
	"time"
)

type AppContainer struct {
	insertionTime     time.Time
	Application       *Application           `json:"application"` // The groomed version
	Revision          string                 `json:"revision"`
	Status            *Status                `json:"status"`
	DefaultParameters map[string]interface{} `json:"defaultParameters"`
	DefaultContext    map[string]interface{} `json:"defaultContext"`
}

var _ cache.Entry = &AppContainer{}

func (a *AppContainer) GetInsertionTime() time.Time {
	return a.insertionTime
}

func (a *AppContainer) SetInsertionTime(t time.Time) {
	a.insertionTime = t
}

func (a *AppContainer) String() string {
	return fmt.Sprintf("Application %s:%s (%s)", a.Application.Metadata.Name, a.Application.Metadata.Version, a.Revision)
}

func (a *AppContainer) SetApplication(app *Application, status *Status, revision string) error {
	a.Application = app
	split := strings.Split(revision, ":") // Revision pattern: 0.1.1@sha256:3d8dd2f0a9a0015fa13a7e52ae449707b4f0f77b4da0fc6427d9ed949d159265
	a.Revision = split[1][:12]            // First 12 chart should be enough for uniqueness
	a.Status = status
	err := a.Application.Validate()
	if err != nil {
		return err
	}
	a.Application.Groom()
	a.DefaultParameters, err = kuboschema.Defaulter(a.Application.Spec.ParametersSchema)
	if err != nil {
		return err
	}
	a.DefaultContext, err = kuboschema.Defaulter(a.Application.Spec.ContextSchema)
	if err != nil {
		return err
	}
	return nil
}
