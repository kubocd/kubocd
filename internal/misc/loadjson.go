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
	"encoding/json"
	"io"
	"os"
)

// LoadJson decodes the JSON file fileName into target, as written: a ${...}
// sequence stays literal.
func LoadJson(fileName string, target interface{}) error {
	content, err := os.ReadFile(fileName)
	if err != nil {
		return err
	}
	return JsonDecode(string(content), target)
}

func JsonDecode(content string, target interface{}) error {
	err := json.Unmarshal([]byte(content), target)
	if err != nil {
		if err != io.EOF { // EOF is not an error. Just an empty file (with or without comment)
			return err
		}
	}
	return nil

}
