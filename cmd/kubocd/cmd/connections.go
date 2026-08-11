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
	"kubocd/api/v1alpha1"

	"github.com/spf13/cobra"
)

/* Instructions for Claude:

This command if for displaying relationship between Release using Connections/ClusterConnection

It will build an array of Link objects by digging inside Release and Connections/ClusterConnection in provided scope.
scope is the specified namespace or --all-namespaces

Cluster scoped ClusterConnection are included in all scope

To find relationship, the following apply:
- For a Connection, the parent release is in ownerReferences
- For a ClusterConnection, the parent release is found in spec.parentRelease
- For a Release, the inputs connections are found in status.effectiveInputConnections

Connection/ClusterConnection can also exists 'unmanaged', without any parent.

If a release name is specified as first parameter, only links related to this release are displayed

By default, Links will be displayed in tabular format, using tablewriter library.

Optionally, Link will be displayed in JSON or YAML format (Corresponding CLI options to be added).

connectionsCmd.Run to be implemented. Keep code simple and clear.

*/

var connectionsParams struct {
	namespace     string
	allNamespaces bool
}

func init() {
	connectionsCmd.PersistentFlags().StringVarP(&connectionsParams.namespace, "namespace", "n", "default", "The namespace to look inside")
	connectionsCmd.PersistentFlags().BoolVarP(&connectionsParams.allNamespaces, "all-namespaces", "A", false, "Lookup in all namespaces")
}

type Link struct {
	SourceRelease struct {
		Name      string `json:"name"` // "" if connection is unmanaged
		Namespace string `json:"namespace"`
	} `json:"sourceRelease"`
	Interface  string `json:"interface"`
	Connection struct {
		Kind      v1alpha1.Kind `json:"kind"`
		Name      string        `json:"name"`
		Namespace string        `json:"namespace,omitempty"` // Empty if ClusterConnection
	} `json:"connection"`
	TargetRelease struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"targetRelease"`
}

var connectionsCmd = &cobra.Command{
	Use:   "connections [release name]",
	Short: "Display connections for a release or globally",
	Args:  cobra.RangeArgs(0, 1),
	Run: func(cmd *cobra.Command, args []string) {

	},
}
