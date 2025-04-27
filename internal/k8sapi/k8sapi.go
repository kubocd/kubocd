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
