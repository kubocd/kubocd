package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	securejoin "github.com/cyphar/filepath-securejoin"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/repo"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"kubocd/internal/service"
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

type moduleInfo struct {
	moduleName  string
	archiveName string // The file in ../assembly folder
	archivePath string // The path where to access the archive
}

var packageCmd = cobra.Command{
	Use:     "package <Service manifest> <repository>:<tag>",
	Short:   "Assemble a Service from a manifest to an OCI image",
	Args:    cobra.ExactArgs(2),
	Aliases: []string{"pack", "build"},

	Run: func(cmd *cobra.Command, args []string) {
		err := func() error {
			srv := &service.Service{}
			err := misc.LoadYaml(args[0], srv)
			if err != nil {
				return err
			}
			err = srv.Groom()
			if err != nil {
				return err
			}

			// --------------------- Handle entry parameters
			var repository string
			var tag string
			splits := strings.Split(args[1], ":")
			if len(splits) == 2 {
				repository = splits[0]
				tag = splits[1]
			} else {
				repository = splits[0]
				tag = "latest"
			}

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
			moduleInfos, err := fetchArchives(srv, assemblyPath)
			if err != nil {
				return err
			}

			fmt.Printf("--- Packaging\n")

			// ------------------------------------- Build index.yaml file to be an helm repository
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
			err = os.WriteFile(path.Join(assemblyPath, "manifest.yaml"), misc.Map2Yaml(srv), os.ModePerm)
			if err != nil {
				return err
			}
			// Generate the manifest.json file to be set as config in the image
			err = os.WriteFile(path.Join(assemblyPath, "manifest.json"), misc.Map2Json(srv), os.ModePerm)
			if err != nil {
				return err
			}

			// ------------------------------------- Package
			fmt.Printf("    Wrap all in assembly.tgz\n")
			err = buildAssembly(assemblyPath, moduleInfos)
			if err != nil {
				return err
			}

			// Build and push image
			err = pushImage(srv, assemblyPath, repository, tag)
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

// lookupArchive load all module's archive and reference them in two locations:
// - In the Status part of the service
// - A map archiveByModule, as return value
func fetchArchives(srv *service.Service, assemblyPath string) (moduleInfos []moduleInfo, err error) {
	moduleInfos = make([]moduleInfo, 0, len(srv.Spec.Modules))
	for _, module := range srv.Spec.Modules {
		fmt.Printf("--- Building module '%s':\n", module.Name)
		printPrefix := "    "
		var archive string
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
		targetArchiveName := fmt.Sprintf("%s.tgz", module.Name)
		targetArchivePath, err := securejoin.SecureJoin(assemblyPath, targetArchiveName)
		if err != nil {
			return nil, fmt.Errorf("could not build module '%s' target archive path: %v", module.Name, err)
		}
		err = misc.CopyFile(archive, targetArchivePath)
		if err != nil {
			return nil, fmt.Errorf("cannot copy %s to %s: %w", archive, targetArchivePath, err)
		}
		moduleInfos = append(moduleInfos, moduleInfo{
			moduleName:  module.Name,
			archiveName: targetArchiveName,
			archivePath: targetArchivePath,
		})
	}
	return moduleInfos, nil
}

func buildAssembly(assemblyPath string, moduleInfos []moduleInfo) error {
	archiveName := path.Join(assemblyPath, "assembly.tgz")
	out, err := os.Create(archiveName)
	if err != nil {
		return fmt.Errorf("could not create archive '%s': %w", archiveName, err)
	}
	defer out.Close()
	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	err = addToArchive(tw, path.Join(assemblyPath, "manifest.yaml"), "manifest.yaml")
	if err != nil {
		return fmt.Errorf("could not add 'manifest.yaml' to archive '%s': %w", archiveName, err)
	}
	err = addToArchive(tw, path.Join(assemblyPath, "index.yaml"), "index.yaml")
	if err != nil {
		return fmt.Errorf("could not add 'index.yaml' to archive '%s': %w", archiveName, err)
	}
	for _, mInfo := range moduleInfos {
		err = addToArchive(tw, mInfo.archivePath, mInfo.archiveName)
		if err != nil {
			return fmt.Errorf("could not add '%s' to archive '%s': %w", mInfo.archiveName, archiveName, err)
		}
	}
	return nil
}

func pushImage(srv *service.Service, assemblyPath string, repository string, tag string) error {
	fmt.Printf("--- push OCI image: %s:%s\n", repository, tag)
	// 0. Create a file store
	fs, err := file.New(assemblyPath)
	if err != nil {
		return fmt.Errorf("failed to create OCI file system: %w", err)
	}
	defer fs.Close()
	ctx := context.Background()

	// 1. Add files to the file store
	mediaType := global.ServiceContentMediaType
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
	configFileDescriptor, err := fs.Add(ctx, "manifest.json", global.ServiceConfigMediaType, "")

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

//func pushImage(srv *service.Service, archiveByModule map[string]string, workDir string, repository string, tag string) error {
//	fmt.Printf("--- push OCI image: %s:%s\n", repository, tag)
//
//	// 0. Create the file store
//	fsFolder := path.Join(workDir, "_fs_")
//	err := misc.SafeEnsureEmpty(fsFolder)
//	if err != nil {
//		return err
//	}
//	fs, err := file.New(fsFolder)
//	if err != nil {
//		return fmt.Errorf("failed to create OCI file system: %w", err)
//	}
//	defer fs.Close()
//	ctx := context.Background()
//
//	// 1. Add files to the file store
//	fileDescriptors := make([]v1.Descriptor, 0, len(archiveByModule)+1)
//
//	// Manage the Manifest file to be added as layer
//	manifestFile := path.Join(workDir, "manifest.yaml")
//	manifestArchive := path.Join(fsFolder, "_manifest_.tgz")
//
//	err = os.WriteFile(manifestFile, misc.Map2YamlByteA(srv), os.ModePerm)
//	if err != nil {
//		return err
//	}
//	err = archiveSingleFile(manifestArchive, manifestFile, "manifest.yaml")
//	if err != nil {
//		return err
//	}
//	fileDescriptor, err := fs.Add(ctx, "_manifest_.tgz", global.ServiceManifestMediaType, "")
//	if err != nil {
//		return err
//	}
//	fileDescriptors = append(fileDescriptors, fileDescriptor)
//	// And copy the chart layers
//	for module, archive := range archiveByModule {
//		targetArchive := fmt.Sprintf("%s.tgz", module)
//		err = misc.CopyFile(archive, path.Join(fsFolder, targetArchive))
//		if err != nil {
//			return err
//		}
//		fileDescriptor, err := fs.Add(ctx, targetArchive, fmt.Sprintf(global.ServiceModuleContentMediaType, module), "")
//		if err != nil {
//			panic(err)
//		}
//		fileDescriptors = append(fileDescriptors, fileDescriptor)
//	}
//
//	// Add config stuff
//	// copy the Manifest file to be oci config part
//	err = os.WriteFile(path.Join(fsFolder, "manifest.json"), misc.Map2JsonByteA(srv), os.ModePerm)
//	if err != nil {
//		return err
//	}
//	configFileDescriptor, err := fs.Add(ctx, "manifest.json", global.ServiceConfigMediaType, "")
//
//	// 2. Pack the files and tag the packed manifest
//	artifactType := ""
//	opts := oras.PackManifestOptions{
//		Layers:           fileDescriptors,
//		ConfigDescriptor: &configFileDescriptor,
//	}
//	manifestDescriptor, err := oras.PackManifest(ctx, fs, oras.PackManifestVersion1_1, artifactType, opts)
//	if err != nil {
//		return fmt.Errorf("failed to pack manifest: %w", err)
//	}
//	//fmt.Println("manifest descriptor:", manifestDescriptor)
//
//	if err = fs.Tag(ctx, manifestDescriptor, tag); err != nil {
//		return fmt.Errorf("failed to tag manifest: %w", err)
//	}
//
//	// 3. Connect to a remote repository
//	repo, err := remote.NewRepository(repository)
//	if err != nil {
//		return fmt.Errorf("failed to create OCI repository: %w", err)
//	}
//
//	splits := strings.Split(repository, "/")
//	regHost := splits[0]
//	userName, secret, err := getCredentials(regHost)
//	if err != nil {
//		return fmt.Errorf("failed to get credentials for repository '%s': %v", regHost, err)
//	}
//
//	if secret != "" {
//		repo.Client = &auth.Client{
//			Client: retry.DefaultClient,
//			Cache:  auth.NewCache(),
//			Credential: auth.StaticCredential(regHost, auth.Credential{
//				Username: userName,
//				Password: secret,
//			}),
//		}
//	}
//	// 4. Copy from the file store to the remote repository
//	_, err = oras.Copy(ctx, fs, tag, repo, tag, oras.DefaultCopyOptions)
//	if err != nil {
//		return fmt.Errorf("failed to copy OCI image: %w", err)
//	}
//	fmt.Printf("    Successfully pushed\n")
//	return nil
//}
//
//func archiveSingleFile(archivePathName string, filePathName string, fileNameInArchive string) error {
//	out, err := os.Create(archivePathName)
//	if err != nil {
//		return fmt.Errorf("could not create archive '%s': %w", archivePathName, err)
//	}
//	defer out.Close()
//	gw := gzip.NewWriter(out)
//	defer gw.Close()
//	tw := tar.NewWriter(gw)
//	defer tw.Close()
//
//	err = addToArchive(tw, filePathName, fileNameInArchive)
//	if err != nil {
//		return err
//	}
//	return nil
//}
