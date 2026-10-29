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

package cmn

import (
	"fmt"
	"kubocd/cmd/kubocd/cmd/tgz"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
)

func ExtractPackageCharts(manifest, output misc.RootedDir, status *kubopackage.Status) error {
	charts, err := output.Child("charts")
	if err != nil {
		return err
	}
	for module, ref := range status.ChartByModule {
		target, err := charts.Child(module)
		if err != nil {
			return err
		}
		archive := fmt.Sprintf("%s-%s.tgz", ref.Name, ref.Version)
		fmt.Printf("Create %s\n", target.Path())
		if err := tgz.ExtractAllAt(manifest, archive, target); err != nil {
			return err
		}
	}
	return nil
}
