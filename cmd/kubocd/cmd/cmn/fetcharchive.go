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
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"kubocd/cmd/kubocd/cmd/helmrepo"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/cmd/kubocd/cmd/tgz"
	"kubocd/internal/global"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"helm.sh/helm/v3/pkg/chart"
	"sigs.k8s.io/yaml"
)

type ArchiveInfo struct {
	Name string
	Path string
}

// FetchArchives load all module's archive and
// - return a list of archive (de-duplicated, if two modules use the same chart)
// - return a status with a map of chartInfo by module
func FetchArchives(printPrefix string, pck *kubopackage.Package, assemblyPath string, workDir string, packageFolder string) ([]ArchiveInfo, *kubopackage.Status, error) {
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
				archive, err = getHelmChartArchiveFromGit(printPrefix+"    ", module.Source.Git.Url, module.Source.Git.Branch, module.Source.Git.Tag, module.Source.Git.Path, module.Name, workDir)
				if err != nil {
					return nil, nil, fmt.Errorf("module '%s': could not get helm chart archive: %w", module.Name, err)
				}
			} else if module.Source.Local != nil {
				chartPath := module.Source.Local.Path
				if !path.IsAbs(chartPath) {
					chartPath = path.Join(packageFolder, module.Source.Local.Path)
				}
				archive, err = getHelmCharArchiveFromLocal(printPrefix+"    ", chartPath, module.Name, workDir)
				if err != nil {
					return nil, nil, fmt.Errorf("module '%s': could not get helm chart archive: %w", module.Name, err)
				}
			} else {
				panic("Unrecognized module source")
			}
		} else {
			panic("Unrecognized module type")
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

func getHelmChartArchiveFromGit(printPrefix string, url string, branch string, tag string, chartPath string, moduleName string, workDir string) (string, error) {
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

	// ----------------------------------------------------------- Check we are at the root of an helm chart
	chartFile := path.Join(chartLocation, "Chart.yaml")
	_, err = os.Stat(chartFile)
	if err != nil {
		return "", fmt.Errorf("mising 'Chart.yaml'. Does not look like an helm chart: %w", err)
	}

	// ----------------------------------------------------------- Build chart dependencies
	cmd := exec.Command("helm", "dependency", "build", chartLocation)
	// Run the command and capture output
	_, err = cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to build chart dependencies: %w", err)
	}

	// ----------------------------------------------------------- Build chart archive
	out, err := os.Create(archive)
	if err != nil {
		return "", fmt.Errorf("failed to create archive '%s': %w", archive, err)
	}
	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	chartLocationLen := len(chartLocation)
	err = filepath.WalkDir(chartLocation, func(thePath string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Error("Error while walking git repository on path: %s: %s", thePath, err.Error())
			return nil
		}
		if !d.IsDir() {
			itemFileName := thePath[chartLocationLen:]
			targetFileName := path.Join(moduleName, itemFileName)
			if isChartItem(itemFileName) {
				//fmt.Printf("%s  -> %s (%s)\n", thePath, targetFileName, itemFileName)
				slog.Debug("Store chart item", "item", itemFileName)
				err := tgz.AddToArchive(tw, thePath, targetFileName)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
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
func isChartItem(fileName string) bool {
	if _, ok := validChartItems[fileName]; ok {
		return true
	}
	if strings.HasPrefix(fileName, "/charts") || strings.HasPrefix(fileName, "/crds") || strings.HasPrefix(fileName, "/templates") {
		return true
	}
	return false
}

func getHelmCharArchiveFromLocal(printPrefix string, chartLocation string, moduleName string, workDir string) (string, error) {
	// ----------------------------------------------------------- Build chart archive
	fmt.Printf("%sFetching chart from '%s'\n", printPrefix, chartLocation)

	chartFile := path.Join(chartLocation, "Chart.yaml")
	_, err := os.Stat(chartFile)
	if err != nil {
		return "", fmt.Errorf("mising 'Chart.yaml'. Does not look like an helm chart: %w", err)
	}
	// Copy the chart is a safe place to add dependencies, if any
	loc1 := path.Join(workDir, "local1-workdir")
	err = misc.SafeEnsureEmpty(loc1)
	if err != nil {
		return "", err
	}
	chartLocation2 := path.Join(loc1, "chart")
	err = misc.CopyDir(chartLocation, chartLocation2)
	if err != nil {
		return "", err
	}
	//----------------------
	cmd := exec.Command("helm", "dependency", "build", chartLocation2)

	// Run the command and capture output
	_, err = cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to build chart dependencies: %w", err)
	}

	//----------------------

	chartLocation = chartLocation2
	loc2 := path.Join(workDir, "local2-workdir")
	err = misc.SafeEnsureEmpty(loc2)
	if err != nil {
		return "", err
	}
	archive := path.Join(loc2, fmt.Sprintf("%s.tgz", moduleName))
	out, err := os.Create(archive)
	if err != nil {
		return "", fmt.Errorf("failed to create archive '%s': %w", archive, err)
	}
	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	chartLocation = path.Clean(chartLocation)
	chartLocationLen := len(chartLocation)
	err = filepath.WalkDir(chartLocation, func(thePath string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Error("Error while walking git repository on path: %s: %s", thePath, err.Error())
			return nil
		}
		if !d.IsDir() {
			targetFileName := path.Join(moduleName, thePath[chartLocationLen:])
			//fmt.Printf("%s adding file to archive '%s' -> '%s'\n", printPrefix, thePath, targetFileName)
			err := tgz.AddToArchive(tw, thePath, targetFileName)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return archive, nil
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
