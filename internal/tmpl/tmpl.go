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

package tmpl

import (
	"bytes"
	"fmt"
	"kubocd/internal/misc"
	"strconv"
	"strings"
	"text/template"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

type Tmpl interface {
	RenderToText(model map[string]interface{}) (string, error)
	RenderToSingleLine(model map[string]interface{}) (string, error)
	RenderToMap(model map[string]interface{}) (map[string]interface{}, string, error)
	RenderToBool(model map[string]interface{}, def bool) (bool, string, error)
	RenderToInt(model map[string]interface{}, def int) (int, string, error)
	RenderToStringList(model map[string]interface{}) ([]string, string, error)
	SetDelimiters(d1, d2 string)
	RenderToDuration(model map[string]interface{}) (metav1.Duration, string, error)
}

var _ Tmpl = &tmpl{}

type tmpl struct {
	template *template.Template
}

// New parses header followed by tempText as a template, which can call the
// given functions.
func New(templateName string, tempText string, header string, functions Functions) (Tmpl, error) {
	var err error
	tt := &tmpl{}
	tt.template = template.New(templateName).Option("missingkey=zero").Funcs(functions.funcs)
	tempText = fmt.Sprintf("%s%s", header, tempText)
	tt.template, err = tt.template.Parse(tempText)
	if err != nil {
		return nil, err
	}
	return tt, nil
}

// NewFromAny is New for a template given as a string, or as a map or a list
// holding template strings.
func NewFromAny(templateName string, src interface{}, header string, functions Functions) (Tmpl, error) {
	if src == nil {
		return New(templateName, "", "", functions)
	}
	str, ok := src.(string)
	if ok {
		return New(templateName, str, header, functions)
	}
	m, ok := src.(map[string]interface{})
	if ok {
		return New(templateName, string(misc.Any2Yaml(m)), header, functions)
	}
	ia, ok := src.([]interface{})
	if ok {
		return New(templateName, string(misc.Any2Yaml(ia)), header, functions)
	}
	sa, ok := src.([]string)
	if ok {
		return New(templateName, string(misc.Any2Yaml(sa)), header, functions)
	}
	return nil, fmt.Errorf("invalid template object type %T", src)
}

func (tt *tmpl) SetDelimiters(d1, d2 string) {
	tt.template.Delims(d1, d2)
}

// RenderToText executes the template on its own copy of model, which must
// hold plain data only.
func (tt *tmpl) RenderToText(model map[string]interface{}) (string, error) {
	data, err := plainCopy(model)
	if err != nil {
		return "", err
	}
	buf := &bytes.Buffer{}
	err = tt.template.Execute(buf, data)
	if err != nil {
		return "", err
	}
	// Work around the issue where Go will emit "<no value>" even if Options(missing=zero)
	// is set. Since missing=error will never get here, we do not need to handle
	// the Strict case.
	return strings.ReplaceAll(buf.String(), "<no value>", ""), nil
}

func (tt *tmpl) RenderToSingleLine(model map[string]interface{}) (string, error) {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(txt), nil
}

// Helper functions
// NB: the intermediate text template is returned, to be displayed in case of error on yaml

func (tt *tmpl) RenderToMap(model map[string]interface{}) (map[string]interface{}, string, error) {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return nil, txt, err
	}
	m := make(map[string]interface{})
	err = yaml.Unmarshal([]byte(txt), &m)
	if err != nil {
		return nil, txt, err
	}
	return m, txt, nil
}

func (tt *tmpl) RenderToStringList(model map[string]interface{}) ([]string, string, error) {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return nil, txt, err
	}
	a := make([]string, 0)
	err = yaml.Unmarshal([]byte(txt), &a)
	if err != nil {
		return nil, txt, err
	}
	return a, txt, nil
}

func (tt *tmpl) RenderToBool(model map[string]interface{}, def bool) (bool, string, error) {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return false, txt, err
	}
	txt = strings.TrimSpace(txt)
	if txt == "" {
		return def, txt, nil
	}
	b, err := strconv.ParseBool(txt)
	if err != nil {
		return false, txt, err
	}
	return b, txt, nil
}

func (tt *tmpl) RenderToInt(model map[string]interface{}, def int) (int, string, error) {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return 0, txt, err
	}
	txt = strings.TrimSpace(txt)
	if txt == "" {
		return def, txt, nil
	}
	i, err := strconv.Atoi(txt)
	if err != nil {
		return 0, txt, err
	}
	return i, txt, nil
}

func (tt *tmpl) RenderToDuration(model map[string]interface{}) (metav1.Duration, string, error) {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return metav1.Duration{
			Duration: time.Duration(0),
		}, txt, err
	}
	txt = strings.TrimSpace(txt)
	d, err := time.ParseDuration(txt)
	if err != nil {
		return metav1.Duration{
			Duration: time.Duration(0),
		}, txt, err
	}
	return metav1.Duration{
		Duration: d,
	}, txt, nil
}
