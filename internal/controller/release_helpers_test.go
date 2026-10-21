/*
Copyright 2026 Kubotal

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
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/kubopackage"
	"kubocd/internal/kuboschema"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestExcludedFunctionInReleaseParametersIsRejected(t *testing.T) {
	for _, name := range []string{"now", "resolveDNS"} {
		t.Run(name, func(t *testing.T) {
			release := &kv1alpha1.Release{}
			release.Spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(`{"value":"{{ ` + name + ` }}"}`)}
			_, err := HandleParameters(release, map[string]interface{}{}, configstore.New(), &kubopackage.PckContainer{})
			if err == nil {
				t.Fatal("parameters using an excluded function should be rejected")
			}
			for _, want := range []string{"parameters", `function "` + name + `" not defined`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err.Error(), want)
				}
			}
		})
	}
}

func TestExcludedFunctionInConnectionRefDefaultIsRejected(t *testing.T) {
	for _, name := range []string{"now", "resolveDNS"} {
		t.Run(name, func(t *testing.T) {
			def := `{{ ` + name + ` }}`
			pckContainer := &kubopackage.PckContainer{
				DefaultParameters:    map[string]interface{}{"db": def},
				ParamConnectionDecls: []kuboschema.ConnectionDecl{{Path: []string{"db"}, Contract: "database", Default: def}},
			}
			_, err := HandleParameters(&kv1alpha1.Release{}, map[string]interface{}{}, configstore.New(), pckContainer)
			if err == nil {
				t.Fatal("a connectionRef default using an excluded function should be rejected")
			}
			for _, want := range []string{"default of connection parameter 'db'", `function "` + name + `" not defined`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err.Error(), want)
				}
			}
		})
	}
}
