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
	"kubocd/internal/misc"
	"regexp"
	"slices"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/yaml"
)

// apiWithheld stands for the message of a failed request to the API server,
// which can quote a value of the object, or a name built from rendered text.
const apiWithheld = "message withheld, as it can quote the object"

// apiFieldSegment matches a segment of a field path that names a field, with
// the index or the map key that can follow it.
var apiFieldSegment = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)(\[.*)?$`)

// apiError describes a failed request to the API server by the reason of the
// answer and the fields at fault, without its message.
func apiError(err error) string {
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return "the request to the API server failed, " + apiWithheld
	}
	reason := string(status.Status().Reason)
	if reason == "" {
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

// apiField keeps the first two segments of a field path, such as spec.timeout,
// without index nor map key. A deeper segment can be a map key, which can come
// from data.
func apiField(path string) string {
	segments := strings.SplitN(path, ".", 3)
	kept := make([]string, 0, 2)
	for _, segment := range segments[:min(len(segments), 2)] {
		m := apiFieldSegment.FindStringSubmatch(segment)
		if m == nil {
			return ""
		}
		kept = append(kept, m[1])
	}
	return strings.Join(kept, ".")
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
