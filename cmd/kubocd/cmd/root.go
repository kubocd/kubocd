package cmd

import (
	"fmt"
	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	"os"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	rootCmd.AddCommand(controllerCmd)
	rootCmd.AddCommand(webhookCmd)
	rootCmd.AddCommand(renderCmd)
	rootCmd.AddCommand(dumpCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(packageCmd)

	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(kubocdv1alpha1.AddToScheme(scheme))
	utilruntime.Must(sourcev1b2.AddToScheme(scheme))
	utilruntime.Must(sourcev1.AddToScheme(scheme))
	utilruntime.Must(fluxv2.AddToScheme(scheme))

}

var rootCmd = &cobra.Command{
	Use:   "kubocd",
	Short: "KuboCD Application deployment system",
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
