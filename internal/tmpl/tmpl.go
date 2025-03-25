package tmpl

import (
	"bytes"
	"fmt"
	"kubocd/internal/misc"
	"sigs.k8s.io/yaml"
	"strconv"
	"strings"
	"text/template"
)

type Tmpl interface {
	RenderToText(model map[string]interface{}) (string, error)
	RenderToSingleLine(model map[string]interface{}) (string, error)
	RenderToMap(model map[string]interface{}) (map[string]interface{}, string, error)
	RenderToBool(model map[string]interface{}) (bool, string, error)
	RenderToStringList(model map[string]interface{}) ([]string, string, error)
	SetDelimiters(d1, d2 string)
}

var _ Tmpl = &tmpl{}

type tmpl struct {
	template *template.Template
}

func New(templateName string, tempText string, header string) (Tmpl, error) {
	var err error
	tt := &tmpl{}
	tt.template = template.New(templateName).Option("missingkey=zero").Funcs(funcMap())
	tempText = fmt.Sprintf("%s%s", header, tempText)
	tt.template, err = tt.template.Parse(tempText)
	if err != nil {
		return nil, err
	}
	return tt, nil
}

func NewFromAny(templateName string, src interface{}, header string) (Tmpl, error) {
	if src == nil {
		return New(templateName, "", "")
	}
	str, ok := src.(string)
	if ok {
		return New(templateName, str, header)
	}
	m, ok := src.(map[string]interface{})
	if ok {
		return New(templateName, string(misc.Any2Yaml(m)), header)
	}
	ia, ok := src.([]interface{})
	if ok {
		return New(templateName, string(misc.Any2Yaml(ia)), header)
	}
	sa, ok := src.([]string)
	if ok {
		return New(templateName, string(misc.Any2Yaml(sa)), header)
	}
	return nil, fmt.Errorf("invalid template object type %T", src)
}

func (tt *tmpl) SetDelimiters(d1, d2 string) {
	tt.template.Delims(d1, d2)
}

func (tt *tmpl) RenderToText(model map[string]interface{}) (string, error) {
	buf := &bytes.Buffer{}
	err := tt.template.Execute(buf, model)
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

func (tt *tmpl) RenderToBool(model map[string]interface{}) (bool, string, error) {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return false, txt, err
	}
	txt = strings.TrimSpace(txt)
	b, err := strconv.ParseBool(txt)
	if err != nil {
		return false, txt, err
	}
	return b, txt, nil
}
