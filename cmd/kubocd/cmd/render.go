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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	kapi "kubocd/api/v1alpha1"
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/cmd/kubocd/cmd/tgz"
	"kubocd/internal/configstore"
	"kubocd/internal/controller"
	"kubocd/internal/global"
	"kubocd/internal/k8sapi"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"kubocd/internal/tmpl"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var renderParams struct {
	sourceOptions   cmn.SourceOptions
	output          string
	workDir         string
	kubocdNamespace string
	namespace       string
	debug           bool
	trace           bool
	conditionLimits controller.ConditionLimits
	skipConditions  bool
	allowDNSLookups bool

	ociRepositoryOptions controller.OCIRepositoryOptions
}

var renderLog logr.Logger

func init() {
	registerConditionFlags(renderCmd, &renderParams.conditionLimits)
	renderCmd.PersistentFlags().BoolVar(&renderParams.sourceOptions.AllowInsecureCredentials,
		"allowInsecureCredentials", false, oci.CredentialHelp)
	renderParams.sourceOptions.ArchiveLimits = registerArchiveFlags(renderCmd)
	renderCmd.PersistentFlags().BoolVar(&renderParams.sourceOptions.AllowExternalLinks, "allowExternalLinks", false,
		"Allow Git and local charts to read files and file:// dependencies outside their source root; "+
			"special files remain refused.")

	renderCmd.PersistentFlags().BoolVar(&renderParams.ociRepositoryOptions.AllowRedirectVerifyDrop,
		"allowRedirectVerifyDrop", false,
		"Allow a matching packageRedirect without verify to remove Release signature verification.")

	renderCmd.PersistentFlags().BoolVar(&renderParams.ociRepositoryOptions.AllowReleaseInsecureSource,
		"allowReleaseInsecureSource", false,
		"Allow Release package sources to use insecure HTTP without a matching packageRedirect.")

	renderCmd.PersistentFlags().StringVarP(&renderParams.output, "output", "o", "./.render",
		"Output directory; an empty value prints top-level dumps to stdout and leaves charts/, modules/ "+
			"and connection append files in the current directory")
	renderCmd.PersistentFlags().StringVarP(&renderParams.workDir, "workDir", "w", "", "working directory. Default to $HOME/.kubocd")
	renderCmd.PersistentFlags().StringVarP(&renderParams.kubocdNamespace, "kubocdNamespace", "", "kubocd", "The namespace where the kubocd controller is installed in (To fetch configs resources)")
	renderCmd.PersistentFlags().StringVarP(&renderParams.namespace, "namespace", "n", "default", "Value to set if release.metadata.namespace is empty")
	renderCmd.PersistentFlags().BoolVar(&renderParams.debug, "debug", false, "Enable debug logging")
	renderCmd.PersistentFlags().BoolVar(&renderParams.trace, "trace", false, "Enable trace logging")
	renderCmd.PersistentFlags().BoolVarP(&renderParams.skipConditions, "skipConditions", "s", false,
		"Skip condition evaluation; core Secret/ConfigMap and Release target namespace restrictions still apply")
	renderCmd.PersistentFlags().BoolVar(&renderParams.allowDNSLookups, "allowDNSLookups", false,
		"Let the package templates resolve host names with resolveDNS, as the controller option of the same name")
}

// renderPackageFunctions returns the functions of the package templates,
// under the resolveDNS policy of --allowDNSLookups.
func renderPackageFunctions() tmpl.Functions {
	return tmpl.PackageFunctions(renderParams.allowDNSLookups)
}

var renderCmd = &cobra.Command{
	Use:   "render <Release manifest> [<package manifest>]",
	Short: "Render a KuboCD release",
	Long:  "Render a KuboCD release.\n\n" + cmn.ChartSourceHelp,
	Args:  cobra.RangeArgs(1, 2),
	Example: `	Preview a Release.
	$ render releases/podinfo2-ctx.yaml

	Preview a Release using an alternate package manifest.
	$ kubocd render releases/podinfo1.yaml packages/podinfo-p01.yaml`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if err := renderParams.conditionLimits.Validate(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
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
		// logger is just used for some functions shared,with the release reconciler. So currently, we hard code mode.
		level := "info"
		if renderParams.debug {
			level = "debug"
		}
		if renderParams.trace {
			level = "trace"
		}
		renderLog, err = misc.HandleLog(&misc.LogConfig{
			Level: level,
			Mode:  "dev",
		})
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Unable to set logging configuration: %v\n", err)
			os.Exit(2)
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		err := func() error {
			renderLog.V(1).Info("Debug mode set")
			renderLog.V(2).Info("Trace mode set")
			output := renderParams.output

			release := &kapi.Release{}
			err := misc.LoadYaml(args[0], release)
			if err != nil {
				return fmt.Errorf("error loading release: %w", err)
			}
			target, output, err := misc.PrepareOutput(output, release.Name)
			if err != nil {
				return fmt.Errorf("error ensuring output file exists: %w", err)
			}
			defer func() { _ = target.Root.Close() }()
			work, err := misc.OpenRootedDir(renderParams.workDir)
			if err != nil {
				return err
			}
			defer func() { _ = work.Root.Close() }()
			if release.Namespace == "" {
				release.Namespace = renderParams.namespace
			}
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

			//------------------------------------------------------------------------- Groom release
			controller.GroomRelease(release, renderLog, configStore)
			cmn.Dump(output, "release.yaml", release)

			ociRepository := &sourcev1.OCIRepository{
				TypeMeta: metav1.TypeMeta{
					Kind:       sourcev1.OCIRepositoryKind,
					APIVersion: sourcev1.GroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: release.Namespace,
					Name:      fmt.Sprintf(controller.OciRepositoryNameFormat, release.Name),
				},
			}
			err = controller.PopulateOciRepository(ociRepository, release, global.PackageContentMediaType, "extract",
				configStore, renderParams.ociRepositoryOptions)
			if err != nil {
				return fmt.Errorf("could not populate OCI repository: %w", err)
			}

			// ----------------------------------------------------------------------- Retrieve package
			packageFolder := ""
			pkgOriginal := &kubopackage.Package{}
			pkgContainer := &kubopackage.PckContainer{}
			chartsDir, err := target.Child("charts")
			if err != nil {
				return err
			}
			var errorOnContainer error
			if len(args) == 2 {
				err = misc.LoadYaml(args[1], pkgOriginal)
				if err != nil {
					return fmt.Errorf("error loading package: %w", err)
				}
				abs, err := filepath.Abs(args[1])
				if err != nil {
					return fmt.Errorf("error getting absolute path of package: %w", err)
				}
				packageFolder = filepath.Dir(abs)

				// Status is computed right after, to have a groomed version of the package
				errorOnContainer = pkgContainer.SetPackage(pkgOriginal, nil, "0.0.0@sha256:0000000000000000000000000",
					configStore, renderPackageFunctions())
				// Compute status to give the map module->helmChart (Need to fetch helm charts)
				if err := fetchRenderCharts(work, target, pkgContainer, packageFolder); err != nil {
					return err
				}

			} else {
				op := &oci.Operation{
					AllowInsecureCredentials: renderParams.sourceOptions.AllowInsecureCredentials,
					WorkDir:                  renderParams.workDir,
					ImageRepo:                strings.TrimPrefix(ociRepository.Spec.URL, "oci://"),
					ImageTag:                 ociRepository.Spec.Reference.Tag,
					Insecure:                 ociRepository.Spec.Insecure,
					Anonymous:                false,
				}
				archive, err := oci.GetContentFromOci("# ", op, global.PackageContentMediaType)
				if err != nil {
					return fmt.Errorf("error getting OCI content: %w", err)
				}
				renderLog.V(1).Info("OCI fetched successfully", "archive", archive)
				//fmt.Printf("# Fetched OCI image content: %s\n\n", archive)
				archiveName, err := work.Relative(archive)
				if err != nil {
					return err
				}
				status := &kubopackage.Status{}
				if err := readRenderPackageMetadata(work, archiveName, pkgOriginal, status); err != nil {
					return err
				}

				errorOnContainer = pkgContainer.SetPackage(pkgOriginal, status, "0.0.0@sha256:0000000000000000000000000",
					configStore, renderPackageFunctions())
				renderLog.V(1).Info("Setting package in pkgContainer OK")

				if err := extractRenderPackage(work, archiveName, target, status); err != nil {
					return err
				}

			}
			cmn.Dump(output, "package.yaml", pkgContainer.Package)
			cmn.Dump(output, "default-parameters.yaml", pkgContainer.DefaultParameters)
			cmn.Dump(output, "default-context.yaml", pkgContainer.DefaultContext)

			// We better to stop AFTER dump, to ease error solving
			if errorOnContainer != nil {
				return fmt.Errorf("error while storing package in cache: %w", errorOnContainer)
			}
			cmn.Dump(output, "status.yaml", pkgContainer.Status)

			// ------------------------------------------------------------------------ handle context
			kcontext, contextList, err := controller.ComputeContext(context.Background(), k8sClient, release, configStore, pkgContainer.DefaultContext)
			if err != nil {
				return fmt.Errorf("could not compute context: %w", err)
			}
			cmn.Dump(output, "context.yaml", kcontext)
			err = pkgContainer.ValidateContext(kcontext)
			if err != nil {
				return fmt.Errorf("could not validate context: %w", err)
			}
			// ----------------------------------------------------------------------- Handle parameters
			parameters, err := controller.HandleParameters(release, kcontext, configStore, pkgContainer)
			if err != nil {
				return err
			}
			cmn.Dump(output, "parameters.yaml", parameters)

			// -------------------------------------------------------------------- Build model without input
			model, err := controller.BuildModel(kcontext, parameters, release, configStore)
			if err != nil {
				return err
			}

			// -------------------------------------------------------------------- Render inputs
			inputsRendered, err := pkgContainer.Package.RenderInputs(model, release.Namespace)
			if err != nil {
				return err
			}
			cmn.Dump(output, "inputs.yaml", inputsRendered)
			// -------------------------------------------------------------------- Enrich model with inputs
			helper := &buildInputModelHelper{
				Client: k8sClient,
			}

			buildInputModelResult, err := controller.BuildInputModel(context.Background(), helper, inputsRendered)
			if err != nil {
				return err
			}
			cmn.Dump(output, "watchedInputConnections.yaml", buildInputModelResult.WatchedInputConnections)
			if len(buildInputModelResult.Messages) > 0 {
				return fmt.Errorf("missing connection(s):\n  %s", strings.Join(buildInputModelResult.Messages, "\n  "))
			}
			model["Inputs"] = buildInputModelResult.InputModel
			model["InputLists"] = buildInputModelResult.InputListModel
			// -------------------------------------------------------------------- Render all values

			cmn.Dump(output, "model.yaml", model)
			rendered, err := pkgContainer.Package.Render(model, release)
			if err != nil {
				return fmt.Errorf("could not render package: %w", err)
			}
			rendered.Inputs = inputsRendered // Just for the dump.
			cmn.Dump(output, "rendered.yaml", rendered)
			// --------------------------------------------------------------------- Handle roles/dependencies
			roles := misc.RemoveDuplicates(append(rendered.Roles, release.Spec.Roles...))
			dependencies := misc.RemoveDuplicates(append(rendered.Dependencies, release.Spec.Dependencies...))
			cmn.Dump(output, "roles.yaml", roles)
			cmn.Dump(output, "dependencies.yaml", dependencies)

			cmn.Dump(output, "ociRepository.yaml", ociRepository)

			// -------------------------------------------------------------------------Generate Usage
			cmn.Dump(output, "usage.txt", rendered.Usage)

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

			// -------------------------------------------------------------------- Check conditions
			if !renderParams.skipConditions {
				for idx, condition := range rendered.Conditions {
					fmt.Printf("Check condition %d\n", idx)
					ok, message, reconcileError := controller.CheckConditionWithLimits(context.Background(), k8sClient, condition,
						renderParams.conditionLimits)
					if reconcileError != nil {
						return fmt.Errorf("condition#%d: error: %w", idx, reconcileError)
					}
					if !ok {
						return fmt.Errorf("condition#%d: : %s", idx, message)
					}
				}
			}

			// ---------------------------------------------------------------------- Generate stuff by module
			err = previewModulesAt(target, chartsDir, release, pkgContainer, rendered, helmRepositoryName, configStore, runHelm,
				os.Stderr)
			if err != nil {
				return err
			}
			// --------------------------------------------------------------------- Generate output connections
			for idx, outputRendered := range rendered.Outputs {
				if outputRendered.Kind == kapi.KindClusterConnection {
					connection := &kapi.ClusterConnection{
						TypeMeta: metav1.TypeMeta{
							APIVersion: kapi.GroupVersion.String(),
							Kind:       string(kapi.KindClusterConnection),
						},
						ObjectMeta: metav1.ObjectMeta{
							Name: controller.BuildClusterConnectionName(release.Name, release.Namespace, outputRendered.Name),
						},
					}
					valuesTxt, err := json.Marshal(outputRendered.Values)
					if err != nil {
						return fmt.Errorf("output[%d]: could not encode the values: %w", idx, err)
					}
					connection.Spec.Values = &v1.JSON{Raw: valuesTxt}
					connection.Spec.Contract = outputRendered.Contract
					connection.Spec.Description = outputRendered.Description
					connection.Spec.Priority = outputRendered.Priority
					connection.Spec.OutputName = outputRendered.Name
					connection.Spec.ParentRelease = &kapi.ParentReleaseRef{
						Name:      release.Name,
						Namespace: release.Namespace,
					}

					cmn.DumpAppend(output, "outputClusterConnections.yaml", connection)
				} else {
					connection := &kapi.Connection{
						TypeMeta: metav1.TypeMeta{
							APIVersion: kapi.GroupVersion.String(),
							Kind:       string(kapi.KindConnection),
						},
						ObjectMeta: metav1.ObjectMeta{
							Namespace: release.Namespace,
							Name:      controller.BuildConnectionName(release.Name, outputRendered.Name),
						},
					}
					valuesTxt, err := json.Marshal(outputRendered.Values)
					if err != nil {
						return fmt.Errorf("output[%d]: could not encode the values: %w", idx, err)
					}
					connection.Spec.Values = &v1.JSON{Raw: valuesTxt}
					connection.Spec.Contract = outputRendered.Contract
					connection.Spec.Description = outputRendered.Description
					connection.Spec.Priority = outputRendered.Priority
					connection.Spec.OutputName = outputRendered.Name

					cmn.DumpAppend(output, "outputConnections.yaml", connection)
				}
			}

			// ---------------------------------------------------------------------- display relevant context
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

// helmCommand runs helm with args, and returns its standard output and its
// standard error.
type helmCommand func(args ...string) (stdout []byte, stderr []byte, err error)

// runHelm runs the helm found on the PATH.
func runHelm(args ...string) ([]byte, []byte, error) {
	var stdout, stderr bytes.Buffer
	command := exec.Command("helm", args...)
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// fluxReleaseName returns the name under which helm-controller installs
// helmRelease. Past 53 bytes, helm-controller keeps the first 40, then adds a
// dash and the first 12 hexadecimal digits of the SHA-256 of the name.
func fluxReleaseName(helmRelease *fluxv2.HelmRelease) string {
	name := helmRelease.GetReleaseName()
	if len(name) <= 53 {
		return name
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
	return name[:40] + "-" + sum[:12]
}

// previewModules writes, for each enabled module of the package, in
// <output>/modules/<module>: the HelmRelease that the controller builds, the
// values of its spec, and the manifests that helm renders from the chart with
// these values, in the namespace and under the release name where Flux
// installs that HelmRelease. When helm succeeds, what it writes on its
// standard error goes to diagnostics. When it fails, both of its outputs are
// printed with the command.
func previewModules(output string, chartsDir string, release *kapi.Release, pkgContainer *kubopackage.PckContainer,
	rendered *kubopackage.Rendered, helmRepositoryName string, configStore configstore.ConfigStore,
	helm helmCommand, diagnostics io.Writer) error {
	target, err := misc.OpenRootedDir(output)
	if err != nil {
		return err
	}
	defer func() { _ = target.Root.Close() }()
	relative, err := target.Relative(chartsDir)
	if err != nil {
		return err
	}
	charts, err := target.Child(relative)
	if err != nil {
		return err
	}
	return previewModulesAt(target, charts, release, pkgContainer, rendered, helmRepositoryName, configStore,
		helm, diagnostics)
}

func previewModulesAt(output, chartsDir misc.RootedDir, release *kapi.Release, pkgContainer *kubopackage.PckContainer,
	rendered *kubopackage.Rendered, helmRepositoryName string, configStore configstore.ConfigStore,
	helm helmCommand, diagnostics io.Writer) error {
	modules, err := output.Child("modules")
	if err != nil {
		return err
	}
	helmReleaseNameByModuleName := make(map[string]string)
	for _, module := range pkgContainer.Package.Modules {
		helmReleaseNameByModuleName[module.Name] = controller.BuildHelmReleaseName(release.Name, module.Name)
	}
	for _, module := range pkgContainer.Package.Modules {
		if !rendered.ModuleRenderedByName[module.Name].Enabled {
			continue
		}
		destination, err := misc.PrepareDir(modules, module.Name)
		if err != nil {
			return fmt.Errorf("could not ensure empty manifests folder: %w", err)
		}
		out := destination.Path()
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
		err = controller.PopulateHelmRelease(helmRelease, release, pkgContainer, rendered, helmRepositoryName, module,
			helmReleaseNameByModuleName, configStore)
		if err != nil {
			return fmt.Errorf("could not build helm release for module %s: %w", module.Name, err)
		}
		cmn.Dump(out, "helmRelease.yaml", helmRelease)
		values := helmRelease.GetValues()
		if values == nil {
			values = map[string]interface{}{}
		}
		cmn.Dump(out, "values.yaml", values)

		chartDir, err := chartsDir.Child(module.Name)
		if err != nil {
			return err
		}
		if err := validatePreviewChart(chartDir); err != nil {
			return err
		}
		chartFolder, err := digFolderAt(chartDir, "Chart.yaml")
		if err != nil {
			return fmt.Errorf("could not determine chart folder: %w", err)
		}
		args := []string{"template", "--debug", "-n", helmRelease.GetReleaseNamespace(), fluxReleaseName(helmRelease),
			chartFolder, "--values", path.Join(out, "values.yaml")}
		manifests, messages, err := helm(args...)
		if err != nil {
			fmt.Printf("\nexec: helm %s\n", strings.Join(args, " "))
			fmt.Printf("%s\n%s\n", string(manifests), string(messages))
			return fmt.Errorf("failed to generate manifest: %w", err)
		}
		_, _ = diagnostics.Write(messages)
		cmn.DumpTxt(out, "manifests.yaml", string(manifests))
	}
	return nil
}

func DigFolderForFile(inFolder string, lookedUpFile string) (string, error) {
	root, err := misc.OpenRootedDir(inFolder)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Root.Close() }()
	return digFolderAt(root, lookedUpFile)
}

func validatePreviewChart(folder misc.RootedDir) error {
	entries, err := folder.ReadDir(".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			child, err := folder.Child(entry.Name())
			if err != nil {
				return err
			}
			if err := validatePreviewChart(child); err != nil {
				return err
			}
		} else if !entry.Type().IsRegular() {
			return fmt.Errorf("path %q is not a regular file", filepath.Join(folder.Rel, entry.Name()))
		}
	}
	return nil
}

func digFolderAt(folder misc.RootedDir, lookedUpFile string) (string, error) {
	entries, err := folder.ReadDir(".")
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path %q is not a regular file", entry.Name())
		}
		if !entry.IsDir() && entry.Name() == lookedUpFile {
			f, err := folder.OpenRegular(entry.Name())
			if err != nil {
				return "", err
			}
			if err := f.Close(); err != nil {
				return "", err
			}
			return folder.Path(), nil
		}
		if entry.IsDir() {
			child, err := folder.Child(entry.Name())
			if err != nil {
				return "", err
			}
			result, err := digFolderAt(child, lookedUpFile)
			if err != nil || result != "" {
				return result, err
			}
		}
	}
	return "", nil
}

type buildInputModelHelper struct {
	client.Client
}

var _ controller.BuildInputModelHelper = &buildInputModelHelper{}

func (h *buildInputModelHelper) FindConnectionsFromContract(ctx context.Context, namespace string, contract string) ([]kapi.Connection, controller.ReconcileError) {
	// No field indexer is configured for the render command, so we list all connections
	// in the namespace and filter on the contract
	connections := kapi.ConnectionList{}
	err := h.List(ctx, &connections, &client.ListOptions{Namespace: namespace})
	if err != nil {
		return nil, controller.NewReconcileError(fmt.Errorf("FindConnectionsFromContract(): unable to list connections: %w", err), false, "")
	}
	result := make([]kapi.Connection, 0, len(connections.Items))
	for _, connection := range connections.Items {
		if connection.Spec.Contract == contract {
			result = append(result, connection)
		}
	}
	return result, nil
}

func (h *buildInputModelHelper) FindClusterConnectionsFromContract(ctx context.Context, contract string) ([]kapi.ClusterConnection, controller.ReconcileError) {
	// No field indexer is configured for the render command, so we list all clusterConnections
	// filter on the contract
	clusterConnections := kapi.ClusterConnectionList{}
	err := h.List(ctx, &clusterConnections, &client.ListOptions{})
	if err != nil {
		return nil, controller.NewReconcileError(fmt.Errorf("FindClusterConnectionsFromContract(): unable to list clusterConnections: %w", err), false, "")
	}
	result := make([]kapi.ClusterConnection, 0, len(clusterConnections.Items))
	for _, connection := range clusterConnections.Items {
		if connection.Spec.Contract == contract {
			result = append(result, connection)
		}
	}
	return result, nil
}

func (h *buildInputModelHelper) FindOutputConnectionsFromRelease(ctx context.Context, release types.NamespacedName) ([]kapi.Connection, controller.ReconcileError) {
	// No field indexer is configured for the render command, so we list all connections
	// in the release namespace and filter on the controller owner reference name.
	connections := kapi.ConnectionList{}
	err := h.List(ctx, &connections, &client.ListOptions{Namespace: release.Namespace})
	if err != nil {
		return nil, controller.NewReconcileError(fmt.Errorf("FindOutputConnectionFromRelease(): unable to list connections: %w", err), false, "")
	}
	result := make([]kapi.Connection, 0, len(connections.Items))
	for _, connection := range connections.Items {
		owner := metav1.GetControllerOf(&connection)
		if owner != nil && owner.Name == release.Name {
			result = append(result, connection)
		}
	}
	return result, nil
}

func (h *buildInputModelHelper) FindOutputClusterConnectionsFromRelease(ctx context.Context, release types.NamespacedName) ([]kapi.ClusterConnection, controller.ReconcileError) {
	// No field indexer is configured for the render command, so we list all clusterConnections
	// in the release namespace and filter on the controller owner reference name.
	clusterConnections := kapi.ClusterConnectionList{}
	err := h.List(ctx, &clusterConnections, &client.ListOptions{})
	if err != nil {
		return nil, controller.NewReconcileError(fmt.Errorf("FindOutputClusterConnectionsFromRelease(): unable to list clusterConnections: %w", err), false, "")
	}
	result := make([]kapi.ClusterConnection, 0, len(clusterConnections.Items))
	for _, connection := range clusterConnections.Items {
		if connection.Spec.ParentRelease != nil && connection.Spec.ParentRelease.Name == release.Name && connection.Spec.ParentRelease.Namespace == release.Namespace {
			result = append(result, connection)
		}
	}
	return result, nil
}

func fetchRenderCharts(work, target misc.RootedDir, container *kubopackage.PckContainer, packageFolder string) error {
	assembly, err := misc.PrepareDir(work, "assembly")
	if err != nil {
		return fmt.Errorf("error ensuring assembly path: %w", err)
	}
	_, container.Status, err = cmn.FetchArchivesAt("", container.Package, assembly, work, packageFolder,
		renderParams.sourceOptions)
	if err != nil {
		return fmt.Errorf("could not fetch package archive: %w", err)
	}
	return cmn.ExtractPackageCharts(assembly, target, container.Status, renderParams.sourceOptions.ArchiveLimits)
}

func readRenderPackageMetadata(work misc.RootedDir, archiveName string,
	original *kubopackage.Package, status *kubopackage.Status) error {
	if err := tgz.UnmarshalDataAt(work, archiveName, "original.yaml", original,
		renderParams.sourceOptions.ArchiveLimits); err != nil {
		return fmt.Errorf("error unmarshalling OCI content (original.yaml): %w", err)
	}
	renderLog.V(1).Info("Unmarshalled original.yaml")
	if err := tgz.UnmarshalDataAt(work, archiveName, "status.yaml", status,
		renderParams.sourceOptions.ArchiveLimits); err != nil {
		return fmt.Errorf("error unmarshalling OCI content (status.yaml): %w", err)
	}
	renderLog.V(1).Info("Unmarshalled status.yaml")
	return nil
}

func extractRenderPackage(work misc.RootedDir, archiveName string,
	target misc.RootedDir, status *kubopackage.Status) error {
	manifest, err := misc.PrepareDir(work, "manifest.tar")
	if err != nil {
		return err
	}
	if err := tgz.ExtractAllAt(work, archiveName, manifest, renderParams.sourceOptions.ArchiveLimits); err != nil {
		return err
	}
	return cmn.ExtractPackageCharts(manifest, target, status, renderParams.sourceOptions.ArchiveLimits)
}
