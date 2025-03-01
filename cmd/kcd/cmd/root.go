package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	"kubocd/internal/misc"
	"log/slog"
	"os"
)

var workDir string

var rootParams struct {
	debug        bool
	workDirParam string
}

func init() {
	RootCmd.AddCommand(&versionCmd)
	RootCmd.AddCommand(&packageCmd)
	RootCmd.AddCommand(&dumpCmd)
	RootCmd.AddCommand(&ociCmd)
	RootCmd.AddCommand(&hrCmd)

	RootCmd.PersistentFlags().BoolVarP(&rootParams.debug, "debug", "d", false, "debug mode")
	RootCmd.PersistentFlags().StringVarP(&rootParams.workDirParam, "workDir", "w", "", "working directory. Default to $HOME/.kubocd")
}

var RootCmd = &cobra.Command{
	Use:   "kcd",
	Short: "kcd is a tool for assembling HelmCharts to build Kubernetes KuboCD Applications.",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// ---------------------------------- Handle log
		opts := &slog.HandlerOptions{
			Level: misc.Ternary(rootParams.debug, slog.LevelDebug, slog.LevelInfo),
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, opts)))
		slog.Debug("Debug mode set")

		// ------------------------------------------- Setup working folder

		if rootParams.workDirParam == "" {
			dir, err := os.UserHomeDir()
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "Unable to determine home directory: %v\n", err)
				os.Exit(1)
			}
			workDir = fmt.Sprintf("%s/.kubocd", dir)
		} else {
			workDir = rootParams.workDirParam
		}
	},
}

func Execute() {
	defer func() {
		if !rootParams.debug {
			if r := recover(); r != nil {
				fmt.Printf("ERROR:%v\n", r)
				os.Exit(1)
			}
		}
	}()
	if err := RootCmd.Execute(); err != nil {
		//fmt.Println(err)
		os.Exit(2)
	}
}

// --------------------------------------------------------------------------

func init() {
	dumpCmd.AddCommand(dumpOciCmd)
	dumpCmd.AddCommand(dumpHrCmd)
}

var dumpCmd = cobra.Command{
	Use:   "dump",
	Args:  cobra.NoArgs,
	Short: "Dump resources",
}

func init() {
	ociCmd.AddCommand(ociDumpCmd)
}

var ociCmd = cobra.Command{
	Use:   "oci",
	Args:  cobra.NoArgs,
	Short: "OCI commands",
}

func init() {
	hrCmd.AddCommand(hrDumpCmd)
}

var hrCmd = cobra.Command{
	Use:     "helmRepository",
	Args:    cobra.NoArgs,
	Aliases: []string{"hr", "helmRepo", "helmrepository", "helmrepo"},
	Short:   "HelmRepository commands",
}
