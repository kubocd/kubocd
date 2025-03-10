package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	"kubocd/cmd/kubocd/cmd/helmrepo"
	"kubocd/cmd/kubocd/cmd/oci"
	"os"
	"strings"
)

func init() {
	dumpCmd.AddCommand(dumpOciCmd)
	dumpCmd.AddCommand(dumpHrCmd)
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
}

func init() {
	dumpOciCmd.PersistentFlags().BoolVarP(&ociParams.insecure, "insecure", "i", false, "insecure (use HTTP, not HTTPS)")
	dumpOciCmd.PersistentFlags().BoolVarP(&ociParams.anonymous, "anonymous", "a", false, "Connect anonymously. To check 'public' image status")

}

var dumpOciCmd = &cobra.Command{
	Use:   "oci repo:version",
	Short: "Dump OCI metadata",
	Args:  cobra.ExactArgs(1),
	Run: func(command *cobra.Command, args []string) {
		var imageName = args[0]
		if strings.HasPrefix(imageName, "oci://") {
			imageName = imageName[7:]
		}

		var imageRepo string
		var imageTag string

		err := func() error {
			a := strings.Split(imageName, ":")
			if len(a) == 2 {
				imageRepo = a[0]
				imageTag = a[1]
			} else if len(a) == 1 {
				imageRepo = a[0]
				imageTag = "latest"
			} else {
				return fmt.Errorf("invalid image name: %s", imageName)
			}
			//fmt.Printf("OCI dump of %s:%s\n", imageRepo, imageTag)
			op := &oci.Operation{
				WorkDir:   dumpParams.workDir,
				ImageRepo: imageRepo,
				ImageTag:  imageTag,
				Insecure:  ociParams.insecure,
				Anonymous: ociParams.anonymous,
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
var dumpHrCmd = &cobra.Command{
	Use:     "helmRepository repoUrl [chartName [version]]",
	Short:   "Dump helm chart",
	Args:    cobra.RangeArgs(1, 3),
	Aliases: []string{"hr", "HelmRepository", "helmrepository", "helmRepo", "HelmRepo", "helmrepo"},
	Run: func(command *cobra.Command, args []string) {
		err := func() error {
			op := &helmrepo.Operation{
				WorkDir: dumpParams.workDir,
				RepoUrl: args[0],
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
