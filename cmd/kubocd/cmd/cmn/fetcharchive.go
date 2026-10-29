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
	"io"
	"kubocd/cmd/kubocd/cmd/helmrepo"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/cmd/kubocd/cmd/tgz"
	"kubocd/internal/global"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"sigs.k8s.io/yaml"
)

type ArchiveInfo struct {
	Name string
	Path string
}

// FetchArchives load all module's archive and
// - return a list of archive (de-duplicated, if two modules use the same chart)
// - return a status with a map of chartInfo by module
func FetchArchives(printPrefix string, pck *kubopackage.Package, assemblyPath, workDir, packageFolder string,
	sourceOpts ...SourceOptions) ([]ArchiveInfo, *kubopackage.Status, error) {
	chartSet := make(map[string]bool) // To deduplicate
	archives := make([]ArchiveInfo, 0, len(pck.Modules))
	status := &kubopackage.Status{
		ApiVersion:    global.PackageApiVersion,
		ChartByModule: make(map[string]kubopackage.ChartRef),
	}
	for _, module := range pck.Modules {
		fmt.Printf("%s--- Handling module '%s':\n", printPrefix, module.Name)
		var archive string
		var err error
		if module.Type == global.HelmChartType {
			if module.Source.Oci != nil {
				op := &oci.Operation{
					ImageRepo: module.Source.Oci.Repository,
					ImageTag:  module.Source.Oci.Tag,
					Insecure:  module.Source.Oci.Insecure,
					WorkDir:   workDir,
					Anonymous: false,
				}
				archive, err = oci.GetContentFromOci(printPrefix+"    ", op, global.HelmChartMediaType)
				if err != nil {
					return nil, nil, fmt.Errorf("module '%s': could not get helm chart archive: %w", module.Name, err)
				}
			} else if module.Source.HelmRepository != nil {
				op := &helmrepo.Operation{
					WorkDir:      workDir,
					RepoUrl:      module.Source.HelmRepository.Url,
					ChartName:    module.Source.HelmRepository.Chart,
					ChartVersion: module.Source.HelmRepository.Version,
				}
				_, helmClient, err := helmrepo.SetupHelmRepo(op, module.Name)
				if err != nil {
					return nil, nil, fmt.Errorf("module '%s': error on helmRepository settings: %w", module.Name, err)
				}
				_, archive, err = helmrepo.GetChartArchiveFromHelmRepo(printPrefix+"    ", helmClient, module.Name, op)
				if err != nil {
					return nil, nil, fmt.Errorf("module '%s': could not get helm chart archive: %w", module.Name, err)
				}
			} else if module.Source.Git != nil {
				archive, err = getHelmChartArchiveFromGit(printPrefix+"    ", module.Source.Git.Url, module.Source.Git.Branch,
					module.Source.Git.Tag, module.Source.Git.Path, module.Name, workDir,
					module.Source.Git.ExtraFilePrefixes, sourceOpts...)
				if err != nil {
					return nil, nil, fmt.Errorf("module '%s': could not get helm chart archive: %w", module.Name, err)
				}
			} else if module.Source.Local != nil {
				chartPath := module.Source.Local.Path
				if !path.IsAbs(chartPath) {
					chartPath = path.Join(packageFolder, module.Source.Local.Path)
				}
				archive, err = getHelmCharArchiveFromLocal(printPrefix+"    ", chartPath, module.Name, workDir, sourceOpts...)
				if err != nil {
					return nil, nil, fmt.Errorf("module '%s': could not get helm chart archive: %w", module.Name, err)
				}
			} else {
				return nil, nil, fmt.Errorf("module '%s': no source is set for its chart", module.Name)
			}
		} else {
			return nil, nil, fmt.Errorf("module '%s': the module type '%s' is not supported", module.Name, module.Type)
		}
		chartName, chartVersion, err := extractChartInfo(archive)
		if err != nil {
			return nil, nil, err
		}
		targetArchiveName := fmt.Sprintf("%s-%s.tgz", chartName, chartVersion)
		targetArchivePath := path.Join(assemblyPath, targetArchiveName)
		_, ok := chartSet[targetArchiveName]
		if !ok {
			chartSet[targetArchiveName] = true
			archives = append(archives, ArchiveInfo{
				Name: targetArchiveName,
				Path: targetArchivePath,
			})
			err = misc.CopyFile(archive, targetArchivePath)
			if err != nil {
				return nil, nil, fmt.Errorf("cannot copy %s to %s: %w", archive, targetArchivePath, err)
			}
		}
		status.ChartByModule[module.Name] = kubopackage.ChartRef{
			Name:    chartName,
			Version: chartVersion,
		}
		fmt.Printf("%s    Chart: %s:%s\n", printPrefix, chartName, chartVersion)
	}
	return archives, status, nil
}

func getHelmChartArchiveFromGit(printPrefix, url, branch, tag, chartPath, moduleName, workDir string,
	extraFilePrefixes []string, sourceOpts ...SourceOptions) (string, error) {
	opts := sourceOptions(sourceOpts)
	if chartPath == "" {
		chartPath = "."
	}
	if !opts.AllowExternalLinks && !filepath.IsLocal(chartPath) {
		return "", fmt.Errorf("git chart path %q is not inside the checkout", chartPath)
	}
	// Prepare target archive folder
	loc := path.Join(workDir, "git-workdir")
	err := misc.SafeEnsureEmpty(loc)
	if err != nil {
		return "", err
	}
	repoLocation := path.Join(loc, "repo")
	chartLocation := path.Join(repoLocation, chartPath)
	archive := path.Join(loc, fmt.Sprintf("%s.tgz", moduleName))

	fmt.Printf("%sCloning git repository '%s'\n", printPrefix, url)
	options := &git.CloneOptions{
		//Auth:          auth,		// See KAD git services for auth handling
		URL:           url,
		Progress:      io.Discard,
		ReferenceName: misc.Ternary(tag == "", plumbing.NewBranchReferenceName(branch), plumbing.NewTagReferenceName(tag)),
	}
	gitToken := os.Getenv("GITHUB_TOKEN")
	if gitToken != "" {
		options.Auth = &http.BasicAuth{
			Username: "git",
			Password: gitToken,
		}
	}
	_, err = git.PlainClone(repoLocation, false, options)
	if err != nil {
		return "", fmt.Errorf("failed to clone repo: %w", err)
	}

	if opts.AllowExternalLinks {
		if err := checkLegacyChart(chartLocation, loc, map[string]bool{}); err != nil {
			return "", err
		}
	} else {
		chartLocation, err = stageChart(repoLocation, filepath.Clean(chartPath), filepath.Join(loc, "stage"), true)
		if err != nil {
			return "", err
		}
	}
	if err := requireChartMetadata(chartLocation, opts.AllowExternalLinks); err != nil {
		return "", err
	}
	if err := updateChartDependencies(chartLocation); err != nil {
		return "", err
	}
	if err := archiveChart(chartLocation, archive, moduleName, extraFilePrefixes,
		true, opts.AllowExternalLinks); err != nil {
		return "", err
	}
	return archive, nil
}

var validChartItems = map[string]bool{
	"/Chart.yaml":         true,
	"/Chart.json":         true,
	"/Chart.yml":          true,
	"/LICENSE":            true,
	"/README.md":          true,
	"/values.yaml":        true,
	"/values.schema.json": true,
	"/Chart.lock":         true,
}

// Test if the file is an effective chart item (cf https://helm.sh/docs/v3/topics/charts)
func isChartItem(fileName string, extraFilePrefixes []string) bool {
	if _, ok := validChartItems[fileName]; ok {
		return true
	}
	if strings.HasPrefix(fileName, "/charts") || strings.HasPrefix(fileName, "/crds") || strings.HasPrefix(fileName, "/templates") {
		return true
	}
	for _, prefix := range extraFilePrefixes {
		if strings.HasPrefix(fileName, prefix) {
			return true
		}
	}
	return false
}

func getHelmCharArchiveFromLocal(printPrefix, chartLocation, moduleName, workDir string,
	sourceOpts ...SourceOptions) (string, error) {
	opts := sourceOptions(sourceOpts)
	fmt.Printf("%sFetching chart from '%s'\n", printPrefix, chartLocation)
	loc1 := filepath.Join(workDir, "local1-workdir")
	if err := misc.SafeEnsureEmpty(loc1); err != nil {
		return "", err
	}
	chartCopy := filepath.Join(loc1, "chart")
	if opts.AllowExternalLinks {
		source, err := misc.OpenChartSource(chartLocation, true)
		if err != nil {
			return "", err
		}
		defer func() { _ = source.Close() }()
		root, err := os.OpenRoot(loc1)
		if err != nil {
			return "", err
		}
		defer func() { _ = root.Close() }()
		if err := source.CopyTree(".", root, "chart", true); err != nil {
			return "", err
		}
		if err := checkLegacyChart(chartCopy, loc1, map[string]bool{}); err != nil {
			return "", err
		}
	} else {
		if _, err := stageChart(chartLocation, ".", chartCopy, false); err != nil {
			return "", err
		}
	}
	if err := requireChartMetadata(chartCopy, opts.AllowExternalLinks); err != nil {
		return "", err
	}
	if err := updateChartDependencies(chartCopy); err != nil {
		return "", err
	}
	loc2 := filepath.Join(workDir, "local2-workdir")
	if err := misc.SafeEnsureEmpty(loc2); err != nil {
		return "", err
	}
	archive := filepath.Join(loc2, fmt.Sprintf("%s.tgz", moduleName))
	if err := archiveChart(chartCopy, archive, moduleName, nil, false, opts.AllowExternalLinks); err != nil {
		return "", err
	}
	return archive, nil
}

func requireChartMetadata(dir string, allowExternal bool) error {
	source, err := misc.OpenChartSource(dir, allowExternal)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	f, err := source.OpenRegular("Chart.yaml")
	if err != nil {
		return fmt.Errorf("missing 'Chart.yaml'. Does not look like a Helm chart: %w", err)
	}
	return f.Close()
}

// Extract the chart name and version from a chart archive
func extractChartInfo(tgzPath string) (chartName string, chartVersion string, err error) {
	ba, err := tgz.ExtractDataFromTgz(tgzPath, "Chart.yaml")
	if err != nil {
		return "", "", err
	}
	var chartMeta chart.Metadata
	// Unmarshal YAML into the recipient
	err = yaml.Unmarshal(ba, &chartMeta)
	if err != nil {
		return "", "", err
	}
	return chartMeta.Name, chartMeta.Version, nil
}
