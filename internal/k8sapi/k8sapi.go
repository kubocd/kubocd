package k8sapi

import (
	"fmt"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"path/filepath"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//
//var (
//	Scheme = runtime.NewScheme()
//)
//
//func init() {
//	utilruntime.Must(clientgoscheme.AddToScheme(Scheme))
//}

func GetRestConfig() (*rest.Config, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		// fallback to kubeconfig
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("unable to locate home directory: %w", err)
		}
		kubeconfig := filepath.Join(home, ".kube", "config")
		if envVar := os.Getenv("KUBECONFIG"); len(envVar) > 0 {
			kubeconfig = envVar
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("unable to build kubernetes config: %w", err)
		}
	}
	return config, nil
}

func GetKubeClientFromConfig(config *rest.Config, scheme *runtime.Scheme) (client.Client, error) {
	kubeClient, err := client.New(config, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to build kubernetes client: %w", err)
	}
	return kubeClient, nil
}

func GetKubeClient(scheme *runtime.Scheme) (client.Client, error) {
	config, err := GetRestConfig()
	if err != nil {
		return nil, err
	}
	return GetKubeClientFromConfig(config, scheme)
}
