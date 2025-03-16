package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/repo"
	"io"
	"io/fs"
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/cmd/kubocd/cmd/helmrepo"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/internal/application"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"log/slog"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
	"os"
	"path"
	"path/filepath"
	"sigs.k8s.io/yaml"
	"strings"
)

/*
	Folder layout in workDir

	fs/assembly.tgz
	assembly/manifest.yaml
	assembly/index.yaml
	assembly/module01.tgz
	assembly/module0X.tgz
	....

*/

type archiveInfo struct {
	name string
	path string
}

var packageParams struct {
	ociRepoPrefix string
	plainHTTP     bool
	workDir       string
}

func init() {
	packageCmd.PersistentFlags().StringVarP(&packageParams.ociRepoPrefix, "ociRepoPrefix", "r", "", "OCI repository prefix (i.e 'quay.io/your-organization/applications'). Can also be specified with OCI_REPO_PREFIX environment variable")
	packageCmd.PersistentFlags().BoolVarP(&packageParams.plainHTTP, "plainHTTP", "p", false, "Use plain HTTP instead of HTTPS")
	packageCmd.PersistentFlags().StringVarP(&packageParams.workDir, "workDir", "w", "", "working directory. Default to $HOME/.kubocd")

}

var packageCmd = &cobra.Command{
	Use:     "package <Application manifest>",
	Short:   "Assemble a KuboCd Application from a manifest to an OCI image",
	Args:    cobra.ExactArgs(1),
	Aliases: []string{"pack", "build"},
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// ------------------------------------------- Setup working folder
		if packageParams.workDir == "" {
			dir, err := os.UserHomeDir()
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "Unable to determine home directory: %v\n", err)
				os.Exit(1)
			}
			packageParams.workDir = fmt.Sprintf("%s/.kubocd", dir)
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		err := func() error {
			appOriginal := &application.Application{}
			appGroomed := &application.Application{}
			err := misc.LoadYaml(args[0], appOriginal, appGroomed)
			if err != nil {
				return err
			}
			err = appGroomed.Groom()
			if err != nil {
				return err
			}

			// --------------------- Handle entry parameters
			repository := packageParams.ociRepoPrefix
			if repository == "" {
				repository = os.Getenv("OCI_REPO_PREFIX")
				if repository == "" {
					return fmt.Errorf("an OCI repository prefix must be definded. Use OCI_REPO_PREFIX environment variable or --ociRepoPrefix option")
				}
			}
			repository = path.Join(repository, appGroomed.Metadata.Name)

			tag := appGroomed.Metadata.Version

			// ---------- Prepare the target layout
			fsPath := path.Join(packageParams.workDir, "fs")
			err = misc.SafeEnsureEmpty(fsPath)
			if err != nil {
				return err
			}
			assemblyPath := path.Join(packageParams.workDir, "assembly")
			err = misc.SafeEnsureEmpty(assemblyPath)
			if err != nil {
				return err
			}
			// -------------------------- Collect all archives, store them in assembly, and reference them in a []moduleInfo
			chartSet, status, err := fetchArchives("", appGroomed, assemblyPath, packageParams.workDir)
			if err != nil {
				return err
			}

			fmt.Printf("--- Packaging\n")

			// ------------------------------------- Build index.yaml file to be a helm repository
			fmt.Printf("    Generating index file\n")
			index, err := repo.IndexDirectory(assemblyPath, "")
			if err != nil {
				return err
			}

			// ------------------------------------- Produce meta files
			//fmt.Printf("index.yaml:\n%s\n", misc.Map2YamlStr(index))
			// Generate the index file to be included in archive
			err = os.WriteFile(path.Join(assemblyPath, "index.yaml"), misc.Map2Yaml(index), os.ModePerm)
			if err != nil {
				return err
			}
			// Generate the original.yaml file to be included in archive
			err = os.WriteFile(path.Join(assemblyPath, "original.yaml"), misc.Map2Yaml(appOriginal), os.ModePerm)
			if err != nil {
				return err
			}
			// Generate the groomed.yaml file to be included in archive
			err = os.WriteFile(path.Join(assemblyPath, "groomed.yaml"), misc.Map2Yaml(appGroomed), os.ModePerm)
			if err != nil {
				return err
			}
			// Generate the groomed.yaml file to be included in archive
			err = os.WriteFile(path.Join(assemblyPath, "status.yaml"), misc.Map2Yaml(status), os.ModePerm)
			if err != nil {
				return err
			}
			// Generate the manifest.json file to be set as config in the image
			err = os.WriteFile(path.Join(assemblyPath, "manifest.json"), misc.Map2Json(appOriginal), os.ModePerm)
			if err != nil {
				return err
			}

			// ------------------------------------- Package
			fmt.Printf("    Wrap all in assembly.tgz\n")
			err = buildAssembly(assemblyPath, chartSet)
			if err != nil {
				return err
			}

			// Build and push image
			err = pushImage(assemblyPath, repository, tag, packageParams.plainHTTP)
			if err != nil {
				return err
			}
			return nil
		}()
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			os.Exit(1)
		}
	},
}

// lookupArchive load all module's archive and
// - return a list of archive (de-duplicated, if two modules use the same chart)
// - return a status with a map of chartInfo by module
func fetchArchives(printPrefix string, app *application.Application, assemblyPath string, workDir string) ([]archiveInfo, *application.Status, error) {
	chartSet := make(map[string]bool) // To deduplicate
	archives := make([]archiveInfo, 0, len(app.Spec.Modules))
	status := &application.Status{
		ApiVersion:    global.ApplicationApiVersion,
		ChartByModule: make(map[string]application.ChartRef),
	}
	for _, module := range app.Spec.Modules {
		fmt.Printf("%s--- Building module '%s':\n", printPrefix, module.Name)
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
			archives = append(archives, archiveInfo{
				name: targetArchiveName,
				path: targetArchivePath,
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

func buildAssembly(assemblyPath string, archives []archiveInfo) error {
	assemblyArchiveName := path.Join(assemblyPath, "assembly.tgz")
	out, err := os.Create(assemblyArchiveName)
	if err != nil {
		return fmt.Errorf("could not create archive '%s': %w", assemblyArchiveName, err)
	}
	defer out.Close()
	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	err = addToArchive(tw, path.Join(assemblyPath, "original.yaml"), "original.yaml")
	if err != nil {
		return fmt.Errorf("could not add 'original.yaml' to archive '%s': %w", assemblyArchiveName, err)
	}
	err = addToArchive(tw, path.Join(assemblyPath, "groomed.yaml"), "groomed.yaml")
	if err != nil {
		return fmt.Errorf("could not add 'groomed.yaml' to archive '%s': %w", assemblyArchiveName, err)
	}
	err = addToArchive(tw, path.Join(assemblyPath, "index.yaml"), "index.yaml")
	if err != nil {
		return fmt.Errorf("could not add 'index.yaml' to archive '%s': %w", assemblyArchiveName, err)
	}
	err = addToArchive(tw, path.Join(assemblyPath, "status.yaml"), "status.yaml")
	if err != nil {
		return fmt.Errorf("could not add 'status.yaml' to archive '%s': %w", assemblyArchiveName, err)
	}
	for _, archiveInfo := range archives {
		err = addToArchive(tw, archiveInfo.path, archiveInfo.name)
		if err != nil {
			return fmt.Errorf("could not add '%s' to archive '%s': %w", archiveInfo.name, assemblyArchiveName, err)
		}
	}
	return nil
}

func pushImage(assemblyPath string, repository string, tag string, plainHTTP bool) error {
	fmt.Printf("--- push OCI image: %s:%s\n", repository, tag)
	// 0. Create a file store
	ociFs, err := file.New(assemblyPath)
	if err != nil {
		return fmt.Errorf("failed to create OCI file system: %w", err)
	}
	defer ociFs.Close()
	ctx := context.Background()

	// 1. Add files to the file store
	mediaType := global.ApplicationContentMediaType
	fileNames := []string{"assembly.tgz"}
	fileDescriptors := make([]v1.Descriptor, 0, len(fileNames))
	for _, name := range fileNames {
		fileDescriptor, err := ociFs.Add(ctx, name, mediaType, "")
		if err != nil {
			panic(err)
		}
		fileDescriptors = append(fileDescriptors, fileDescriptor)
		//fmt.Printf("file descriptor for %s: %v\n", name, fileDescriptor)
	}

	// Add config stuff
	configFileDescriptor, err := ociFs.Add(ctx, "manifest.json", global.ApplicationConfigMediaType, "")

	// 2. Pack the files and tag the packed manifest
	artifactType := ""
	opts := oras.PackManifestOptions{
		Layers:           fileDescriptors,
		ConfigDescriptor: &configFileDescriptor,
	}
	manifestDescriptor, err := oras.PackManifest(ctx, ociFs, oras.PackManifestVersion1_1, artifactType, opts)
	if err != nil {
		return fmt.Errorf("failed to pack manifest: %w", err)
	}
	//fmt.Println("manifest descriptor:", manifestDescriptor)

	if err = ociFs.Tag(ctx, manifestDescriptor, tag); err != nil {
		return fmt.Errorf("failed to tag manifest: %w", err)
	}

	// 3. Connect to a remote repository
	remoteRepo, err := remote.NewRepository(repository)
	if err != nil {
		return fmt.Errorf("failed to create OCI repository: %w", err)
	}
	remoteRepo.PlainHTTP = plainHTTP

	splits := strings.Split(repository, "/")
	regHost := splits[0]
	userName, secret, err := oci.GetCredentials(regHost)
	if err != nil {
		return fmt.Errorf("failed to get credentials for repository '%s': %v", regHost, err)
	}

	if secret != "" {
		remoteRepo.Client = &auth.Client{
			Client: retry.DefaultClient,
			Cache:  auth.NewCache(),
			Credential: auth.StaticCredential(regHost, auth.Credential{
				Username: userName,
				Password: secret,
			}),
		}
	}
	// 4. Copy from the file store to the remote repository
	_, err = oras.Copy(ctx, ociFs, tag, remoteRepo, tag, oras.DefaultCopyOptions)
	if err != nil {
		return fmt.Errorf("failed to copy OCI image: %w", err)
	}
	fmt.Printf("    Successfully pushed\n")
	return nil
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
			err := addToArchive(tw, thePath, targetFileName)
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

func addToArchive(tw *tar.Writer, filePath string, inArchiveName string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file '%s': %w", filePath, err)
	}
	defer f.Close()

	// Get FileInfo about our file providing file size, mode, etc.
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file '%s': %w", filePath, err)
	}
	// Create a tar Header from the FileInfo data
	header, err := tar.FileInfoHeader(info, info.Name())
	if err != nil {
		return fmt.Errorf("failed to create header for file '%s': %w", filePath, err)
	}
	// https://golang.org/src/archive/tar/common.go?#L626
	header.Name = inArchiveName
	// Write file header to the tar archive
	err = tw.WriteHeader(header)
	if err != nil {
		return fmt.Errorf("failed to write tar header for file '%s': %w", filePath, err)
	}
	// Copy file content to tar archive
	_, err = io.Copy(tw, f)
	if err != nil {
		return fmt.Errorf("failed to copy file %s to archive: %w", filePath, err)
	}
	return nil
}

// Extract the chart name and version from a chart archive
func extractChartInfo(tgzPath string) (chartName string, chartVersion string, err error) {
	ba, err := cmn.ExtractDataFromTgz(tgzPath, "Chart.yaml")
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
