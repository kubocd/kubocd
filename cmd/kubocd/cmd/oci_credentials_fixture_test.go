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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"kubocd/internal/global"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote/auth"
)

const (
	ociTestRepo   = "registry.test/demo"
	ociTestUser   = "fixture-user"
	ociTestSecret = "fixture-secret"
	ociTestToken  = "fixture-token"
	ociTLSMessage = "OCI credentials require HTTPS; " +
		"use --allowInsecureCredentials to allow authentication over HTTP"
	ociDowngradeMessage = "OCI HTTPS-to-HTTP redirect with credentials is not allowed; " +
		"use --allowInsecureCredentials to allow authentication over HTTP"
)

type ociRequestRecord struct {
	Method, URL, Auth, UserAgent string
	Userinfo                     bool
}
type ociRegistryFixture struct {
	mu         sync.Mutex
	requests   []ociRequestRecord
	blobs      map[string][]byte
	manifest   []byte
	pushed     []byte
	basic      bool
	realm      string
	redirect   func(*http.Request) string
	respond    func(http.ResponseWriter, *http.Request) bool
	recordFile string
}

func ociArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func newOCIRegistryFixture(t *testing.T) *ociRegistryFixture {
	t.Helper()
	chart := ociArchive(t, map[string][]byte{"demo/Chart.yaml": []byte("apiVersion: v2\nname: demo\nversion: 0.1.0\n"),
		"demo/templates/cm.yaml": []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fixture\n")})
	pkg := []byte(`apiVersion: v1alpha1
name: demo
tag: 0.1.0
modules:
- name: main
  source:
    oci:
      repository: registry.test/demo
      tag: 0.1.0
`)
	archive := ociArchive(t, map[string][]byte{ociTestOriginal: pkg, "groomed.yaml": pkg, "demo-0.1.0.tgz": chart,
		ociTestStatus: []byte("apiVersion: v1alpha1\nchartByModule:\n  main:\n    name: demo\n    version: 0.1.0\n"),
		ociTestIndex:  []byte("apiVersion: v1\nentries: {}\n")})
	f := &ociRegistryFixture{blobs: make(map[string][]byte)}
	descriptor := func(media string, data []byte) v1.Descriptor {
		d := digest.FromBytes(data)
		f.blobs[d.String()] = data
		return v1.Descriptor{MediaType: media, Digest: d, Size: int64(len(data))}
	}
	m := v1.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: v1.MediaTypeImageManifest,
		Config: descriptor(global.PackageConfigMediaType, []byte("{}")),
		Layers: []v1.Descriptor{descriptor(global.PackageContentMediaType,
			archive), descriptor(global.HelmChartMediaType, chart)}}
	var err error
	f.manifest, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *ociRegistryFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() == "fixture.invalid" {
		return (sourceConfigTransport{}).RoundTrip(r)
	}
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	response := w.Result()
	response.Request = r
	return response, nil
}

func (f *ociRegistryFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record := ociRequestRecord{r.Method, r.URL.String(), r.Header.Get("Authorization"),
		r.Header.Get("User-Agent"), r.URL.User != nil}
	f.requests = append(f.requests, record)
	if f.recordFile != "" {
		data, _ := json.Marshal(record)
		file, err := os.OpenFile(f.recordFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		_, err = file.Write(append(data, '\n'))
		if err != nil {
			panic(err)
		}
		if err := file.Close(); err != nil {
			panic(err)
		}
	}
	if f.respond != nil && f.respond(w, r) {
		return
	}
	if f.redirect != nil {
		if target := f.redirect(r); target != "" {
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return
		}
	}
	if r.URL.Path == "/token" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token":"`+ociTestToken+`","expires_in":300}`)
		return
	}
	if f.realm != "" && r.Header.Get("Authorization") != "Bearer "+ociTestToken {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="fixture",scope="repository:demo:pull,push"`,
			f.realm))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.basic {
		user, secret, ok := r.BasicAuth()
		if !ok || user != ociTestUser || secret != ociTestSecret {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	p := r.URL.Path
	switch {
	case strings.Contains(p, "/manifests/"):
		if r.Method == http.MethodPut {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				panic(err)
			}
			f.pushed = data
			w.Header().Set("Docker-Content-Digest", digest.FromBytes(data).String())
			w.WriteHeader(http.StatusCreated)
			return
		}
		// Push probes by digest; reads resolve the tag first and then fetch by digest.
		if strings.Contains(p, "/manifests/sha256:") && !strings.HasSuffix(p, digest.FromBytes(f.manifest).String()) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", v1.MediaTypeImageManifest)
		w.Header().Set("Content-Length", fmt.Sprint(len(f.manifest)))
		w.Header().Set("Docker-Content-Digest", digest.FromBytes(f.manifest).String())
		if r.Method != http.MethodHead {
			_, _ = w.Write(f.manifest)
		}
	case strings.Contains(p, "/blobs/uploads/"):
		if r.Method == http.MethodPut {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				panic(err)
			}
			d := r.URL.Query().Get("digest")
			if digest.FromBytes(data).String() != d {
				http.Error(w, "wrong blob digest", 400)
				return
			}
			f.blobs[d] = data
			w.Header().Set("Docker-Content-Digest", d)
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.Header().Set("Location", "/v2/demo/blobs/uploads/fixture")
		w.WriteHeader(http.StatusAccepted)
	case strings.Contains(p, "/blobs/"):
		d := p[strings.LastIndex(p, "/")+1:]
		data, ok := f.blobs[d]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Header().Set("Docker-Content-Digest", d)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *ociRegistryFixture) snapshot() []ociRequestRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ociRequestRecord(nil), f.requests...)
}
func installOCIFixture(t *testing.T, f *ociRegistryFixture, credentials string) {
	t.Helper()
	previousAuth := auth.DefaultClient
	isolatedAuth := *previousAuth
	isolatedAuth.Cache = auth.NewCache()
	auth.DefaultClient = &isolatedAuth
	t.Cleanup(func() { auth.DefaultClient = previousAuth })
	previous := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = previous })
	for _, key := range []string{"KCD_OCI_USER", "KCD_OCI_SECRET", "KCD_OCI_REGISTRY_TEST_USER",
		"KCD_OCI_REGISTRY_TEST_SECRET"} {
		t.Setenv(key, "")
	}
	helper := filepath.Join(t.TempDir(), "credentials")
	answer := `{}`
	switch credentials {
	case "deprecated":
		t.Setenv("KCD_OCI_USER", ociTestUser)
		t.Setenv("KCD_OCI_SECRET", ociTestSecret)
	case ociTestRegistryMode:
		t.Setenv("KCD_OCI_REGISTRY_TEST_USER", ociTestUser)
		t.Setenv("KCD_OCI_REGISTRY_TEST_SECRET", ociTestSecret)
	case "helper":
		answer = fmt.Sprintf(`{"Username":%q,"Secret":%q}`, ociTestUser, ociTestSecret)
	case "userOnly":
		answer = fmt.Sprintf(`{"Username":%q}`, ociTestUser)
	case "secretOnly":
		answer = fmt.Sprintf(`{"Secret":%q}`, ociTestSecret)
	}
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' '"+answer+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KCD_DOCKER_CREDENTIAL_HELPER", helper)
}
func ociPushAssembly(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range map[string][]byte{"assembly.tgz": ociArchive(t,
		map[string][]byte{"proof": []byte("pushed content")}), "manifest.json": []byte("{}\n")} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
