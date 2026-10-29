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

package helmrepo

import (
	"bytes"
	"fmt"
	"io"
	"kubocd/cmd/kubocd/cmd/tgz"
	"kubocd/internal/archiveio"
	"kubocd/internal/misc"
	"log/slog"
	"path"
	"sort"
	"strings"

	helmclient "github.com/mittwald/go-helm-client"
	"helm.sh/helm/v4/pkg/action"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/repo/v1"
	"sigs.k8s.io/yaml"
)

type Operation struct {
	ArchiveLimits *archiveio.Limits
	WorkDir       string
	RepoUrl       string
	ChartName     string
	ChartVersion  string
	Output        string
	Chart         bool
}

func DumpHelmRepo(op *Operation) error {

	_, helmClient, err := SetupHelmRepo(op, "local")
	if err != nil {
		return err
	}
	work, err := misc.OpenRootedDir(op.WorkDir)
	if err != nil {
		return err
	}
	defer func() { _ = work.Root.Close() }()
	if op.ChartName == "" {
		// List chart in a repo
		file, err := work.ReadFile("helmRepo/.helmcache/local-charts.txt")
		if err != nil {
			return err
		}
		fmt.Printf("\n---------------Chart in repo '%s':\n%s\n", op.RepoUrl, string(file))
	} else if op.ChartVersion == "" {
		// list version for a chart
		indexFileContent, err := work.ReadFile("helmRepo/.helmcache/local-index.yaml")
		if err != nil {
			return err
		}
		index, err := loadChartIndex(indexFileContent, "index.yaml")
		if err != nil {
			return err
		}
		fmt.Printf("\n---------- Versions for '%s':\n", op.ChartName)
		chartVersions, ok := index.Entries[op.ChartName]
		if !ok {
			return fmt.Errorf("chart %s not found in index", op.ChartName)
		}
		versions := make([]string, len(chartVersions))
		for idx, v := range chartVersions {
			versions[idx] = v.Version
		}
		sort.Strings(versions)

		for _, v := range versions {
			fmt.Printf("%s\n", v)
		}
	} else {
		// Dump Chart.yaml and content
		chartInfo, archive, err := GetChartArchiveFromHelmRepo("", helmClient, "local", op)
		if err != nil {
			return err
		}
		name, err := work.Relative(archive)
		if err != nil {
			return err
		}
		return dumpFetchedChartAt(work, name, chartInfo, op)
	}
	return nil

}

// SetupHelmRepo
// WARNING: We can't reuse the same repo name several times in the same execution
// (Maybe some global stuff inside the "github.com/mittwald/go-helm-client" library)
func SetupHelmRepo(op *Operation, repoName string) (loc string, helmClient helmclient.Client, err error) {
	// Prepare landing zone
	work, err := misc.OpenRootedDir(op.WorkDir)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = work.Root.Close() }()
	target, err := misc.PrepareDir(work, "helmRepo")
	if err != nil {
		return "", nil, err
	}
	cache, err := target.Child(".helmcache")
	if err != nil {
		return "", nil, err
	}
	for _, suffix := range []string{"-index.yaml", "-charts.txt"} {
		if _, err := cache.Child(repoName + suffix); err != nil {
			return "", nil, err
		}
	}
	loc = target.Path()
	opt := &helmclient.Options{
		Namespace:        "default", // Don't care, as we do not interact with a cluster
		RepositoryCache:  path.Join(loc, ".helmcache"),
		ContentCache:     path.Join(loc, ".helmcontent"),
		RepositoryConfig: path.Join(loc, ".helmrepo"),
		Debug:            true,
		Linting:          true,
		DebugLog:         func(format string, v ...interface{}) {},
		//RegistryConfig:   path.Join(loc, ".registry-config"),
		//Output:           &outputBuffer, // Not mandatory, leave open for default os.Stdout
	}

	//fmt.Printf("Creating helm client...\n")
	helmClient, err = helmclient.New(opt)
	if err != nil {
		return loc, nil, err
	}

	chartRepo := repo.Entry{
		Name: repoName,
		URL:  op.RepoUrl,
	}
	//fmt.Printf("Loading helm chart repository...\n")
	// Add a chart-repository to the client.
	if err := helmClient.AddOrUpdateChartRepo(chartRepo); err != nil {
		return loc, helmClient, err
	}
	return loc, helmClient, nil
}

// from helm/pkg/repo/index.go
// loadIndex loads an index file and does minimal validity checking.
//
// The source parameter is only used for logging.
// This will fail if API Version is not set (ErrNoAPIVersion) or if the unmarshal fails.
func loadChartIndex(data []byte, source string) (*repo.IndexFile, error) {
	i := &repo.IndexFile{}

	if len(data) == 0 {
		return i, repo.ErrEmptyIndexYaml
	}

	if err := yaml.UnmarshalStrict(data, i); err != nil {
		return i, err
	}

	for name, cvs := range i.Entries {
		for idx := len(cvs) - 1; idx >= 0; idx-- {
			if cvs[idx].APIVersion == "" {
				cvs[idx].APIVersion = chart.APIVersionV1
			}
			if err := cvs[idx].Validate(); err != nil {
				slog.Info(fmt.Sprintf("skipping loading invalid entry for chart %q %q from %s: %s", name, cvs[idx].Version, source, err))
				cvs = append(cvs[:idx], cvs[idx+1:]...)
			}
		}
	}
	i.SortEntries()
	if i.APIVersion == "" {
		return i, repo.ErrNoAPIVersion
	}
	return i, nil
}

func GetChartArchiveFromHelmRepo(printPrefix string, helmClient helmclient.Client, repoName string, op *Operation) (chart *chart.Chart, archive string, err error) {
	fmt.Printf("%sFetching chart %s:%s...\n", printPrefix, op.ChartName, op.ChartVersion)
	return helmClient.GetChart(fmt.Sprintf("%s/%s", repoName, op.ChartName), &action.ChartPathOptions{Version: op.ChartVersion})
}

func dumpChartAt(source misc.RootedDir, name string, op *Operation) error {
	target, _, err := misc.PrepareOutput(op.Output, fmt.Sprintf("%s-%s", op.ChartName, op.ChartVersion))
	if err != nil {
		return err
	}
	defer func() { _ = target.Root.Close() }()
	fmt.Printf("---------------------- Extract chart %s (%s) to ./%s\n\n", op.ChartName, op.ChartVersion, target.Path())
	return tgz.ExtractAllAt(source, name, target, op.ArchiveLimits)
}

func dumpFetchedChartAt(work misc.RootedDir, name string, _ *chart.Chart, op *Operation) error {
	tarList, err := misc.ListTarGzContentsAt(work, name, op.ArchiveLimits)
	if err != nil {
		return err
	}
	metadata, err := readDumpChartMetadata(work, name, op.ArchiveLimits)
	if err != nil {
		return err
	}
	fmt.Printf("\nChart: %s:%s\n\n---------------------- Chart.yaml:\n%s\n\n-------------------- content:\n%s\n",
		metadata.Name, metadata.Version, misc.Any2Yaml(metadata), tarList)
	if op.Chart && op.Output != "" {
		if err := dumpChartAt(work, name, op); err != nil {
			return err
		}
	}
	return nil
}

func readDumpChartMetadata(work misc.RootedDir, name string, limits *archiveio.Limits) (*chart.Metadata, error) {
	file, err := work.OpenRegular(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	reader, err := archiveio.New(file, limits)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	var metadata *chart.Metadata
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		// Helm chooses backslashes before removing the first component of a mixed path.
		separator := "/"
		if strings.ContainsRune(header.Name, '\\') {
			separator = "\\"
		}
		_, relative, ok := strings.Cut(header.Name, separator)
		relative = strings.ReplaceAll(relative, separator, "/")
		if !ok || path.Clean(relative) != "Chart.yaml" {
			continue
		}
		var data bytes.Buffer
		if err := reader.CopyMetadata(&data); err != nil {
			return nil, err
		}
		if metadata == nil {
			metadata = &chart.Metadata{}
		}
		if err := yaml.Unmarshal(bytes.TrimPrefix(data.Bytes(), []byte{0xef, 0xbb, 0xbf}), metadata); err != nil {
			return nil, err
		}
	}
	if metadata == nil {
		return nil, fmt.Errorf("file 'Chart.yaml' not found in archive")
	}
	return metadata, nil
}
