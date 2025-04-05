package cmn

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"helm.sh/helm/v3/pkg/chart"
	"io"
	"io/fs"
	"kubocd/cmd/kubocd/cmd/helmrepo"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/cmd/kubocd/cmd/tgz"
	"kubocd/internal/application"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sigs.k8s.io/yaml"
)

type ArchiveInfo struct {
	Name string
	Path string
}

// FetchArchives load all module's archive and
// - return a list of archive (de-duplicated, if two modules use the same chart)
// - return a status with a map of chartInfo by module
func FetchArchives(printPrefix string, app *application.Application, assemblyPath string, workDir string, applicationFolder string) ([]ArchiveInfo, *application.Status, error) {
	chartSet := make(map[string]bool) // To deduplicate
	archives := make([]ArchiveInfo, 0, len(app.Spec.Modules))
	status := &application.Status{
		ApiVersion:    global.ApplicationApiVersion,
		ChartByModule: make(map[string]application.ChartRef),
	}
	for _, module := range app.Spec.Modules {
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
					chartPath = path.Join(applicationFolder, module.Source.Local.Path)
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
		status.ChartByModule[module.Name] = application.ChartRef{
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

	// ----------------------------------------------------------- Build chart archive
	chartFile := path.Join(chartLocation, "Chart.yaml")
	_, err = os.Stat(chartFile)
	if err != nil {
		return "", fmt.Errorf("mising 'Chart.yaml'. Does not look like an helm chart: %w", err)
	}
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
			targetFileName := path.Join(moduleName, thePath[chartLocationLen:])
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
	cmd := exec.Command("helm", "dependency", "update", chartLocation2)

	// Run the command and capture output
	_, err = cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to update chart dependencies: %w", err)
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
			fmt.Printf("%sadding file to archive '%s' -> '%s'\n", printPrefix, thePath, targetFileName)
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
