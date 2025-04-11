package cmd

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/cmd/kubocd/cmd/app"
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/cmd/kubocd/cmd/helmrepo"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/internal/configstore"
	"kubocd/internal/controller"
	"kubocd/internal/k8sapi"
	"kubocd/internal/misc"
	"os"
	"strings"
)

func init() {
	dumpCmd.AddCommand(dumpOciCmd)
	dumpCmd.AddCommand(dumpHrCmd)
	dumpCmd.AddCommand(dumpAppCmd)
	dumpCmd.AddCommand(dumpContextCmd)
}

var dumpParams struct {
	workDir string
}

func init() {
	dumpCmd.PersistentFlags().StringVarP(&dumpParams.workDir, "workDir", "w", "", "working directory. Default to $HOME/.kubocd")

}

var dumpCmd = &cobra.Command{
	Use:   "dump",
	Args:  cobra.NoArgs,
	Short: "Dump resources",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// ------------------------------------------- Setup working folder
		if dumpParams.workDir == "" {
			dir, err := os.UserHomeDir()
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "Unable to determine home directory: %v\n", err)
				os.Exit(1)
			}
			dumpParams.workDir = fmt.Sprintf("%s/.kubocd", dir)
		}
	},
}

// ---------------------------------------------------------------------------- dumpOci

var ociParams struct {
	insecure  bool
	anonymous bool
	chart     bool
	output    string
}

func init() {
	dumpOciCmd.PersistentFlags().BoolVarP(&ociParams.insecure, "insecure", "i", false, "insecure (use HTTP, not HTTPS)")
	dumpOciCmd.PersistentFlags().BoolVarP(&ociParams.anonymous, "anonymous", "a", false, "Connect anonymously. To check 'public' image status")
	dumpOciCmd.PersistentFlags().BoolVarP(&ociParams.chart, "chart", "c", false, "unpack charts in output directory")
	dumpOciCmd.PersistentFlags().StringVarP(&ociParams.output, "output", "o", "./.charts", "Output chart directory")

}

var dumpOciCmd = &cobra.Command{
	Use:   "oci repo:version",
	Short: "Dump OCI metadata",
	Args:  cobra.ExactArgs(1),
	Run: func(command *cobra.Command, args []string) {

		err := func() error {
			imageRepo, imageTag, err := misc.DecodeImageUrl(args[0])
			if err != nil {
				return err
			}
			op := &oci.Operation{
				WorkDir:   dumpParams.workDir,
				ImageRepo: imageRepo,
				ImageTag:  imageTag,
				Insecure:  ociParams.insecure,
				Anonymous: ociParams.anonymous,
				Chart:     ociParams.chart,
				Output:    ociParams.output,
			}
			return oci.DumpOci(op)
		}()
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
			os.Exit(1)
		}
	},
}

// --------------------------------------------------------------------------- dump helmRepo

var dumpHrParams struct {
	output string
	chart  bool
}

func init() {
	dumpHrCmd.PersistentFlags().BoolVarP(&dumpHrParams.chart, "chart", "c", false, "unpack charts in output directory")
	dumpHrCmd.PersistentFlags().StringVarP(&dumpHrParams.output, "output", "o", "./.charts", "Output chart directory")
}

var dumpHrCmd = &cobra.Command{
	Use:     "helmRepository repoUrl [chartName [version]]",
	Short:   "Dump helm chart",
	Args:    cobra.RangeArgs(1, 3),
	Aliases: []string{"hr", "HelmRepository", "helmrepository", "helmRepo", "HelmRepo", "helmrepo"},
	Run: func(command *cobra.Command, args []string) {
		err := func() error {
			if dumpHrParams.chart && dumpHrParams.output == "" {
				return fmt.Errorf("--output is required when --charts is specified")
			}
			op := &helmrepo.Operation{
				WorkDir: dumpParams.workDir,
				RepoUrl: args[0],
				Output:  dumpHrParams.output,
				Chart:   dumpHrParams.chart,
			}
			if len(args) > 1 {
				op.ChartName = args[1]
			}
			if len(args) > 2 {
				op.ChartVersion = args[2]
			}
			return helmrepo.DumpHelmRepo(op)
		}()
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
			os.Exit(1)
		}
	},
}

// --------------------------------------------------------------------------------- dump application

var dumpAppParams struct {
	output    string
	insecure  bool
	anonymous bool
	charts    bool
}

func init() {
	dumpAppCmd.PersistentFlags().BoolVarP(&dumpAppParams.insecure, "insecure", "i", false, "insecure (use HTTP, not HTTPS)")
	dumpAppCmd.PersistentFlags().BoolVarP(&dumpAppParams.anonymous, "anonymous", "a", false, "Connect anonymously. To check 'public' image status")
	dumpAppCmd.PersistentFlags().BoolVarP(&dumpAppParams.charts, "charts", "c", false, "unpack charts in output directory")
	dumpAppCmd.PersistentFlags().StringVarP(&dumpAppParams.output, "output", "o", "./.dump", "Output dump directory")
}

var dumpAppCmd = &cobra.Command{
	Use:     "application <application.yaml|oci://repo:version>",
	Short:   "Dump KuboCD Application",
	Args:    cobra.ExactArgs(1),
	Aliases: []string{"app", "Application", "App"},
	Run: func(command *cobra.Command, args []string) {
		err := func() error {
			output := dumpAppParams.output
			if dumpAppParams.charts && dumpAppParams.output == "" {
				return fmt.Errorf("--output is required when --charts is specified")
			}
			return app.Dump(args[0], dumpParams.workDir, dumpAppParams.insecure, dumpAppParams.anonymous, dumpAppParams.charts, output)
		}()
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
			os.Exit(1)
		}

	},
}

// --------------------------------------------------------------------------------- dump context

var dumpContextParams struct {
	contexts           []string
	skipDefaultContext bool
	namespace          string
	kubocdNamespace    string
}

func init() {
	dumpContextCmd.PersistentFlags().BoolVarP(&dumpContextParams.skipDefaultContext, "skipDefaultContext", "", false, "Don't use default context")
	dumpContextCmd.PersistentFlags().StringVarP(&dumpContextParams.namespace, "namespace", "n", "default", "namespace")
	dumpContextCmd.PersistentFlags().StringArrayVarP(&dumpContextParams.contexts, "context", "c", []string{}, "context as 'namespace:name'")
	dumpContextCmd.PersistentFlags().StringVarP(&dumpContextParams.kubocdNamespace, "kubocdNamespace", "", "kubocd", "The namespace where the kubocd controller is installed in (To fetch configs resources)")
}

var dumpContextCmd = &cobra.Command{
	Use:     "context",
	Short:   "Dump KuboCD context",
	Args:    cobra.NoArgs,
	Aliases: []string{"ctx", "Context", "Ctx"},
	Run: func(command *cobra.Command, args []string) {
		err := func() error {
			k8sClient, err := k8sapi.GetKubeClient(scheme)
			if err != nil {
				return fmt.Errorf("error getting kubernetes client: %w", err)
			}
			ctx := context.Background()
			// ------------------------------------------------------------------------ handle config
			configStore := configstore.New()
			err = configStore.Init(ctx, k8sClient, dumpContextParams.kubocdNamespace)
			if err != nil {
				return fmt.Errorf("could not fetch config(s): %w", err)
			}
			// ------------------------------------------------------------- Setup context list
			contexts := make([]kv1alpha1.NamespacedName, len(dumpContextParams.contexts))
			for i, context := range dumpContextParams.contexts {
				x := strings.Split(context, ":")
				if len(x) != 2 {
					return fmt.Errorf("invalid context: %s", context)
				}
				contexts[i] = kv1alpha1.NamespacedName{
					Namespace: x[0],
					Name:      x[1],
				}
			}
			// ------------------------------------------------------------Setup a fake release to comply t
			release := &kv1alpha1.Release{
				Spec: kv1alpha1.ReleaseSpec{
					Contexts:           contexts,
					SkipDefaultContext: dumpContextParams.skipDefaultContext,
				},
			}
			release.SetNamespace(dumpContextParams.namespace)
			// --------------------------------------------------------------- compute context
			context, _, err := controller.ComputeContext(ctx, k8sClient, release, configStore, nil)
			if err != nil {
				return fmt.Errorf("could not compute context: %w", err)
			}
			cmn.Dump("", "", context)
			return nil
		}()
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			os.Exit(1)
		}
	},
}
