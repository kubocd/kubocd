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
	"kubocd/cmd/kubocd/cmd/app"
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/internal/application"
	"kubocd/internal/controller"
	"kubocd/internal/global"
	"kubocd/internal/k8sapi"
	"kubocd/internal/misc"
	"os"
	"path"
)

var renderParams struct {
	output  string
	workDir string
}

var renderLog logr.Logger

func init() {
	renderCmd.Flags().StringVarP(&renderParams.output, "output", "o", "", "Output directory")
	renderCmd.PersistentFlags().StringVarP(&renderParams.workDir, "workDir", "w", "", "working directory. Default to $HOME/.kubocd")
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
			if output != "" {
				err := misc.SafeEnsureEmpty(output)
				if err != nil {
					return err
				}
			}

			release := &kapi.Release{}
			err := misc.LoadYaml(args[0], release)
			if err != nil {
				return err
			}
			if release.Namespace == "" {
				release.Namespace = metav1.NamespaceDefault
			}
			controller.GroomRelease(release, renderLog)

			cmn.Dump(output, "release.yaml", release)

			appOriginal := &application.Application{}
			appContainer := &application.AppContainer{}
			var errorOnContainer error
			if len(args) == 2 {
				err = misc.LoadYaml(args[1], appOriginal)
				if err != nil {
					return err
				}
				errorOnContainer = appContainer.SetApplication(appOriginal, nil, "0.0.0@sha256:0000000000000000000000000")
			} else {
				op := &oci.Operation{
					WorkDir:   renderParams.workDir,
					ImageRepo: release.Spec.Application.Repository,
					ImageTag:  release.Spec.Application.Tag,
					Insecure:  release.Spec.Application.Insecure,
					Anonymous: false,
				}

				archive, err := oci.GetContentFromOci("# ", op, global.ApplicationContentMediaType)
				if err != nil {
					return err
				}
				fmt.Printf("# Fetched OCI image content: %s\n\n", archive)
				err = app.UnmarshalDataFromTgz(archive, "original.yaml", &appOriginal)
				if err != nil {
					return err
				}
				status := &application.Status{}
				err = app.UnmarshalDataFromTgz(archive, "status.yaml", &status)
				if err != nil {
					return err
				}
				errorOnContainer = appContainer.SetApplication(appOriginal, status, "0.0.0@sha256:0000000000000000000000000")
			}
			cmn.Dump(output, "application.yaml", appContainer.Application)
			cmn.Dump(output, "default-parameters.yaml", appContainer.DefaultParameters)
			cmn.Dump(output, "default-context.yaml", appContainer.DefaultContext)

			// We better to stop AFTER dump, to ease error solving
			if errorOnContainer != nil {
				return errorOnContainer
			}

			if appContainer.Status == nil {
				// Compute status to give the map module->helmChart (Need to fetch helm charts
				assemblyPath := path.Join(renderParams.workDir, "assembly")
				_, appContainer.Status, err = fetchArchives("# ", appContainer.Application, assemblyPath, renderParams.workDir)
				if err != nil {
					return err
				}
				fmt.Printf("\n")
			}
			cmn.Dump(output, "status.yaml", appContainer.Status)
			k8sClient, err := k8sapi.GetKubeClient(scheme)
			if err != nil {
				return err
			}
			// ------------------------------------------------------------------------ handle context
			kcontext, err := controller.ComputeContext(context.Background(), k8sClient, release, appContainer)
			if err != nil {
				return err
			}
			cmn.Dump(output, "context.yaml", kcontext)
			err = appContainer.ValidateContext(kcontext)
			if err != nil {
				return err
			}
			// ----------------------------------------------------------------------- Handle parameters
			parameters := appContainer.DefaultParameters
			parameters, err = controller.Merge(parameters, release.Spec.Parameters)
			if err != nil {
				return err
			}
			cmn.Dump(output, "parameters.yaml", parameters)
			err = appContainer.ValidateParameters(parameters)
			if err != nil {
				return err
			}
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
			controller.PopulateOciRepository(ociRepository, release, global.ApplicationContentMediaType, "extract")
			cmn.Dump(output, "ociRepository.yaml", ociRepository)

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
			for _, module := range appContainer.Application.Spec.Modules {
				helmRelease := &fluxv2.HelmRelease{
					TypeMeta: metav1.TypeMeta{
						APIVersion: fluxv2.GroupVersion.String(),
						Kind:       fluxv2.HelmReleaseKind,
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf(controller.HelmReleaseNameFormat, release.Name, module.Name),
						Namespace: release.Namespace,
					},
				}
				controller.PopulateHelmRelease(helmRelease, release, appContainer, helmRepositoryName, module.Name)
				cmn.Dump(output, fmt.Sprintf("helmRelease-%s-%s.yaml", helmRelease.Namespace, helmRelease.Name), helmRelease)
			}
			return nil
		}()
		if err != nil {
			//fmt.Printf("************** err type: %T\n", err)
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			os.Exit(1)
		}
	},
}

//func appendSettings(ctx context.Context, client client.Client, newSettings []kapi.NamespacedName, defaultNamespace string, collector []*kapi.Setting) ([]*kapi.Setting, error) {
//	for _, settingNs := range newSettings {
//		if settingNs.Namespace == "" {
//			settingNs.Namespace = defaultNamespace
//		}
//		setting := &kapi.Setting{}
//		err := client.Get(ctx, settingNs.ToObjectKey(), setting)
//		if err != nil {
//			return nil, err
//		}
//		if len(setting.Spec.Parents) > 0 {
//			collector, err = appendSettings(ctx, client, setting.Spec.Parents, setting.Namespace, collector)
//			if err != nil {
//				return nil, err
//			}
//		}
//		collector = append(collector, setting)
//	}
//	return collector, nil
//}
//
//func computeSetting(ctx context.Context, client client.Client, settings []kapi.NamespacedName, defaultNamespace string) (*kapi.Setting, error) {
//	collector := make([]*kapi.Setting, 0)
//	var err error
//	collector, err = appendSettings(ctx, client, settings, defaultNamespace, collector)
//	if err != nil {
//		return nil, err
//	}
//	for _, setting := range collector {
//		fmt.Printf("Setting: %s:%s\n", setting.Name, setting.Namespace)
//	}
//	return nil, nil
//}
