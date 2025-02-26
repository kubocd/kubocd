package cmd

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	helmclient "github.com/mittwald/go-helm-client"
	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/repo"
	"io"
	"kubocd/internal/misc"
	"log/slog"
	"os"
	"path"
	"sigs.k8s.io/yaml"
)

var hrDumpCmd = &cobra.Command{
	Use:   "dump repoUrl [chartName [version]]",
	Short: "Dump helm chart",
	Args:  cobra.RangeArgs(1, 3),
	Run:   hrDumpRun,
}

var dumpHrCmd = &cobra.Command{
	Use:     "helmRepository repoUrl [chartName [version]]",
	Short:   "Dump helm chart",
	Args:    cobra.RangeArgs(1, 3),
	Aliases: []string{"hr", "HelmRepository", "helmrepository", "helmRepo", "HelmRepo", "helmrepo"},
	Run:     hrDumpRun,
}

var hrDumpRun = func(command *cobra.Command, args []string) {
	err := func() error {
		repoUrl := args[0]
		loc, helmClient, err := setupHelmRepo(repoUrl, "local")
		if err != nil {
			return err
		}
		if len(args) == 1 {
			// List chart in a repo
			file, err := os.ReadFile(path.Join(loc, ".helmcache", "local-charts.txt"))
			if err != nil {
				return err
			}
			fmt.Printf("\n---------------Chart in repo '%s':\n%s\n", repoUrl, string(file))
		} else if len(args) == 2 {
			chartName := args[1]
			// list version for a chart
			indexFileContent, err := os.ReadFile(path.Join(loc, ".helmcache", "local-index.yaml"))
			if err != nil {
				return err
			}
			index, err := loadChartIndex(indexFileContent, "index.yaml")
			if err != nil {
				return err
			}
			fmt.Printf("\n---------- Versions for '%s':\n", chartName)
			chartVersions, ok := index.Entries[chartName]
			if !ok {
				return fmt.Errorf("chart %s not found in index", chartName)
			}
			for _, chartVersion := range chartVersions {
				fmt.Printf("%s\n", chartVersion.Version)
			}

		} else if len(args) == 3 {
			// Dump Chart.yaml and content
			chartName := args[1]
			version := args[2]
			chart, archive, err := getChartArchiveFromHelmRepo("", helmClient, "local", chartName, version)
			if err != nil {
				return err
			}
			tarList, err := misc.ListTarGzContents(archive)
			if err != nil {
				return err
			}
			fmt.Printf("\nChart: %s:%s\n\n---------------------- Chart.yaml:\n%s\n\n-------------------- content:\n%s\n", chart.Metadata.Name, chart.Metadata.Version, misc.Map2Yaml(chart.Metadata), tarList)

		}
		return nil
	}()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
		os.Exit(1)
	}
}

// WARNING: We can't reuse the same repo name several times in the same execution
// (May be some global stuff inside the "github.com/mittwald/go-helm-client" library)
func setupHelmRepo(repoUrl string, repoName string) (loc string, helmClient helmclient.Client, err error) {
	//misc.WaitUserInput("setupHelmRepo entry")
	// Prepare landing zone
	loc = path.Join(workDir, "helmRepo")
	if err := misc.SafeEnsureEmpty(loc); err != nil {
		return loc, nil, err
	}
	//misc.WaitUserInput("setupHelmRepo after cleanup")
	opt := &helmclient.Options{
		Namespace:        "default", // Don't care, as we do not interact with a cluster
		RepositoryCache:  path.Join(loc, ".helmcache"),
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
		URL:  repoUrl,
	}
	//fmt.Printf("Loading helm chart repository...\n")
	// Add a chart-repository to the client.
	if err := helmClient.AddOrUpdateChartRepo(chartRepo); err != nil {
		return loc, helmClient, err
	}
	//misc.WaitUserInput("setupHelmRepo exit")
	return loc, helmClient, nil
}

func getChartArchiveFromHelmRepo(printPrefix string, helmClient helmclient.Client, repoName string, chartName string, version string) (chart *chart.Chart, archive string, err error) {
	fmt.Printf("%sFetching chart %s:%s...\n", printPrefix, chartName, version)
	return helmClient.GetChart(fmt.Sprintf("%s/%s", repoName, chartName), &action.ChartPathOptions{Version: version})
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

// Extract the chart name and version from a chart archive
func extractChartInfo(tgzPath string) (chartName string, chartVersion string, err error) {

	// Open the .tgz file
	file, err := os.Open(tgzPath)
	if err != nil {
		return "", "", err
	}
	defer file.Close()

	// Create a gzip reader
	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return "", "", err
	}
	defer gzReader.Close()

	// Create a tar reader
	tarReader := tar.NewReader(gzReader)

	// Iterate through the archive to find the YAML file
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			return "", "", err
		}

		// Check if the file is the target YAML file
		if header.Typeflag == tar.TypeReg {
			fileName := header.Name
			//fmt.Printf("archive file: %s\n", fileName)
			if path.Base(fileName) == "Chart.yaml" {
				// Got it. Read the file content
				yamlChart, err := io.ReadAll(tarReader)
				if err != nil {
					return "", "", err
				}
				// Unmarshal YAML into the Chart.yaml struct
				var chartMeta chart.Metadata
				err = yaml.Unmarshal(yamlChart, &chartMeta)
				if err != nil {
					return "", "", err
				}
				return chartMeta.Name, chartMeta.Version, nil
			}
		}
	}
	return "", "", fmt.Errorf("file Chart.yaml not found in archive")
}
