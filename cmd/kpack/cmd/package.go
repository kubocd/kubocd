package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/repo"
	"kubocd/internal/application"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
	"os"
	"path"
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
}

func init() {
	packageCmd.PersistentFlags().StringVarP(&packageParams.ociRepoPrefix, "ociRepoPrefix", "r", "", "OCI repository prefix (i.e 'quay.io/your-organization/applications'). Can also be specified with OCI_REPO_PREFIX environment variable")
	packageCmd.PersistentFlags().BoolVarP(&packageParams.plainHTTP, "plainHTTP", "p", false, "Use plain HTTP instead of HTTPS")
}

var packageCmd = cobra.Command{
	Use:     "package <Application manifest>",
	Short:   "Assemble a KuboCd Application from a manifest to an OCI image",
	Args:    cobra.ExactArgs(1),
	Aliases: []string{"pack", "build"},

	Run: func(cmd *cobra.Command, args []string) {
		err := func() error {
			app := &application.Application{}
			err := misc.LoadYaml(args[0], app)
			if err != nil {
				return err
			}
			err = app.Groom()
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
			repository = path.Join(repository, app.Metadata.Name)

			tag := app.Metadata.Version

			// ---------- Prepare the target layout
			fsPath := path.Join(workDir, "fs")
			err = misc.SafeEnsureEmpty(fsPath)
			if err != nil {
				return err
			}
			assemblyPath := path.Join(workDir, "assembly")
			err = misc.SafeEnsureEmpty(assemblyPath)
			if err != nil {
				return err
			}
			// -------------------------- Collect all archives, store them in assembly, and reference them in a []moduleInfo
			chartSet, err := fetchArchives(app, assemblyPath)
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
			// Generate the manifest.yaml file to be included in archive
			err = os.WriteFile(path.Join(assemblyPath, "manifest.yaml"), misc.Map2Yaml(app), os.ModePerm)
			if err != nil {
				return err
			}
			// Generate the manifest.json file to be set as config in the image
			err = os.WriteFile(path.Join(assemblyPath, "manifest.json"), misc.Map2Json(app), os.ModePerm)
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
// - Populate the status of the Application with a map of chartInfo by module
func fetchArchives(app *application.Application, assemblyPath string) ([]archiveInfo, error) {
	chartSet := make(map[string]bool) // To deduplicate
	archives := make([]archiveInfo, 0, len(app.Spec.Modules))
	app.Status.ChartByModule = make(map[string]application.ChartRef)
	for _, module := range app.Spec.Modules {
		fmt.Printf("--- Building module '%s':\n", module.Name)
		printPrefix := "    "
		var archive string
		var err error
		if module.Type == global.HelmChartType {
			if module.Source.Oci != nil {
				archive, err = getChartArchiveFromOci(printPrefix, module.Source.Oci.Repository, module.Source.Oci.Insecure, module.Source.Oci.Tag)
				if err != nil {
					return nil, fmt.Errorf("module '%s': could not get helm chart archive: %w", module.Name, err)
				}
			} else if module.Source.HelmRepository != nil {
				_, helmClient, err := setupHelmRepo(module.Source.HelmRepository.Url, module.Name)
				if err != nil {
					return nil, fmt.Errorf("module '%s': error on helmRepository settings: %w", module.Name, err)
				}
				_, archive, err = getChartArchiveFromHelmRepo(printPrefix, helmClient, module.Name, module.Source.HelmRepository.Chart, module.Source.HelmRepository.Version)
				if err != nil {
					return nil, fmt.Errorf("module '%s': could not get helm chart archive: %w", module.Name, err)
				}
			} else if module.Source.Git != nil {
				archive, err = getHelmChartArchiveFromGit(printPrefix, module.Source.Git.Url, module.Source.Git.Branch, module.Source.Git.Tag, module.Source.Git.Path, module.Name)
				if err != nil {
					return nil, fmt.Errorf("module '%s': could not get helm chart archive: %w", module.Name, err)
				}
			} else {
				panic("Unrecognized module source")
			}
		} else {
			panic("Unrecognized module type")
		}
		chartName, chartVersion, err := extractChartInfo(archive)
		if err != nil {
			return nil, err
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
				return nil, fmt.Errorf("cannot copy %s to %s: %w", archive, targetArchivePath, err)
			}
		}
		app.Status.ChartByModule[module.Name] = application.ChartRef{
			Name:    chartName,
			Version: chartVersion,
		}
		fmt.Printf("    Chart: %s:%s\n", chartName, chartVersion)
	}
	return archives, nil
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

	err = addToArchive(tw, path.Join(assemblyPath, "manifest.yaml"), "manifest.yaml")
	if err != nil {
		return fmt.Errorf("could not add 'manifest.yaml' to archive '%s': %w", assemblyArchiveName, err)
	}
	err = addToArchive(tw, path.Join(assemblyPath, "index.yaml"), "index.yaml")
	if err != nil {
		return fmt.Errorf("could not add 'index.yaml' to archive '%s': %w", assemblyArchiveName, err)
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
	fs, err := file.New(assemblyPath)
	if err != nil {
		return fmt.Errorf("failed to create OCI file system: %w", err)
	}
	defer fs.Close()
	ctx := context.Background()

	// 1. Add files to the file store
	mediaType := global.ApplicationContentMediaType
	fileNames := []string{"assembly.tgz"}
	fileDescriptors := make([]v1.Descriptor, 0, len(fileNames))
	for _, name := range fileNames {
		fileDescriptor, err := fs.Add(ctx, name, mediaType, "")
		if err != nil {
			panic(err)
		}
		fileDescriptors = append(fileDescriptors, fileDescriptor)
		//fmt.Printf("file descriptor for %s: %v\n", name, fileDescriptor)
	}

	// Add config stuff
	configFileDescriptor, err := fs.Add(ctx, "manifest.json", global.ApplicationConfigMediaType, "")

	// 2. Pack the files and tag the packed manifest
	artifactType := ""
	opts := oras.PackManifestOptions{
		Layers:           fileDescriptors,
		ConfigDescriptor: &configFileDescriptor,
	}
	manifestDescriptor, err := oras.PackManifest(ctx, fs, oras.PackManifestVersion1_1, artifactType, opts)
	if err != nil {
		return fmt.Errorf("failed to pack manifest: %w", err)
	}
	//fmt.Println("manifest descriptor:", manifestDescriptor)

	if err = fs.Tag(ctx, manifestDescriptor, tag); err != nil {
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
	userName, secret, err := getCredentials(regHost)
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
	_, err = oras.Copy(ctx, fs, tag, remoteRepo, tag, oras.DefaultCopyOptions)
	if err != nil {
		return fmt.Errorf("failed to copy OCI image: %w", err)
	}
	fmt.Printf("    Successfully pushed\n")
	return nil
}
