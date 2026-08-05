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
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// EffectiveOutputName returns the name of the (Cluster)Connection created for
// an output: the explicit connectionName when set, the generated one otherwise.
func EffectiveOutputName(releaseName, releaseNamespace string, or *kubopackage.OutputRendered) string {
	if or.ConnectionName != "" {
		return or.ConnectionName
	}
	if or.Kind == kv1alpha1.KindClusterConnection {
		return BuildClusterConnectionName(releaseName, releaseNamespace, or.Name)
	}
	return BuildConnectionName(releaseName, or.Name)
}

// ComputeEffectiveOutputNames resolves the effective connection name of every
// output of a release and rejects invalid names and duplicates. Duplicates are
// checked across the whole release, generated and explicit names together: the
// no-adoption guard cannot catch two outputs of the same release racing for
// the same name. The result is indexed like the input slice.
func ComputeEffectiveOutputNames(releaseName, releaseNamespace string, outputs []*kubopackage.OutputRendered) ([]string, error) {
	names := make([]string, len(outputs))
	byName := make(map[string]string, len(outputs))
	for idx, or := range outputs {
		name := EffectiveOutputName(releaseName, releaseNamespace, or)
		if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
			return nil, fmt.Errorf("output '%s': invalid connection name '%s': %s", or.Name, name, strings.Join(errs, ", "))
		}
		if other, ok := byName[name]; ok {
			return nil, fmt.Errorf("outputs '%s' and '%s' resolve to the same connection name '%s'", other, or.Name, name)
		}
		byName[name] = or.Name
		names[idx] = name
	}
	return names, nil
}
