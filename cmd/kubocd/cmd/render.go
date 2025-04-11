package cmd

import (
	"context"
	"fmt"
	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kapi "kubocd/api/v1alpha1"
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/cmd/kubocd/cmd/tgz"
	"kubocd/internal/application"
	"kubocd/internal/configstore"
	"kubocd/internal/controller"
	"kubocd/internal/global"
	"kubocd/internal/k8sapi"
	"kubocd/internal/misc"
	"os"
	"path"
	"path/filepath"
)

var renderParams struct {
	output          string
	workDir         string
	kubocdNamespace string
	namespace       string
}

var renderLog logr.Logger

func init() {
	renderCmd.PersistentFlags().StringVarP(&renderParams.output, "output", "o", "./.render", "Output directory")
	renderCmd.PersistentFlags().StringVarP(&renderParams.workDir, "workDir", "w", "", "working directory. Default to $HOME/.kubocd")
	renderCmd.PersistentFlags().StringVarP(&renderParams.kubocdNamespace, "kubocdNamespace", "", "kubocd", "The namespace where the kubocd controller is installed in (To fetch configs resources)")
	renderCmd.PersistentFlags().StringVarP(&renderParams.namespace, "namespace", "n", "default", "Value to set if release.metadata.namespace is empty")
}

var renderCmd = &cobra.Command{
	Use:   "render <Release manifest> [<Application manifest>]",
	Short: "Render a KuboCD release",
	Args:  cobra.RangeArgs(1, 2),
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// ------------------------------------------- Setup working folder
		if renderParams.workDir == "" {
			dir, err := os.UserHomeDir()
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "Unable to determine home directory: %v\n", err)
				os.Exit(1)
			}
			renderParams.workDir = fmt.Sprintf("%s/.kubocd", dir)
		}
		var err error
		// logger is just used for some functions shared,with the release reconciler. So currently, we hard code level and mode.
		renderLog, err = misc.HandleLog(&misc.LogConfig{
			Level: "info",
			Mode:  "dev",
		})
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Unable to set logging configuration: %v\n", err)
			os.Exit(2)
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		err := func() error {
			output := renderParams.output

			release := &kapi.Release{}
			err := misc.LoadYaml(args[0], release)
			if err != nil {
				return fmt.Errorf("error loading release: %w", err)
			}
			if output != "" {
				output = path.Join(output, release.Name)
				err := misc.SafeEnsureEmpty(output)
				if err != nil {
					return fmt.Errorf("error ensuring output file exists: %w", err)
				}
			}
			if release.Namespace == "" {
				release.Namespace = renderParams.namespace
			}
			controller.GroomRelease(release, renderLog)
			cmn.Dump(output, "release.yaml", release)

			k8sClient, err := k8sapi.GetKubeClient(scheme)
			if err != nil {
				return fmt.Errorf("error getting kubernetes client: %w", err)
			}

			// ------------------------------------------------------------------------ handle config
			configStore := configstore.New()
			err = configStore.Init(context.Background(), k8sClient, renderParams.kubocdNamespace)
			if err != nil {
				return fmt.Errorf("could not fetch config(s): %w", err)
			}
			cmn.Dump(output, "configs.yaml", configStore.ObjectMap())

			// ----------------------------------------------------------------------- Retrieve application
			applicationFolder := ""
			appOriginal := &application.Application{}
			appContainer := &application.AppContainer{}
			var errorOnContainer error
			if len(args) == 2 {
				err = misc.LoadYaml(args[1], appOriginal)
				if err != nil {
					return fmt.Errorf("error loading application: %w", err)
				}
				abs, err := filepath.Abs(args[1])
				if err != nil {
					return fmt.Errorf("error getting absolute path of application: %w", err)
				}
				applicationFolder = filepath.Dir(abs)

				errorOnContainer = appContainer.SetApplication(appOriginal, nil, "0.0.0@sha256:0000000000000000000000000")
			} else {
				var repo, tag string
				kuboAppRedirectSpec, newUrl := configStore.GetKuboAppRedirect(fmt.Sprintf("%s:%s", release.Spec.Application.Repository, release.Spec.Application.Tag))
				if kuboAppRedirectSpec != nil {
					repo, tag, err = misc.DecodeImageUrl(newUrl)
					if err != nil {
						return fmt.Errorf("invalid OCI repository URL: %w", err)
					}
				} else {
					repo = release.Spec.Application.Repository
					tag = release.Spec.Application.Tag
				}
				op := &oci.Operation{
					WorkDir:   renderParams.workDir,
					ImageRepo: repo,
					ImageTag:  tag,
					Insecure:  release.Spec.Application.Insecure,
					Anonymous: false,
				}
				archive, err := oci.GetContentFromOci("# ", op, global.ApplicationContentMediaType)
				if err != nil {
					return fmt.Errorf("error getting OCI content: %w", err)
				}
				//fmt.Printf("# Fetched OCI image content: %s\n\n", archive)
				err = tgz.UnmarshalDataFromTgz(archive, "original.yaml", &appOriginal)
				if err != nil {
					return fmt.Errorf("error unmarshalling OCI content (original.yaml): %w", err)
				}
				status := &application.Status{}
				err = tgz.UnmarshalDataFromTgz(archive, "status.yaml", &status)
				if err != nil {
					return fmt.Errorf("error unmarshalling OCI content (status.yaml): %w", err)
				}
				errorOnContainer = appContainer.SetApplication(appOriginal, status, "0.0.0@sha256:0000000000000000000000000")
			}
			cmn.Dump(output, "application.yaml", appContainer.Application)
			cmn.Dump(output, "default-parameters.yaml", appContainer.DefaultParameters)
			cmn.Dump(output, "default-context.yaml", appContainer.DefaultContext)

			// We better to stop AFTER dump, to ease error solving
			if errorOnContainer != nil {
				return fmt.Errorf("error while storing application in cache: %w", errorOnContainer)
			}
			if appContainer.Status == nil {
				// Compute status to give the map module->helmChart (Need to fetch helm charts)
				assemblyPath := path.Join(renderParams.workDir, "assembly")
				_, appContainer.Status, err = cmn.FetchArchives("", appContainer.Application, assemblyPath, renderParams.workDir, applicationFolder)
				if err != nil {
					return fmt.Errorf("could not fetch application archive: %w", err)
				}
			}
			cmn.Dump(output, "status.yaml", appContainer.Status)

			// ------------------------------------------------------------------------ handle context
			kcontext, contextList, err := controller.ComputeContext(context.Background(), k8sClient, release, configStore, appContainer.DefaultContext)
			if err != nil {
				return fmt.Errorf("could not compute context: %w", err)
			}
			cmn.Dump(output, "context.yaml", kcontext)
			err = appContainer.ValidateContext(kcontext)
			if err != nil {
				return fmt.Errorf("could not validate context: %w", err)
			}
			// ----------------------------------------------------------------------- Handle parameters
			parameters, err := controller.HandleParameters(release, kcontext, configStore, appContainer)
			if err != nil {
				return err
			}
			cmn.Dump(output, "parameters.yaml", parameters)
			// -------------------------------------------------------------------- Render all values
			model := controller.BuildModel(kcontext, parameters, release, configStore)
			cmn.Dump(output, "model.yaml", model)
			rendered, err := appContainer.Application.Render(model)
			if err != nil {
				return fmt.Errorf("could not render application: %w", err)
			}
			// --------------------------------------------------------------------- Handle roles/dependencies
			roles := misc.RemoveDuplicates(append(rendered.Roles, release.Spec.Roles...))
			dependencies := misc.RemoveDuplicates(append(rendered.Dependencies, release.Spec.Dependencies...))
			cmn.Dump(output, "roles.yaml", roles)
			cmn.Dump(output, "dependencies.yaml", dependencies)

			// -------------------------------------------------------------------------Generate OCI repository
			ociRepository := &sourcev1b2.OCIRepository{
				TypeMeta: metav1.TypeMeta{
					Kind:       sourcev1b2.OCIRepositoryKind,
					APIVersion: sourcev1b2.GroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: release.Namespace,
					Name:      fmt.Sprintf(controller.OciRepositoryNameFormat, release.Name),
				},
			}
			err = controller.PopulateOciRepository(ociRepository, release, global.ApplicationContentMediaType, "extract", configStore)
			if err != nil {
				return fmt.Errorf("could not populate OCI repository: %w", err)
			}
			cmn.Dump(output, "ociRepository.yaml", ociRepository)

			// -------------------------------------------------------------------------Generate Usage
			cmn.DumpTxt(output, "usage.txt", rendered.Usage)

			// -------------------------------------------------------------------------Generate Helm repository
			helmRepositoryName := fmt.Sprintf(controller.HelmRepositoryNameFormat, release.Name)
			helmRepositoryNamespace := release.Namespace
			helmRepository := &sourcev1.HelmRepository{
				TypeMeta: metav1.TypeMeta{
					APIVersion: sourcev1.GroupVersion.String(),
					Kind:       sourcev1.HelmRepositoryKind,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      helmRepositoryName,
					Namespace: helmRepositoryNamespace,
				},
			}
			helmRepositoryPath := path.Join("hr", release.Namespace, release.Name)
			repoUrl := fmt.Sprintf("http://%s/%s", "HelmRepoAdvAddr", helmRepositoryPath)
			controller.PopulateHelmRepository(helmRepository, release, repoUrl)
			cmn.Dump(output, "helmRepository.yaml", helmRepository)

			// ---------------------------------------------------------------------- Generate helm releases
			helmReleaseNameByModuleName := make(map[string]string)
			for _, module := range appContainer.Application.Spec.Modules {
				helmReleaseNameByModuleName[module.Name] = controller.BuildHelmReleaseName(release.Name, module.Name)
			}
			for _, module := range appContainer.Application.Spec.Modules {
				enabled := rendered.ModuleRenderedByName[module.Name].Enabled
				if enabled {
					helmRelease := &fluxv2.HelmRelease{
						TypeMeta: metav1.TypeMeta{
							APIVersion: fluxv2.GroupVersion.String(),
							Kind:       fluxv2.HelmReleaseKind,
						},
						ObjectMeta: metav1.ObjectMeta{
							Name:      controller.BuildHelmReleaseName(release.Name, module.Name),
							Namespace: release.Namespace,
						},
					}
					controller.PopulateHelmRelease(helmRelease, release, appContainer, rendered, helmRepositoryName, module, helmReleaseNameByModuleName)
					cmn.Dump(output, fmt.Sprintf("helmRelease-%s-%s.yaml", helmRelease.Namespace, helmRelease.Name), helmRelease)
				}
			}
			fmt.Printf("Contexts: %s\n", misc.FlattenNamespacedNames(contextList))
			return nil
		}()
		if err != nil {
			//fmt.Printf("************** err type: %T\n", err)
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			os.Exit(1)
		}
	},
}
