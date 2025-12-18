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
	"kubocd/internal/misc"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

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
