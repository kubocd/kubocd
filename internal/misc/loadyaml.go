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

package misc

import (
	"errors"
	"fmt"
	"io"
	"os"

	"sigs.k8s.io/yaml"
)

// LoadYaml decodes the YAML file fileName into each of targets, as written:
// a ${...} sequence stays literal.
func LoadYaml(fileName string, targets ...interface{}) error {
	content, err := os.ReadFile(fileName)
	if err != nil {
		return err
	}
	for _, target := range targets {
		err = YamlDecode(string(content), target)
		if err != nil {
			return err
		}
	}
	return nil
}

// YamlDecode decodes the YAML document content into target, rejecting unknown
// fields, and map keys that are the same once converted to strings or that
// cannot be converted.
func YamlDecode(content string, target interface{}) error {
	if err := CheckYAMLKeys([]byte(content)); errors.Is(err, ErrYAMLKeysCollide) || errors.Is(err, ErrYAMLKeyUnsupported) {
		return fmt.Errorf("the YAML document %w", err)
	}
	err := yaml.UnmarshalStrict([]byte(content), target)
	if err != nil {
		if err != io.EOF { // EOF is not an error. Just an empty file (with or without comment)
			return err
		}
	}
	return nil
}
