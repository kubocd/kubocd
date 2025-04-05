package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/repo"
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/cmd/kubocd/cmd/tgz"
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
	"path/filepath"
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

var packageParams struct {
	ociRepoPrefix string
	plainHTTP     bool
	workDir       string
}

func init() {
	packageCmd.PersistentFlags().StringVarP(&packageParams.ociRepoPrefix, "ociRepoPrefix", "r", "", "OCI repository prefix (i.e 'quay.io/your-organization/applications'). Can also be specified with OCI_REPO_PREFIX environment variable")
	packageCmd.PersistentFlags().BoolVarP(&packageParams.plainHTTP, "plainHTTP", "p", false, "Use plain HTTP instead of HTTPS when pushing image")
	packageCmd.PersistentFlags().StringVarP(&packageParams.workDir, "workDir", "w", "", "working directory. Default to $HOME/.kubocd")

}

var packageCmd = &cobra.Command{
	Use:     "package <Application manifest>",
	Short:   "Assemble a KuboCd Application from a manifest to an OCI image",
	Args:    cobra.MinimumNArgs(1),
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
		err := pack(args[0])
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			os.Exit(1)
		}
		// We can't loop as the https://github.com/mittwald/go-helm-client library we used does not support to be
		// called twice with the same repo name (see helmrepo.SetupHelmRepo)
		// Need to fix this (patch the lib ?) to allow this loop.
		//for _, app := range args {
		//	err := pack(app)
		//	if err != nil {
		//		_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
		//		os.Exit(1)
		//	}
		//}
	},
}

func pack(app string) error {
	fmt.Printf("\n====================================== Packaging application '%s'\n", app)
	appOriginal := &application.Application{}
	appGroomed := &application.Application{}
	err := misc.LoadYaml(app, appOriginal, appGroomed)
	if err != nil {
		return err
	}
	err = appGroomed.Groom()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(app)
	if err != nil {
		return err
	}
	applicationFolder := filepath.Dir(abs)

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
	chartSet, status, err := cmn.FetchArchives("", appGroomed, assemblyPath, packageParams.workDir, applicationFolder)
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
	err = os.WriteFile(path.Join(assemblyPath, "index.yaml"), misc.Any2Yaml(index), os.ModePerm)
	if err != nil {
		return err
	}
	// Generate the original.yaml file to be included in archive
	err = os.WriteFile(path.Join(assemblyPath, "original.yaml"), misc.Any2Yaml(appOriginal), os.ModePerm)
	if err != nil {
		return err
	}
	// Generate the groomed.yaml file to be included in archive
	err = os.WriteFile(path.Join(assemblyPath, "groomed.yaml"), misc.Any2Yaml(appGroomed), os.ModePerm)
	if err != nil {
		return err
	}
	// Generate the groomed.yaml file to be included in archive
	err = os.WriteFile(path.Join(assemblyPath, "status.yaml"), misc.Any2Yaml(status), os.ModePerm)
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

}

func buildAssembly(assemblyPath string, archives []cmn.ArchiveInfo) error {
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

	err = tgz.AddToArchive(tw, path.Join(assemblyPath, "original.yaml"), "original.yaml")
	if err != nil {
		return fmt.Errorf("could not add 'original.yaml' to archive '%s': %w", assemblyArchiveName, err)
	}
	err = tgz.AddToArchive(tw, path.Join(assemblyPath, "groomed.yaml"), "groomed.yaml")
	if err != nil {
		return fmt.Errorf("could not add 'groomed.yaml' to archive '%s': %w", assemblyArchiveName, err)
	}
	err = tgz.AddToArchive(tw, path.Join(assemblyPath, "index.yaml"), "index.yaml")
	if err != nil {
		return fmt.Errorf("could not add 'index.yaml' to archive '%s': %w", assemblyArchiveName, err)
	}
	err = tgz.AddToArchive(tw, path.Join(assemblyPath, "status.yaml"), "status.yaml")
	if err != nil {
		return fmt.Errorf("could not add 'status.yaml' to archive '%s': %w", assemblyArchiveName, err)
	}
	for _, archiveInfo := range archives {
		err = tgz.AddToArchive(tw, archiveInfo.Path, archiveInfo.Name)
		if err != nil {
			return fmt.Errorf("could not add '%s' to archive '%s': %w", archiveInfo.Name, assemblyArchiveName, err)
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
