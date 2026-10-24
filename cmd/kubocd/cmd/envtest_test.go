/*
Copyright 2026 Kubotal

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
	"context"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// apiServer is an envtest API server that holds the CRDs of KuboCD, and a
// client that writes to it.
type apiServer struct {
	cfg    *rest.Config
	scheme *runtime.Scheme
	client client.Client
}

// startAPIServer starts an API server with the CRDs of KuboCD and the CRDs
// given, and the namespaces given, and stops it at the end of the test. The
// binaries are those of KUBEBUILDER_ASSETS, or of the first folder of bin/k8s.
func startAPIServer(t *testing.T, namespaces []string, crds ...*apiextensionsv1.CustomResourceDefinition) *apiServer {
	t.Helper()
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		CRDs:                  crds,
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if entries, err := os.ReadDir(filepath.Join("..", "..", "..", "bin", "k8s")); err == nil && len(entries) > 0 {
			env.BinaryAssetsDirectory = filepath.Join("..", "..", "..", "bin", "k8s", entries[0].Name())
		}
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Logf("could not stop the API server: %v", err)
		}
	})
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, kubocdv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	for _, namespace := range namespaces {
		object := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		if err := direct.Create(context.Background(), object); err != nil {
			t.Fatal(err)
		}
	}
	return &apiServer{cfg: cfg, scheme: scheme, client: direct}
}

// eventually waits until check holds, and fails the test after timeout.
func eventually(t *testing.T, what string, timeout time.Duration, check func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if check() {
			return
		}
	}
	t.Fatalf("timeout waiting for %s", what)
}
