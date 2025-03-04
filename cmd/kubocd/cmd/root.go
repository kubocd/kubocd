package cmd

import (
	"fmt"
	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/misc"
	"os"
)

var (
	scheme  = runtime.NewScheme()
	rootLog logr.Logger
)

var rootParams struct {
	logConfig misc.LogConfig
}

func init() {
	rootCmd.AddCommand(controllerCmd)
	rootCmd.AddCommand(webhookCmd)
	rootCmd.AddCommand(renderCmd)

	rootCmd.PersistentFlags().StringVar(&rootParams.logConfig.Level, "logLevel", "INFO", "Log level")
	rootCmd.PersistentFlags().StringVar(&rootParams.logConfig.Mode, "logMode", "dev", "Log mode: 'dev' or 'json'")

	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(kubocdv1alpha1.AddToScheme(scheme))
	utilruntime.Must(sourcev1b2.AddToScheme(scheme))
	utilruntime.Must(sourcev1.AddToScheme(scheme))
	utilruntime.Must(fluxv2.AddToScheme(scheme))

}

var rootCmd = &cobra.Command{
	Use:   "kubocd",
	Short: "KuboCD Application deployment system",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		var err error
		rootLog, err = misc.HandleLog(&rootParams.logConfig)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Unable to load logging configuration: %v\n", err)
			os.Exit(2)
		}
	},
}

var debug = true

func Execute() {
	defer func() {
		if !debug {
			if r := recover(); r != nil {
				fmt.Printf("ERROR:%v\n", r)
				os.Exit(1)
			}
		}
	}()
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(2)
	}
}
