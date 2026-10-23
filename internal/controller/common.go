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

package controller

import (
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/misc"
	"reflect"
	"regexp"
	"slices"
	"strings"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// apiWithheld stands for the message of a failed request to the API server,
// which can quote a value of the object, or a name built from rendered text.
const apiWithheld = "message withheld, as it can quote the object"

// apiFieldSegment matches a segment of a field path that names a field, with
// the index or the map key that can follow it.
var apiFieldSegment = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)(\[.*)?$`)

// apiReasons are the reasons that the API server gives. An admission webhook
// can give any text as the reason of its denial.
var apiReasons = map[metav1.StatusReason]bool{
	metav1.StatusReasonUnauthorized: true, metav1.StatusReasonForbidden: true, metav1.StatusReasonNotFound: true,
	metav1.StatusReasonAlreadyExists: true, metav1.StatusReasonConflict: true, metav1.StatusReasonGone: true,
	metav1.StatusReasonInvalid: true, metav1.StatusReasonServerTimeout: true, metav1.StatusReasonStoreReadError: true,
	metav1.StatusReasonTimeout: true, metav1.StatusReasonTooManyRequests: true, metav1.StatusReasonBadRequest: true,
	metav1.StatusReasonMethodNotAllowed: true, metav1.StatusReasonNotAcceptable: true,
	metav1.StatusReasonRequestEntityTooLarge: true, metav1.StatusReasonUnsupportedMediaType: true,
	metav1.StatusReasonInternalError: true, metav1.StatusReasonExpired: true, metav1.StatusReasonServiceUnavailable: true,
}

// apiSections are, for the metadata, the spec and the status of an object, the
// names of the fields that the kinds the controller writes declare.
var apiSections = declaredFields(&fluxv2.HelmRelease{}, &kv1alpha1.Release{}, &kv1alpha1.Connection{},
	&kv1alpha1.ClusterConnection{})

// apiError describes a failed request to the API server by the reason of the
// answer and the fields at fault, without its message.
func apiError(err error) string {
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return "the request to the API server failed, " + apiWithheld
	}
	reason := status.Status().Reason
	if !apiReasons[reason] {
		reason = "Unknown"
	}
	fields := make([]string, 0)
	if details := status.Status().Details; details != nil {
		for _, cause := range details.Causes {
			if field := apiField(cause.Field); field != "" && !slices.Contains(fields, field) {
				fields = append(fields, field)
			}
		}
	}
	slices.Sort(fields)
	if len(fields) > 0 {
		return fmt.Sprintf("the API server answered %s for %s, %s", reason, strings.Join(fields, ", "), apiWithheld)
	}
	return fmt.Sprintf("the API server answered %s, %s", reason, apiWithheld)
}

// apiField keeps of a field path, such as spec.timeout, its section,
// metadata, spec or status, and the field of the section that follows when a
// kind that the controller writes declares it, without index nor map key. A
// deeper segment, or one that no kind declares, can be a map key from data.
func apiField(path string) string {
	segments := strings.SplitN(path, ".", 3)
	section := apiFieldName(segments[0])
	fields, ok := apiSections[section]
	if !ok {
		return ""
	}
	if len(segments) > 1 {
		if field := apiFieldName(segments[1]); fields[field] {
			return section + "." + field
		}
	}
	return section
}

// apiFieldName is the field that a segment of a field path names, without the
// index or the map key that can follow it.
func apiFieldName(segment string) string {
	m := apiFieldSegment.FindStringSubmatch(segment)
	if m == nil {
		return ""
	}
	return m[1]
}

// declaredFields returns, by section of the objects given (metadata, spec and
// status), the JSON names of the fields that the type of the section declares.
func declaredFields(objects ...interface{}) map[string]map[string]bool {
	sections := map[string]map[string]bool{}
	for _, object := range objects {
		objectType := reflect.TypeOf(object).Elem()
		for i := range objectType.NumField() {
			field := objectType.Field(i)
			section := jsonName(field)
			if section != "metadata" && section != "spec" && section != "status" {
				continue
			}
			if sections[section] == nil {
				sections[section] = map[string]bool{}
			}
			addJSONNames(field.Type, sections[section])
		}
	}
	return sections
}

// addJSONNames adds to names the JSON names of the fields of the struct t,
// through embedded structs.
func addJSONNames(t reflect.Type, names map[string]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := range t.NumField() {
		field := t.Field(i)
		switch name := jsonName(field); {
		case name == "" && field.Anonymous:
			addJSONNames(field.Type, names)
		case name != "" && name != "-":
			names[name] = true
		}
	}
}

// jsonName is the name of field in JSON, empty for an embedded field.
func jsonName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	return name
}

func Merge(base map[string]interface{}, addon *apiextensionsv1.JSON) (map[string]interface{}, error) {
	if addon == nil {
		return base, nil
	}
	inc := make(map[string]interface{})
	err := yaml.Unmarshal(addon.Raw, &inc)
	if err != nil {
		return nil, err
	}
	return misc.MergeMaps(base, inc), nil
}
