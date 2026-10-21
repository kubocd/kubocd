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
	"errors"
	"fmt"
	"kubocd/internal/misc"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Tmpl is a parsed template. The errors of its Render methods give the place
// and the cause of a failure, never the rendered text, which holds data.
type Tmpl interface {
	RenderToText(model map[string]interface{}) (string, error)
	RenderToSingleLine(model map[string]interface{}) (string, error)
	RenderToMap(model map[string]interface{}) (map[string]interface{}, error)
	RenderToBool(model map[string]interface{}, def bool) (bool, error)
	RenderToStringList(model map[string]interface{}) ([]string, error)
	RenderToDuration(model map[string]interface{}) (metav1.Duration, error)
	RenderInto(model map[string]interface{}, target interface{}) error
}

var _ Tmpl = &tmpl{}

type tmpl struct {
	template *template.Template
	header   string
}

var parseErrorLine = regexp.MustCompile(`^template: [^:]*:(\d+):`)

// New parses header followed by tempText as a template, which can call the
// given functions.
func New(templateName string, tempText string, header string, functions Functions) (Tmpl, error) {
	if functions.funcs == nil {
		return nil, errNoFunctions
	}
	var err error
	tt := &tmpl{header: header}
	tt.template = template.New(templateName).Option("missingkey=zero").Funcs(functions.funcs)
	tempText = fmt.Sprintf("%s%s", header, tempText)
	tt.template, err = tt.template.Parse(tempText)
	if err != nil {
		if place := headerPlace(err, header); place != "" {
			return nil, fmt.Errorf("%s: %w", place, err)
		}
		return nil, err
	}
	return tt, nil
}

// headerPlace tells whether a parse error lies in header, which is parsed
// followed by the template it prefixes. A parse error gives a line only: when
// header has no final newline, its last line is also the first line of the
// template, and an error there can lie in either.
func headerPlace(err error, header string) string {
	m := parseErrorLine.FindStringSubmatch(err.Error())
	if m == nil {
		return ""
	}
	line, _ := strconv.Atoi(m[1])
	lines, shared := headerLines(header)
	switch {
	case line < lines, line == lines && !shared:
		return "in templateHeader"
	case line == lines:
		return "in templateHeader or on the first line of the template"
	}
	return ""
}

// headerLines returns the number of lines of header, and whether the last one
// has no final newline, the template then starting on that line.
func headerLines(header string) (int, bool) {
	lines := strings.Count(header, "\n")
	if header != "" && !strings.HasSuffix(header, "\n") {
		return lines + 1, true
	}
	return lines, false
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
		return "", tt.executionError(err)
	}
	// Work around the issue where Go will emit "<no value>" even if Options(missing=zero)
	// is set. Since missing=error will never get here, we do not need to handle
	// the Strict case.
	return strings.ReplaceAll(buf.String(), "<no value>", ""), nil
}

// withheld stands for a message of text/template or of a sprig function,
// which can quote the template data.
const withheld = "message withheld, as it can quote the data"

// execErrorStart matches the start of an execution error of text/template:
// the line and the byte offset in the line of the failing action.
var execErrorStart = regexp.MustCompile(`^template: [^:]*:(\d+):(\d+): `)

// calledFunction matches the end of the execution error of a function call,
// once the message of the function's own error is cut off.
var calledFunction = regexp.MustCompile(`error calling ([A-Za-z_][A-Za-z0-9_]*)$`)

// execCategories name the execution errors of text/template that do not come
// from a function, by the fixed start of their message.
var execCategories = []struct{ start, category string }{
	{"range can't iterate over ", "range over a value that is not a list or a map"},
	{"wrong type for value; ", "argument of the wrong type"},
	{"invalid value; expected ", "argument of the wrong type"},
	{"can't evaluate field ", "field of a value that is not a map"},
	{"nil pointer evaluating ", "field of a missing value"},
	{"wrong number of args for ", "wrong number of arguments"},
}

// executionError turns an execution error of text/template into the place of
// the failing action and a category. The messages of text/template and of
// sprig functions are left out, as they can quote the data. Those of KuboCD
// functions, and those written with fail and required, are kept as written.
func (tt *tmpl) executionError(err error) error {
	reason := "execution error"
	var execErr template.ExecError
	if errors.As(err, &execErr) {
		reason = execReason(execErr.Err)
	}
	var shown *shownError
	if errors.As(err, &shown) {
		reason += ": " + shown.msg
	} else {
		reason += ", " + withheld
	}
	if place := tt.execPlace(err.Error()); place != "" {
		return fmt.Errorf("%s: %s", place, reason)
	}
	return errors.New(reason)
}

// execReason names what failed: the function an error comes from, read in the
// text before the message of that error, or a category of text/template.
func execReason(err error) string {
	msg := err.Error()
	if cause := errors.Unwrap(err); cause != nil {
		if head, ok := strings.CutSuffix(msg, ": "+cause.Error()); ok {
			if m := calledFunction.FindStringSubmatch(head); m != nil {
				return "error calling " + m[1]
			}
		}
		return "function error"
	}
	for _, c := range execCategories {
		if strings.Contains(msg, ">: "+c.start) {
			return c.category
		}
	}
	return "execution error"
}

// execPlace locates the failing action, from the start of an execution error:
// in the templateHeader, or in the template that follows it, each counted from
// its own first line. Columns count from 1.
func (tt *tmpl) execPlace(msg string) string {
	m := execErrorStart.FindStringSubmatch(msg)
	if m == nil {
		return ""
	}
	line, _ := strconv.Atoi(m[1])
	offset, _ := strconv.Atoi(m[2])
	lines, shared := headerLines(tt.header)
	lastLineLength := len(tt.header) - strings.LastIndex(tt.header, "\n") - 1
	switch {
	case line < lines || line == lines && (!shared || offset < lastLineLength):
		return fmt.Sprintf("in templateHeader, line %d, column %d", line, offset+1)
	case line == lines:
		return fmt.Sprintf("line 1, column %d", offset-lastLineLength+1)
	case shared:
		return fmt.Sprintf("line %d, column %d", line-lines+1, offset+1)
	}
	return fmt.Sprintf("line %d, column %d", line-lines, offset+1)
}

func (tt *tmpl) RenderToSingleLine(model map[string]interface{}) (string, error) {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(txt), nil
}

func (tt *tmpl) RenderToMap(model map[string]interface{}) (map[string]interface{}, error) {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return nil, err
	}
	m := make(map[string]interface{})
	if err := yaml.Unmarshal([]byte(txt), &m); err != nil {
		return nil, yamlError(txt, "is not a YAML map")
	}
	return m, nil
}

func (tt *tmpl) RenderToStringList(model map[string]interface{}) ([]string, error) {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return nil, err
	}
	a := make([]string, 0)
	if err := yaml.Unmarshal([]byte(txt), &a); err != nil {
		return nil, yamlError(txt, "is not a YAML list of strings")
	}
	return a, nil
}

// RenderInto renders the template and decodes the result into target,
// rejecting unknown fields.
func (tt *tmpl) RenderInto(model map[string]interface{}, target interface{}) error {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return err
	}
	if err := yaml.UnmarshalStrict([]byte(txt), target); err != nil {
		return yamlError(txt, "does not have the expected structure")
	}
	return nil
}

func (tt *tmpl) RenderToBool(model map[string]interface{}, def bool) (bool, error) {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return false, err
	}
	txt = strings.TrimSpace(txt)
	if txt == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(txt)
	if err != nil {
		return false, errors.New("the rendered text is not a boolean (true or false)")
	}
	return b, nil
}

func (tt *tmpl) RenderToDuration(model map[string]interface{}) (metav1.Duration, error) {
	txt, err := tt.RenderToText(model)
	if err != nil {
		return metav1.Duration{}, err
	}
	d, err := time.ParseDuration(strings.TrimSpace(txt))
	if err != nil {
		return metav1.Duration{}, errors.New("the rendered text is not a duration (such as 90s or 2m30s)")
	}
	return metav1.Duration{Duration: d}, nil
}

// yamlSyntaxError matches the start of a syntax error of the YAML parser, as
// sigs.k8s.io/yaml reports it, with its line. The other errors of the parser
// can quote the rendered text, a line number included.
var yamlSyntaxError = regexp.MustCompile(`^error converting YAML to JSON: yaml: line (\d+): `)

// yamlError reports rendered text that does not decode to the expected value,
// described by problem. The message of the parser is left out, as it can
// quote the rendered text: only the line of a syntax error is kept.
func yamlError(txt string, problem string) error {
	var anything interface{}
	err := yaml.Unmarshal([]byte(txt), &anything)
	if err == nil {
		return fmt.Errorf("the rendered text %s", problem)
	}
	if m := yamlSyntaxError.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Errorf("the rendered text is not valid YAML (line %s)", m[1])
	}
	return errors.New("the rendered text is not valid YAML")
}
