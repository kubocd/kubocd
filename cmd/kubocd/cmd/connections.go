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
	"context"
	"encoding/json"
	"fmt"
	"kubocd/api/v1alpha1"
	"kubocd/internal/k8sapi"
	"kubocd/internal/misc"
	"os"
	"slices"
	"sort"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

/*
This command displays the relationship between Releases, through their Connection/ClusterConnection.

Such relationships are built as a list of Link, by digging inside the Release and the
Connection/ClusterConnection of the scope. The scope is a set of namespaces (--namespace, which may
be repeated) or the whole cluster (--all-namespaces). As they are cluster scoped, ClusterConnection
are part of all the scopes.

To find the relationships, the following apply:
- For a Connection, the parent (source) release is in ownerReferences
- For a ClusterConnection, the parent (source) release is found in spec.parentRelease
- For a Release, the input (target) connections are found in status.effectiveInputConnections

Connection/ClusterConnection can also exist 'unmanaged', without any parent. And a connection may be
used by no release (Such one is only displayed on request, with --unused). In both cases, the
corresponding side of the Link is empty.
*/

/*
- Implement cmd.kubocd.cmd.connections.go following instructions in comments
- For connection with no consumer, do list list them by default, but add a flag to list them
- Add namespace on release in all cases (even if scope is namespaced)
- Add an option --interface (-i) which will filter on interface
- On table view only, condition the CONNECTION column displaying to a flag
- Refactor to allow multiple namespaces option
*/

var connectionsParams struct {
	namespaces     []string
	allNamespaces  bool
	unused         bool
	itf            string
	showConnection bool
	output         string
}

func init() {
	connectionsCmd.PersistentFlags().StringArrayVarP(&connectionsParams.namespaces, "namespace", "n", []string{"default"}, "The namespace to look inside. May be repeated.")
	connectionsCmd.PersistentFlags().BoolVarP(&connectionsParams.allNamespaces, "all-namespaces", "A", false, "Lookup in all namespaces")
	connectionsCmd.PersistentFlags().BoolVarP(&connectionsParams.unused, "unused", "u", false, "Also display the connections not used by any release")
	connectionsCmd.PersistentFlags().StringVarP(&connectionsParams.itf, "interface", "i", "", "Only display the connections of this interface")
	connectionsCmd.PersistentFlags().BoolVarP(&connectionsParams.showConnection, "connection", "c", false, "Display the connection column (table output only)")
	connectionsCmd.PersistentFlags().StringVarP(&connectionsParams.output, "output", "o", "table", "Output format. One of: table|json|yaml")
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
	Use:     "connections [release name]",
	Short:   "Display connections for a release or globally",
	Args:    cobra.RangeArgs(0, 1),
	Aliases: []string{"connection", "cnx"},
	Example: `	Display all the links in the default namespace:
	$ kubocd connections

	Display all the links in the whole cluster:
	$ kubocd connections --all-namespaces

	Display the links related to the 'podinfo' release of the 'project01' namespace:
	$ kubocd connections podinfo --namespace project01

	Display all the links of two namespaces:
	$ kubocd connections --namespace project01 --namespace project02

	Also display the connections which are used by no release:
	$ kubocd connections --all-namespaces --unused

	Display only the links of the 'ingress' interface:
	$ kubocd connections --all-namespaces --interface ingress

	Display all the links as yaml:
	$ kubocd connections --all-namespaces --output yaml`,

	Run: func(cmd *cobra.Command, args []string) {
		err := func() error {
			releaseName := ""
			if len(args) > 0 {
				releaseName = args[0]
			}
			k8sClient, err := k8sapi.GetKubeClient(scheme)
			if err != nil {
				return fmt.Errorf("error getting kubernetes client: %w", err)
			}
			links, err := buildLinks(context.Background(), k8sClient, releaseName)
			if err != nil {
				return err
			}
			return displayLinks(links)
		}()
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			os.Exit(1)
		}
	},
}

// buildLinks lookup all the Connection/ClusterConnection of the scope and, for each of them, bind
// the release which owns it (the source) to the releases using it as input (the targets).
func buildLinks(ctx context.Context, k8sClient client.Client, releaseName string) ([]Link, error) {
	releases := make([]v1alpha1.Release, 0)
	connections := make([]v1alpha1.Connection, 0)
	for _, listOptions := range scopeListOptions() {
		releaseList := &v1alpha1.ReleaseList{}
		if err := k8sClient.List(ctx, releaseList, listOptions...); err != nil {
			return nil, fmt.Errorf("unable to list releases: %w", err)
		}
		releases = append(releases, releaseList.Items...)
		connectionList := &v1alpha1.ConnectionList{}
		if err := k8sClient.List(ctx, connectionList, listOptions...); err != nil {
			return nil, fmt.Errorf("unable to list connections: %w", err)
		}
		connections = append(connections, connectionList.Items...)
	}
	// ClusterConnection are cluster scoped, so they belong to all scopes
	clusterConnectionList := &v1alpha1.ClusterConnectionList{}
	if err := k8sClient.List(ctx, clusterConnectionList); err != nil {
		return nil, fmt.Errorf("unable to list clusterConnections: %w", err)
	}
	// ------------------------------------------ Index the releases using a connection as input, by connection
	targetsByConnection := make(map[string][]v1alpha1.NamespacedName)
	for _, release := range releases {
		for _, input := range release.Status.EffectiveInputConnections {
			if input.Name == "" {
				continue // An input without any elected connection
			}
			key := connectionKey(input.Kind, input.Namespace, input.Name)
			targetsByConnection[key] = append(targetsByConnection[key], v1alpha1.NamespacedName{
				Namespace: release.Namespace,
				Name:      release.Name,
			})
		}
	}
	// ------------------------------------------ Build a link per connection/target pair
	links := make([]Link, 0)
	for _, connection := range connections {
		// The parent release of a Connection is its controller, in the same namespace
		source := v1alpha1.NamespacedName{}
		if owner := metav1.GetControllerOf(&connection); owner != nil {
			source.Namespace = connection.Namespace
			source.Name = owner.Name
		}
		links = append(links, linksForConnection(v1alpha1.KindConnection, connection.Namespace, connection.Name,
			connection.Spec.Interface, source, targetsByConnection)...)
	}
	for _, clusterConnection := range clusterConnectionList.Items {
		// The parent release of a ClusterConnection is explicitly referenced in its spec
		source := v1alpha1.NamespacedName{}
		if clusterConnection.Spec.ParentRelease != nil {
			source.Namespace = clusterConnection.Spec.ParentRelease.Namespace
			source.Name = clusterConnection.Spec.ParentRelease.Name
		}
		links = append(links, linksForConnection(v1alpha1.KindClusterConnection, "", clusterConnection.Name,
			clusterConnection.Spec.Interface, source, targetsByConnection)...)
	}
	// ------------------------------------------ Filter on the release and the interface, if provided
	filtered := make([]Link, 0, len(links))
	for _, link := range links {
		if releaseName != "" && !isRelease(link.SourceRelease.Name, link.SourceRelease.Namespace, releaseName) &&
			!isRelease(link.TargetRelease.Name, link.TargetRelease.Namespace, releaseName) {
			continue
		}
		if connectionsParams.itf != "" && link.Interface != connectionsParams.itf {
			continue
		}
		filtered = append(filtered, link)
	}
	links = filtered
	sortLinks(links)
	return links, nil
}

// linksForConnection build one link per release using this connection. A connection used by nobody
// provides a single link with an empty target, and only if such connections are requested.
func linksForConnection(kind v1alpha1.Kind, namespace, name, itf string, source v1alpha1.NamespacedName, targetsByConnection map[string][]v1alpha1.NamespacedName) []Link {
	targets := targetsByConnection[connectionKey(kind, namespace, name)]
	if len(targets) == 0 {
		if !connectionsParams.unused {
			return nil
		}
		targets = []v1alpha1.NamespacedName{{}}
	}
	links := make([]Link, len(targets))
	for idx, target := range targets {
		links[idx].SourceRelease.Namespace = source.Namespace
		links[idx].SourceRelease.Name = source.Name // "" if the connection is unmanaged
		links[idx].Interface = itf
		links[idx].Connection.Kind = kind
		links[idx].Connection.Namespace = namespace // "" if ClusterConnection
		links[idx].Connection.Name = name
		links[idx].TargetRelease.Namespace = target.Namespace
		links[idx].TargetRelease.Name = target.Name // "" if the connection is used by nobody
	}
	return links
}

// scopeListOptions provide the list options to fetch all the namespaced resources of the scope.
// One entry per namespace, or a single one without any namespace restriction for the whole cluster.
func scopeListOptions() [][]client.ListOption {
	if connectionsParams.allNamespaces {
		return [][]client.ListOption{nil}
	}
	options := make([][]client.ListOption, 0, len(connectionsParams.namespaces))
	for _, namespace := range scopeNamespaces() {
		options = append(options, []client.ListOption{client.InNamespace(namespace)})
	}
	return options
}

// scopeNamespaces provide the namespaces of the scope, without duplicate. (Meaningless if --all-namespaces)
func scopeNamespaces() []string {
	namespaces := make([]string, 0, len(connectionsParams.namespaces))
	for _, namespace := range connectionsParams.namespaces {
		if !slices.Contains(namespaces, namespace) {
			namespaces = append(namespaces, namespace)
		}
	}
	return namespaces
}

// connectionKey identify a connection, as referenced by Release.status.effectiveInputConnections.
// (namespace is empty for a ClusterConnection)
func connectionKey(kind v1alpha1.Kind, namespace, name string) string {
	return fmt.Sprintf("%s/%s/%s", kind, namespace, name)
}

// isRelease check a release reference against the release name provided on the command line.
// (Only a release of the scope may be designated by this name)
func isRelease(name, namespace, releaseName string) bool {
	if name != releaseName {
		return false
	}
	return connectionsParams.allNamespaces || slices.Contains(connectionsParams.namespaces, namespace)
}

func sortLinks(links []Link) {
	sort.Slice(links, func(i, j int) bool {
		a, b := links[i], links[j]
		if a.SourceRelease.Namespace != b.SourceRelease.Namespace {
			return a.SourceRelease.Namespace < b.SourceRelease.Namespace
		}
		if a.SourceRelease.Name != b.SourceRelease.Name {
			return a.SourceRelease.Name < b.SourceRelease.Name
		}
		if a.Connection.Namespace != b.Connection.Namespace {
			return a.Connection.Namespace < b.Connection.Namespace
		}
		if a.Connection.Name != b.Connection.Name {
			return a.Connection.Name < b.Connection.Name
		}
		if a.TargetRelease.Namespace != b.TargetRelease.Namespace {
			return a.TargetRelease.Namespace < b.TargetRelease.Namespace
		}
		return a.TargetRelease.Name < b.TargetRelease.Name
	})
}

func displayLinks(links []Link) error {
	switch connectionsParams.output {
	case "table":
		displayLinksAsTable(links)
	case "json":
		out, err := json.MarshalIndent(links, "", "  ")
		if err != nil {
			return fmt.Errorf("unable to encode links as json: %w", err)
		}
		fmt.Println(string(out))
	case "yaml":
		fmt.Print(string(misc.Any2Yaml(links)))
	default:
		return fmt.Errorf("invalid output format: '%s'. Must be one of table|json|yaml", connectionsParams.output)
	}
	return nil
}

func displayLinksAsTable(links []Link) {
	table := tablewriter.NewWriter(os.Stdout)
	header := []string{"Source release", "Interface"}
	if connectionsParams.showConnection {
		header = append(header, "Connection")
	}
	header = append(header, "Target release")
	table.SetHeader(header)
	table.SetAutoWrapText(false)
	for _, link := range links {
		row := []string{
			displayNamespacedName(link.SourceRelease.Namespace, link.SourceRelease.Name),
			link.Interface,
		}
		if connectionsParams.showConnection {
			row = append(row, displayConnection(link.Connection.Kind, link.Connection.Namespace, link.Connection.Name))
		}
		row = append(row, displayNamespacedName(link.TargetRelease.Namespace, link.TargetRelease.Name))
		table.Append(row)
	}
	table.Render()
}

// displayNamespacedName render a release reference. The namespace is always displayed, as a release
// may be outside of the scope. (The source release of a ClusterConnection, typically)
func displayNamespacedName(namespace, name string) string {
	if name == "" {
		return "-"
	}
	return fmt.Sprintf("%s:%s", namespace, name)
}

// displayConnection render a connection reference. A ClusterConnection is displayed inside brackets.
func displayConnection(kind v1alpha1.Kind, namespace, name string) string {
	if kind == v1alpha1.KindClusterConnection {
		return fmt.Sprintf("[%s]", name)
	}
	return displayNamespacedName(namespace, name)
}
