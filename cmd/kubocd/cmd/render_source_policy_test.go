//nolint:goconst // Keep command fixtures and expected diagnostics together.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRenderSourcePolicyProcess(t *testing.T) {
	encoded := os.Getenv("KUBOCD_RENDER_SOURCE_TEST_ARGS")
	if encoded == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(encoded), &args); err != nil {
		t.Fatal(err)
	}
	rootCmd.SetArgs(args)
	Execute()
}

func TestRenderSourcePolicy(t *testing.T) {
	for _, tc := range []struct {
		name             string
		releaseInsecure  bool
		redirect         bool
		redirectInsecure bool
		allow            bool
		wantRequests     bool
		wantDiagnostic   string
	}{
		{name: "direct insecure refused", releaseInsecure: true,
			wantDiagnostic: "could not populate OCI repository: " +
				"Release.spec.package.insecure is not allowed without a matching packageRedirect"},
		{name: "direct insecure allowed", releaseInsecure: true, allow: true, wantRequests: true},
		{name: "redirect requires HTTPS", releaseInsecure: true, redirect: true},
		{name: "redirect permits HTTP", redirect: true, redirectInsecure: true, wantRequests: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requestsMu sync.Mutex
			var requestPaths []string
			registry := newRenderSourceServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestsMu.Lock()
				requestPaths = append(requestPaths, r.URL.Path)
				requestsMu.Unlock()
				http.NotFound(w, r)
			}))
			defer registry.Close()
			repo := strings.TrimPrefix(registry.URL, "http://") + "/demo"
			configs := `{"kind":"ConfigList","apiVersion":"kubocd.kubotal.io/v1alpha1","items":[]}`
			if tc.redirect {
				configs = fmt.Sprintf(`{"kind":"ConfigList","apiVersion":"kubocd.kubotal.io/v1alpha1","items":[{
					"apiVersion":"kubocd.kubotal.io/v1alpha1","kind":"Config",
					"metadata":{"name":"sources","namespace":"kubocd"},
					"spec":{"packageRedirects":[{"oldPrefix":%q,"newPrefix":%q,"insecure":%t}]}
				}]}`, repo+":0.1.0", strings.TrimPrefix(registry.URL, "http://")+"/mirror:2.0.0", tc.redirectInsecure)
			}
			var configLists atomic.Int64
			api := newRenderSourceServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/apis/kubocd.kubotal.io/v1alpha1/namespaces/kubocd/configs" {
					configLists.Add(1)
					if _, err := io.WriteString(w, configs); err != nil {
						t.Error(err)
					}
					return
				}
				response, err := (sourceConfigTransport{}).RoundTrip(r)
				if err != nil {
					t.Error(err)
					http.NotFound(w, r)
					return
				}
				defer func() { _ = response.Body.Close() }()
				if _, err := io.Copy(w, response.Body); err != nil {
					t.Error(err)
				}
			}))
			defer api.Close()
			base := t.TempDir()
			write := func(name, data string) string {
				t.Helper()
				p := filepath.Join(base, name)
				if err := os.WriteFile(p, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				return p
			}
			release := write("release.yaml", fmt.Sprintf(`apiVersion: kubocd.kubotal.io/v1alpha1
kind: Release
metadata:
  name: demo
  namespace: default
spec:
  package:
    repository: %q
    tag: 0.1.0
    insecure: %t
`, repo, tc.releaseInsecure))
			config := write("kubeconfig", fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: fixture
  cluster:
    server: %s
contexts:
- name: fixture
  context:
    cluster: fixture
    user: fixture
current-context: fixture
users:
- name: fixture
  user: {}
`, api.URL))
			args := []string{"render", release, "--output=" + filepath.Join(base, "out"),
				"--workDir=" + filepath.Join(base, "work")}
			if tc.allow {
				args = append(args, "--allowReleaseInsecureSource")
			}
			output, err := runRenderSourcePolicyCommand(t, args, config)
			if configLists.Load() == 0 {
				t.Fatalf("render did not read Configs: %v\n%s", err, output)
			}
			requestsMu.Lock()
			paths := slices.Clone(requestPaths)
			requestsMu.Unlock()
			count := len(paths)
			if (count > 0) != tc.wantRequests {
				t.Errorf("registry handled %d requests, want requests=%t; render: %v\n%s", count, tc.wantRequests, err, output)
			}
			if tc.wantRequests {
				want := "/v2/demo/manifests/0.1.0"
				if tc.redirect {
					want = "/v2/mirror/manifests/2.0.0"
					for _, path := range paths {
						if strings.HasPrefix(path, "/v2/demo/") || strings.HasSuffix(path, "/manifests/0.1.0") {
							t.Errorf("redirect used Release repository or tag: %s", path)
						}
					}
				}
				if !slices.Contains(paths, want) {
					t.Errorf("want manifest request %q, got %v", want, paths)
				}
			}
			if !tc.wantRequests && err == nil {
				t.Errorf("render succeeded unexpectedly:\n%s", output)
			}
			if tc.redirect && !tc.redirectInsecure {
				wantURL := "https://" + strings.TrimPrefix(registry.URL, "http://") + "/v2/mirror/manifests/2.0.0"
				httpsFetchError := false
				for _, line := range strings.Split(output, "\n") {
					if strings.Contains(line, "error getting OCI content:") && strings.Contains(line, wantURL) {
						httpsFetchError = true
					}
				}
				if !httpsFetchError {
					t.Errorf("want HTTPS fetch error for %q, got:\n%s", wantURL, output)
				}
			}
			if tc.wantDiagnostic != "" && !strings.Contains(output, tc.wantDiagnostic) {
				t.Errorf("want %q, got:\n%s", tc.wantDiagnostic, output)
			}
		})
	}
}

func newRenderSourceServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	return server
}

func runRenderSourcePolicyCommand(t *testing.T, args []string, kubeconfig string) (string, error) {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRenderSourcePolicyProcess$")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "KCD_OCI_") {
			child.Env = append(child.Env, entry)
		}
	}
	child.Env = append(child.Env, "KUBOCD_RENDER_SOURCE_TEST_ARGS="+string(encoded),
		"KUBECONFIG="+kubeconfig, "KUBERNETES_SERVICE_HOST=", "KCD_DOCKER_CREDENTIAL_HELPER=", "PATH="+t.TempDir())
	output, err := child.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("render did not finish: %v\n%s", ctx.Err(), output)
	}
	return string(output), err
}
