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

package cmn

import (
	"fmt"
	"kubocd/internal/misc"
	"os"
	"path"
)

func Dump(output string, fileName string, ap interface{}) {
	var out string
	if fileName != "" {
		out = fmt.Sprintf("# ====================================  %s:\n---\n%s\n", fileName, misc.Any2Yaml(ap))
	} else {
		out = fmt.Sprintf("---\n%s\n", misc.Any2Yaml(ap))
	}
	if output != "" {
		target := path.Join(output, fileName)
		err := os.WriteFile(target, []byte(out), 0644)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Create %s\n", target)
	} else {
		fmt.Print(out)
	}
}
func DumpTxt(output string, fileName string, txt string) {
	var out string
	if fileName != "" {
		out = fmt.Sprintf("# ====================================  %s:\n%s\n", fileName, txt)
	} else {
		out = fmt.Sprintf("---\n%s\n", txt)
	}
	if output != "" {
		target := path.Join(output, fileName)
		err := os.WriteFile(target, []byte(out), 0644)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Create %s\n", target)
	} else {
		fmt.Print(out)
	}
}
