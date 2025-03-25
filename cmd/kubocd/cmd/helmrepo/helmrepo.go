package helmrepo

import (
	"fmt"
	helmclient "github.com/mittwald/go-helm-client"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/repo"
	"kubocd/internal/misc"
	"log/slog"
	"os"
	"path"
	"sigs.k8s.io/yaml"
)

type Operation struct {
	WorkDir      string
	RepoUrl      string
	ChartName    string
	ChartVersion string
}

func DumpHelmRepo(op *Operation) error {

	loc, helmClient, err := SetupHelmRepo(op, "local")
	if err != nil {
		return err
	}
	if op.ChartName == "" {
		// List chart in a repo
		file, err := os.ReadFile(path.Join(loc, ".helmcache", "local-charts.txt"))
		if err != nil {
			return err
		}
		fmt.Printf("\n---------------Chart in repo '%s':\n%s\n", op.RepoUrl, string(file))
	} else if op.ChartVersion == "" {
		// list version for a chart
		indexFileContent, err := os.ReadFile(path.Join(loc, ".helmcache", "local-index.yaml"))
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
		for _, chartVersion := range chartVersions {
			fmt.Printf("%s\n", chartVersion.Version)
		}
	} else {
		// Dump Chart.yaml and content
		chartInfo, archive, err := GetChartArchiveFromHelmRepo("", helmClient, "local", op)
		if err != nil {
			return err
		}
		tarList, err := misc.ListTarGzContents(archive)
		if err != nil {
			return err
		}
		fmt.Printf("\nChart: %s:%s\n\n---------------------- Chart.yaml:\n%s\n\n-------------------- content:\n%s\n", chartInfo.Metadata.Name, chartInfo.Metadata.Version, misc.Any2Yaml(chartInfo.Metadata), tarList)

	}
	return nil

}

// SetupHelmRepo
// WARNING: We can't reuse the same repo name several times in the same execution
// (May be some global stuff inside the "github.com/mittwald/go-helm-client" library)
func SetupHelmRepo(op *Operation, repoName string) (loc string, helmClient helmclient.Client, err error) {
	//misc.WaitUserInput("setupHelmRepo entry")
	// Prepare landing zone
	loc = path.Join(op.WorkDir, "helmRepo")
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
		URL:  op.RepoUrl,
	}
	//fmt.Printf("Loading helm chart repository...\n")
	// Add a chart-repository to the client.
	if err := helmClient.AddOrUpdateChartRepo(chartRepo); err != nil {
		return loc, helmClient, err
	}
	//misc.WaitUserInput("setupHelmRepo exit")
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
