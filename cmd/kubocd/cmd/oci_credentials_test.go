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
	"bytes"
	"encoding/json"
	"fmt"
	"kubocd/cmd/kubocd/cmd/oci"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	ociTestRealmMode         = "realm"
	ociTestTag               = "0.1.0"
	ociTestHTTPSRealm        = "https://auth.test/token"
	ociTestRealmUserinfo     = "HTTPRealmUserinfo"
	ociTestAnonymousUserinfo = "anonymousUserinfo"

	ociTestOriginal           = "original.yaml"
	ociTestIndex              = "index.yaml"
	ociTestRegistryMode       = "registry"
	ociTestAnonymousDowngrade = "anonymousDowngrade"
	ociTestHost               = "registry.test"

	ociTestPackagePush  = "packagePush"
	ociTestCommand      = "oci"
	ociTestStatus       = "status.yaml"
	ociTestChain        = "chain"
	ociTestCachedBearer = "cachedBearer"
	ociTestSameOrigin   = "sameOrigin"
)

func runOCIOperation(t *testing.T, f *ociRegistryFixture, push, plainHTTP, anonymous bool) error {
	t.Helper()
	if push {
		dir := ociPushAssembly(t)
		err := pushImage(dir, ociTestRepo, ociTestTag, plainHTTP)
		if err == nil {
			assertOCIPushed(t, f, dir)
		}
		return err
	}
	loc, err := oci.FetchOciImage("", &oci.Operation{WorkDir: t.TempDir(),
		ImageRepo: ociTestRepo, ImageTag: ociTestTag, Insecure: plainHTTP,
		Anonymous: anonymous})
	if err == nil {
		assertOCIRead(t, f, loc)
	}
	return err
}
func assertOCIRead(t *testing.T, f *ociRegistryFixture, loc string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for d, want := range f.blobs {
		got, err := os.ReadFile(filepath.Join(loc, "blobs", "sha256", strings.TrimPrefix(d, "sha256:")))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("read content mismatch for %s: %v", d, err)
		}
	}
}
func assertOCIPushed(t *testing.T, f *ociRegistryFixture, dir string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var m v1.Manifest
	if err := json.Unmarshal(f.pushed, &m); err != nil {
		t.Fatalf("final manifest not pushed: %v", err)
	}
	for _, name := range []string{"assembly.tgz", "manifest.json"} {
		want, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(f.blobs[digest.FromBytes(want).String()], want) {
			t.Errorf("pushed blob %s missing or changed", name)
		}
	}
	if len(m.Layers) != 1 {
		t.Errorf("pushed manifest layers: %d", len(m.Layers))
	}
}
func assertOCITLSRefused(t *testing.T, f *ociRegistryFixture, err error) {
	t.Helper()
	if err == nil || err.Error() != ociTLSMessage {
		t.Errorf("missing early TLS refusal: %v", err)
	}
	if records := f.snapshot(); len(records) != 0 {
		t.Errorf("HTTP operation with credentials reached transport: %+v", records)
	}
}
func testOCICredentialTLS(t *testing.T, push bool) {
	for _, credentials := range []string{"deprecated", ociTestRegistryMode, "helper", "secretOnly"} {
		t.Run(credentials, func(t *testing.T) {
			f := newOCIRegistryFixture(t)
			f.basic = true
			installOCIFixture(t, f, credentials)
			assertOCITLSRefused(t, f, runOCIOperation(t, f, push, true, false))
		})
	}
	t.Run("HTTPSBasic", func(t *testing.T) {
		f := newOCIRegistryFixture(t)
		f.basic = true
		installOCIFixture(t, f, ociTestRegistryMode)
		if err := runOCIOperation(t, f, push, false, false); err != nil {
			t.Fatal(err)
		}
		authenticated := 0
		for _, r := range f.snapshot() {
			if r.Auth != "" {
				authenticated++
				if !strings.HasPrefix(r.URL, "https://") {
					t.Errorf("HTTP auth: %+v", r)
				}
			}
		}
		if authenticated < 2 {
			t.Errorf("cached authentication not exercised: %d", authenticated)
		}
	})
	t.Run("anonymousHTTP", func(t *testing.T) {
		f := newOCIRegistryFixture(t)
		installOCIFixture(t, f, "")
		if err := runOCIOperation(t, f, push, true, false); err != nil {
			t.Fatal(err)
		}
		for _, r := range f.snapshot() {
			if r.Auth != "" {
				t.Errorf("anonymous request authenticated: %+v", r)
			}
		}
	})
	if !push {
		t.Run(ociTestExplicitAnonymous, func(t *testing.T) {
			f := newOCIRegistryFixture(t)
			installOCIFixture(t, f, "deprecated")
			if err := runOCIOperation(t, f, false, true, true); err != nil {
				t.Fatal(err)
			}
			for _, r := range f.snapshot() {
				if r.Auth != "" {
					t.Errorf("-a sent credentials: %+v", r)
				}
			}
		})
	}
}
func TestOCIReadCredentialTLS(t *testing.T) { testOCICredentialTLS(t, false) }
func TestOCIPushCredentialTLS(t *testing.T) { testOCICredentialTLS(t, true) }

func testOCITokenServiceTLS(t *testing.T, push bool) {
	for _, tc := range []struct {
		name, realm, creds    string
		http, refused, native bool
	}{
		{"HTTPSRealm", ociTestHTTPSRealm, ociTestRegistryMode, false, false, false},
		{"HTTPRegistryHTTPSRealm", ociTestHTTPSRealm, ociTestRegistryMode, true, true, false},
		{"HTTPRealmNativeRefusal", "http://auth.test/token", ociTestRegistryMode, false, false, true},
		{"anonymousHTTPBearer", "http://auth.test/token", "", true, false, false},
		{"anonymousHTTPHTTPSRealm", ociTestHTTPSRealm, "", true, false, false},
		{ociTestRealmUserinfo, "http://realm-user:realm-secret@auth.test/token", "", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOCIRegistryFixture(t)
			f.realm = tc.realm
			installOCIFixture(t, f, tc.creds)
			err := runOCIOperation(t, f, push, tc.http, false)
			if tc.refused {
				assertOCITLSRefused(t, f, err)
				return
			}
			if tc.native || tc.name == ociTestRealmUserinfo {
				if err == nil {
					t.Error("unsafe realm accepted")
				}
				if tc.name == ociTestRealmUserinfo && (err == nil || !strings.Contains(err.Error(), ociTLSMessage)) {
					t.Errorf("missing userinfo TLS refusal: %v", err)
				}
				for _, r := range f.snapshot() {
					if strings.Contains(r.URL, "/token") {
						t.Errorf("unsafe realm reached: %+v", r)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			realmRequests, bearers := 0, 0
			for _, r := range f.snapshot() {
				if strings.Contains(r.URL, "/token") {
					realmRequests++
					if tc.creds != "" && !strings.HasPrefix(r.Auth, "Basic ") {
						t.Error("HTTPS realm did not receive configured credentials")
					}
				}
				if r.Auth == "Bearer "+ociTestToken {
					bearers++
				}
			}
			if realmRequests != 1 || bearers < 2 {
				t.Errorf("token cache not exercised: realms=%d bearers=%d", realmRequests, bearers)
			}
		})
	}
}
func TestOCITokenServiceTLS(t *testing.T)     { testOCITokenServiceTLS(t, false) }
func TestOCIPushTokenServiceTLS(t *testing.T) { testOCITokenServiceTLS(t, true) }

func TestOCICredentialRedirects(t *testing.T) {
	for _, push := range []bool{false, true} {
		for _, mode := range []string{ociTestRegistryMode, ociTestRealmMode, ociTestChain,
			ociTestCachedBearer, "crossOrigin", ociTestCrossOriginPort, ociTestCrossOriginSubdomain,
			ociTestAnonymousDowngrade, ociTestSameOrigin, ociTestAnonymousUserinfo} {
			t.Run(fmt.Sprintf("push=%t/%s", push, mode), func(t *testing.T) {
				f := newOCIRegistryFixture(t)
				f.basic = true
				installOCIFixture(t, f, ociTestRegistryMode)
				if mode == ociTestRealmMode || mode == ociTestCachedBearer {
					f.basic = false
					f.realm = ociTestHTTPSRealm
				}
				if mode == ociTestAnonymousUserinfo || mode == ociTestAnonymousDowngrade {
					f.basic = false
					installOCIFixture(t, f, "")
				}
				f.redirect = func(r *http.Request) string { return ociRedirectTarget(mode, r) }
				err := runOCIOperation(t, f, push, false, false)
				if mode == ociTestSameOrigin || mode == ociTestAnonymousDowngrade {
					assertOCIControlRedirect(t, f.snapshot(), mode, err)
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if strings.HasPrefix(mode, "crossOrigin") {
					assertOCIControlRedirect(t, f.snapshot(), mode, err)
					return
				}
				if err == nil || !strings.Contains(err.Error(), ociDowngradeMessage) {
					t.Errorf("missing downgrade refusal: %v", err)
				}
				for _, r := range f.snapshot() {
					if strings.HasPrefix(r.URL, "http://") {
						t.Errorf("downgrade reached transport: %+v", r)
					}
				}
			})
		}
	}
	t.Run("redirectLimit", func(t *testing.T) {
		f := newOCIRegistryFixture(t)
		installOCIFixture(t, f, ociTestRegistryMode)
		f.redirect = func(r *http.Request) string {
			if len(r.URL.RawQuery) >= 12 {
				return ""
			}
			return "https://registry.test" + r.URL.Path + "?" + r.URL.RawQuery + "x"
		}
		err := runOCIOperation(t, f, false, false, false)
		if err == nil || !strings.Contains(err.Error(), "OCI redirect limit exceeded") {
			t.Errorf("missing redirect limit: %v", err)
		}
		if len(f.snapshot()) != 10 {
			t.Errorf("redirect limit sent %d requests", len(f.snapshot()))
		}
	})
	t.Run("trustedTLSServer", func(t *testing.T) {
		f := newOCIRegistryFixture(t)
		f.basic = true
		installOCIFixture(t, f, "deprecated")
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewUnstartedServer(f)
		_ = server.Listener.Close()
		server.Listener = listener
		server.StartTLS()
		defer server.Close()
		http.DefaultTransport = server.Client().Transport
		loc, err := oci.FetchOciImage("", &oci.Operation{WorkDir: t.TempDir(),
			ImageRepo: strings.TrimPrefix(server.URL, "https://") + "/demo", ImageTag: ociTestTag})
		if err != nil {
			t.Fatal(err)
		}
		assertOCIRead(t, f, loc)
	})
}

func ociRedirectTarget(mode string, r *http.Request) string {
	if mode == ociTestAnonymousDowngrade && r.URL.Host == ociTestHost &&
		(r.Method == http.MethodGet || r.Method == http.MethodHead) {
		return "http://other.test" + r.URL.Path
	}
	if mode == ociTestAnonymousUserinfo && r.URL.Host == ociTestHost {
		return "http://u:pw@other.test" + r.URL.Path
	}
	if mode == ociTestRealmMode && r.URL.Host == ociTestAuthHost {
		return "http://auth.test:8080/token"
	}
	if mode == ociTestChain && r.URL.Host == "middle.test" {
		return "http://middle.test:8080" + r.URL.Path
	}
	if r.URL.Host != ociTestHost || r.Header.Get("Authorization") == "" {
		return ""
	}
	switch mode {
	case ociTestRegistryMode:
		return "http://registry.test:8080" + r.URL.Path
	case ociTestChain:
		return "https://middle.test" + r.URL.Path
	case ociTestCachedBearer:
		if strings.Contains(r.URL.Path, "/blobs/") {
			return "http://registry.test:8080" + r.URL.Path
		}
	case ociTestCrossOriginPort:
		return "https://registry.test:8443" + r.URL.Path
	case ociTestCrossOriginSubdomain:
		return "https://sub.registry.test" + r.URL.Path
	case "crossOrigin":
		return "https://other.test" + r.URL.Path
	case ociTestSameOrigin:
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Query().Get("redirected") == "" {
			return r.URL.String() + "?redirected=1"
		}
	}
	return ""
}
