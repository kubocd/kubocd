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

package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	"kubocd/internal/global"
)

var versionParams struct {
	extended bool
}

func init() {
	versionCmd.PersistentFlags().BoolVarP(&versionParams.extended, "extended", "e", false, "Add build number")
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "display kubocd CLI version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if versionParams.extended {
			fmt.Printf("%s.%s\n", global.Version, global.BuildTs)
		} else {
			fmt.Printf("%s\n", global.Version)
		}
	},
}
