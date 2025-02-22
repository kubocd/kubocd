package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"
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

			// Collect all archives, and reference them in a map
			archiveByModule, err := lookupArchives(srv)
			if err != nil {
				return err
			}

			// Build and push image
			err = pushImage(srv, archiveByModule, workDir, repository, tag)
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
func lookupArchives(srv *service.Service) (archiveByModule map[string]string, err error) {
	archiveByModule = make(map[string]string)
	srv.Status.Charts = make([]service.Chart, 0, len(archiveByModule))
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
				_, helmClient, err := setupHelmRepo(module.Source.HelmRepository.Url)
				if err != nil {
					return nil, fmt.Errorf("module '%s': error on helmRepository settings: %w", module.Name, err)
				}
				_, archive, err = getChartArchiveFromHelmRepo(printPrefix, helmClient, module.Source.HelmRepository.Chart, module.Source.HelmRepository.Version)
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
		archiveByModule[module.Name] = archive
		srv.Status.Charts = append(srv.Status.Charts, service.Chart{
			Module:    module.Name,
			MediaType: fmt.Sprintf(global.ServiceModuleContentMediaType, module.Name),
		})
	}
	//fmt.Printf("\n%s\n", misc.Map2YamlStr(srv))
	return archiveByModule, nil
}

func pushImage(srv *service.Service, archiveByModule map[string]string, workDir string, repository string, tag string) error {
	fmt.Printf("--- push OCI image: %s:%s\n", repository, tag)

	// 0. Create the file store
	fsFolder := path.Join(workDir, "_fs_")
	err := misc.SafeEnsureEmpty(fsFolder)
	if err != nil {
		return err
	}
	fs, err := file.New(fsFolder)
	if err != nil {
		return fmt.Errorf("failed to create OCI file system: %w", err)
	}
	defer fs.Close()
	ctx := context.Background()

	// 1. Add files to the file store
	fileDescriptors := make([]v1.Descriptor, 0, len(archiveByModule)+1)

	// Manage the Manifest file to be added as layer
	manifestFile := path.Join(workDir, "manifest.yaml")
	manifestArchive := path.Join(fsFolder, "_manifest_.tgz")

	err = os.WriteFile(manifestFile, misc.Map2YamlByteA(srv), os.ModePerm)
	if err != nil {
		return err
	}
	err = archiveSingleFile(manifestArchive, manifestFile, "manifest.yaml")
	if err != nil {
		return err
	}
	fileDescriptor, err := fs.Add(ctx, "_manifest_.tgz", global.ServiceManifestMediaType, "")
	if err != nil {
		return err
	}
	fileDescriptors = append(fileDescriptors, fileDescriptor)
	// And copy the chart layers
	for module, archive := range archiveByModule {
		targetArchive := fmt.Sprintf("%s.tgz", module)
		err = misc.CopyFile(archive, path.Join(fsFolder, targetArchive))
		if err != nil {
			return err
		}
		fileDescriptor, err := fs.Add(ctx, targetArchive, fmt.Sprintf(global.ServiceModuleContentMediaType, module), "")
		if err != nil {
			panic(err)
		}
		fileDescriptors = append(fileDescriptors, fileDescriptor)
	}

	// Add config stuff
	// copy the Manifest file to be oci config part
	err = os.WriteFile(path.Join(fsFolder, "manifest.json"), misc.Map2JsonByteA(srv), os.ModePerm)
	if err != nil {
		return err
	}
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
	repo, err := remote.NewRepository(repository)
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
		repo.Client = &auth.Client{
			Client: retry.DefaultClient,
			Cache:  auth.NewCache(),
			Credential: auth.StaticCredential(regHost, auth.Credential{
				Username: userName,
				Password: secret,
			}),
		}
	}
	// 4. Copy from the file store to the remote repository
	_, err = oras.Copy(ctx, fs, tag, repo, tag, oras.DefaultCopyOptions)
	if err != nil {
		return fmt.Errorf("failed to copy OCI image: %w", err)
	}
	fmt.Printf("    Successfully pushed\n")
	return nil
}

func archiveSingleFile(archivePathName string, filePathName string, fileNameInArchive string) error {
	out, err := os.Create(archivePathName)
	if err != nil {
		return fmt.Errorf("could not create archive '%s': %w", archivePathName, err)
	}
	defer out.Close()
	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	err = addToArchive(tw, filePathName, fileNameInArchive)
	if err != nil {
		return err
	}
	return nil
}
