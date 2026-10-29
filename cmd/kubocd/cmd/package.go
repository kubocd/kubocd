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

package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/cmd/kubocd/cmd/tgz"
	"kubocd/internal/global"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"kubocd/internal/tmpl"
	"log/slog"
	"os"
	"path"
	"path/filepath"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"
	"helm.sh/helm/v4/pkg/repo/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry/remote"
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
	sourceOptions cmn.SourceOptions
	logConfig     misc.LogConfig
	ociRepoPrefix string
	plainHTTP     bool
	workDir       string
}

func init() {
	packageCmd.PersistentFlags().BoolVar(&packageParams.sourceOptions.AllowInsecureCredentials,
		"allowInsecureCredentials", false, oci.CredentialHelp)
	packageParams.sourceOptions.ArchiveLimits = registerArchiveFlags(packageCmd)
	packageCmd.PersistentFlags().BoolVar(&packageParams.sourceOptions.AllowExternalLinks, "allowExternalLinks", false,
		"Allow Git and local charts to read files and file:// dependencies outside their source root; "+
			"special files remain refused.")

	packageCmd.PersistentFlags().StringVar(&packageParams.logConfig.Level, "logLevel", "INFO", "Log level")
	packageCmd.PersistentFlags().StringVarP(&packageParams.ociRepoPrefix, "ociRepoPrefix", "r", "", "OCI repository prefix (i.e 'quay.io/your-organization/packages'). Can also be specified with OCI_REPO_PREFIX environment variable")
	packageCmd.PersistentFlags().BoolVarP(&packageParams.plainHTTP, "plainHTTP", "p", false,
		"Use plain HTTP instead of HTTPS when pushing image; authentication over HTTP requires --allowInsecureCredentials")
	packageCmd.PersistentFlags().StringVarP(&packageParams.workDir, "workDir", "w", "", "Working directory. Default to $HOME/.kubocd")

}

var packageCmd = &cobra.Command{
	Use:     "package <Package manifest>",
	Short:   "Assemble a KuboCd Package from a manifest to an OCI image",
	Long:    "Assemble a KuboCd Package from a manifest to an OCI image.\n\n" + cmn.ChartSourceHelp,
	Args:    cobra.ExactArgs(1),
	Aliases: []string{"pack", "build"},
	Example: `	Build and push a package:
	$ kubocd package podinfo-p01.yaml --ociRepoPrefix quay.io/kubodoc/packages

	or
	$ export OCI_REPO_PREFIX=quay.io/kubodoc/packages
	$ kubocd package podinfo-p01.yaml`,

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
		packageParams.logConfig.Mode = "text"
		packageLogger, err := misc.NewSlogLogger(&packageParams.logConfig)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Unable to load logging configuration: %v\n", err)
			os.Exit(2)
		}
		slog.SetDefault(packageLogger)
	},
	Run: func(cmd *cobra.Command, args []string) {
		err := pack(args[0])
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			os.Exit(1)
		}
	},
}

func pack(pck string) error {
	fmt.Printf("\n====================================== Packaging package '%s'\n", pck)
	pckOriginal := &kubopackage.Package{}
	pckGroomed := &kubopackage.Package{}
	err := misc.LoadYaml(pck, pckOriginal, pckGroomed)
	if err != nil {
		return err
	}
	err = pckGroomed.Groom(nil, tmpl.PackageFunctions(false))
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(pck)
	if err != nil {
		return err
	}
	packageFolder := filepath.Dir(abs)

	// --------------------- Handle entry parameters
	repository := packageParams.ociRepoPrefix
	if repository == "" {
		repository = os.Getenv("OCI_REPO_PREFIX")
		if repository == "" {
			return fmt.Errorf("an OCI repository prefix must be definded. Use OCI_REPO_PREFIX environment variable or --ociRepoPrefix option")
		}
	}
	repository = path.Join(repository, pckGroomed.Name)

	tag := pckGroomed.Tag

	// ---------- Prepare the target layout
	work, err := misc.OpenRootedDir(packageParams.workDir)
	if err != nil {
		return err
	}
	defer func() { _ = work.Root.Close() }()
	if _, err := misc.PrepareDir(work, "fs"); err != nil {
		return err
	}
	assembly, err := misc.PrepareDir(work, "assembly")
	if err != nil {
		return err
	}
	assemblyPath := assembly.Path()
	// -------------------------- Collect all archives, store them in assembly, and reference them in a []moduleInfo
	chartSet, status, err := cmn.FetchArchivesAt("", pckGroomed, assembly, work,
		packageFolder, packageParams.sourceOptions)
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
	err = assembly.WriteFile("index.yaml", misc.Any2Yaml(index), os.ModePerm)
	if err != nil {
		return err
	}
	// Generate the original.yaml file to be included in archive
	err = assembly.WriteFile("original.yaml", misc.Any2Yaml(pckOriginal), os.ModePerm)
	if err != nil {
		return err
	}
	// Generate the groomed.yaml file to be included in archive
	err = assembly.WriteFile("groomed.yaml", misc.Any2Yaml(pckGroomed), os.ModePerm)
	if err != nil {
		return err
	}
	// Generate the groomed.yaml file to be included in archive
	err = assembly.WriteFile("status.yaml", misc.Any2Yaml(status), os.ModePerm)
	if err != nil {
		return err
	}
	// Generate the manifest.json file to be set as config in the image
	err = assembly.WriteFile("manifest.json", misc.Map2Json(pckGroomed), os.ModePerm)
	if err != nil {
		return err
	}

	// ------------------------------------- Package
	fmt.Printf("    Wrap all in assembly.tgz\n")
	err = buildAssembly(assembly, chartSet)
	if err != nil {
		return err
	}

	// Build and push image
	err = pushImage(assemblyPath, repository, tag, packageParams.plainHTTP,
		oci.CredentialOptions{AllowInsecureCredentials: packageParams.sourceOptions.AllowInsecureCredentials})
	if err != nil {
		return err
	}
	return nil

}

func buildAssembly(assembly misc.RootedDir, archives []cmn.ArchiveInfo) (err error) {
	out, err := assembly.Create("assembly.tgz")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	gw := gzip.NewWriter(out)
	defer func() { err = errors.Join(err, gw.Close()) }()
	tw := tar.NewWriter(gw)
	defer func() { err = errors.Join(err, tw.Close()) }()
	for _, name := range []string{"original.yaml", "groomed.yaml", "index.yaml", "status.yaml"} {
		if err := tgz.AddToArchiveAt(tw, assembly, name, name); err != nil {
			return err
		}
	}
	for _, archive := range archives {
		if err := tgz.AddToArchiveAt(tw, assembly, archive.Name, archive.Name); err != nil {
			return err
		}
	}
	return nil
}

func pushImage(assemblyPath string, repository string, tag string, plainHTTP bool,
	credentialOptions ...oci.CredentialOptions) error {
	fmt.Printf("--- push OCI image: %s:%s\n", repository, tag)
	// 0. Create a file store
	ociFs, err := file.New(assemblyPath)
	if err != nil {
		return fmt.Errorf("failed to create OCI file system: %w", err)
	}
	defer ociFs.Close()
	ctx := context.Background()

	// 1. Add files to the file store
	mediaType := global.PackageContentMediaType
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
	configFileDescriptor, err := ociFs.Add(ctx, "manifest.json", global.PackageConfigMediaType, "")
	if err != nil {
		return fmt.Errorf("failed to add manifest.json to the OCI file system: %w", err)
	}

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

	options := oci.CredentialOptions{}
	if len(credentialOptions) > 0 {
		options = credentialOptions[0]
	}
	remoteRepo.Client, err = oci.NewRegistryClient(remoteRepo.Reference.Registry, plainHTTP, false, options)
	if err != nil {
		return err
	}
	// 4. Copy from the file store to the remote repository
	_, err = oras.Copy(ctx, ociFs, tag, remoteRepo, tag, oras.DefaultCopyOptions)
	if err != nil {
		return fmt.Errorf("failed to copy OCI image: %w", err)
	}
	fmt.Printf("    Successfully pushed\n")
	return nil
}
