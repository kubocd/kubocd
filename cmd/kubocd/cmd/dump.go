package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	"kubocd/cmd/kubocd/cmd/app"
	"kubocd/cmd/kubocd/cmd/helmrepo"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/internal/misc"
	"os"
)

func init() {
	dumpCmd.AddCommand(dumpOciCmd)
	dumpCmd.AddCommand(dumpHrCmd)
	dumpCmd.AddCommand(dumpAppCmd)
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
