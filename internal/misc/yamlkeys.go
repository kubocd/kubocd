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

package misc

import (
	"errors"
	"strconv"

	yamlv2 "go.yaml.in/yaml/v2"
)

// ErrYAMLKeysCollide reports a YAML map with two keys that sigs.k8s.io/yaml
// converts to the same string. It would keep one of their values at random.
var ErrYAMLKeysCollide = errors.New(`has a map whose keys are the same once converted to strings, such as 1 and "1", or true and "true"`)

// ErrYAMLKeyUnsupported reports a YAML map key that sigs.k8s.io/yaml cannot
// convert to a string. The error of sigs.k8s.io/yaml names one such key at
// random.
var ErrYAMLKeyUnsupported = errors.New("has a map key that cannot be converted to a string, such as null")

// CheckYAMLKeys checks the map keys of the YAML document doc before
// sigs.k8s.io/yaml converts them to strings. It decodes doc with
// go.yaml.in/yaml/v2, the YAML library of sigs.k8s.io/yaml, which keeps the
// keys typed. It returns ErrYAMLKeysCollide when a map has two keys that give
// the same string, else ErrYAMLKeyUnsupported when a key cannot be converted.
// Neither depends on the order of the keys. When doc does not decode, it
// returns the error of go.yaml.in/yaml/v2, which can quote the document.
func CheckYAMLKeys(doc []byte) error {
	var typed interface{}
	if err := yamlv2.Unmarshal(doc, &typed); err != nil {
		return err
	}
	var check keyCheck
	if check.collide(typed) {
		return ErrYAMLKeysCollide
	}
	if check.unsupported {
		return ErrYAMLKeyUnsupported
	}
	return nil
}

type keyCheck struct {
	unsupported bool
}

// collide reports whether node holds a map with two keys that give the same
// string. When it does not, every key of node has been seen, and unsupported
// tells whether one of them cannot be converted.
func (c *keyCheck) collide(node interface{}) bool {
	switch n := node.(type) {
	case map[interface{}]interface{}:
		seen := make(map[string]bool, len(n))
		for key, value := range n {
			if s, ok := jsonKey(key); !ok {
				c.unsupported = true
			} else if seen[s] {
				return true
			} else {
				seen[s] = true
			}
			if c.collide(value) {
				return true
			}
		}
	case []interface{}:
		for _, item := range n {
			if c.collide(item) {
				return true
			}
		}
	}
	return false
}

// jsonKey is the string sigs.k8s.io/yaml makes of a map key decoded by
// go.yaml.in/yaml/v2 (convertToJSONableObject). It fails on the other types.
func jsonKey(key interface{}) (string, bool) {
	switch k := key.(type) {
	case string:
		return k, true
	case int:
		return strconv.Itoa(k), true
	case int64:
		return strconv.FormatInt(k, 10), true
	case float64:
		switch s := strconv.FormatFloat(k, 'g', -1, 32); s {
		case "+Inf":
			return ".inf", true
		case "-Inf":
			return "-.inf", true
		case "NaN":
			return ".nan", true
		default:
			return s, true
		}
	case bool:
		return strconv.FormatBool(k), true
	}
	return "", false
}
