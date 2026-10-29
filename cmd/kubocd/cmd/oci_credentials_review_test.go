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
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestOCIUnicodeRealmRedirects(t *testing.T) {
	const (
		tokenPath      = "/token"
		redirectedPath = "/token2"
		terminalPath   = "/token3"
	)
	for _, push := range []bool{false, true} {
		for _, tc := range []struct {
			name, host, target string
			sameOrigin         bool
		}{
			{"relative", "bücher.example", redirectedPath, true},
			{"unicodeFoldAlias", "gİt.example", "https://git.example/token2", false},
		} {
			t.Run(fmt.Sprintf("push=%t/%s", push, tc.name), func(t *testing.T) {
				f := newOCIRegistryFixture(t)
				f.realm = "https://" + tc.host + tokenPath
				installOCIFixture(t, f, ociTestRegistryMode)
				f.respond = func(w http.ResponseWriter, r *http.Request) bool {
					switch r.URL.Path {
					case tokenPath:
						http.Redirect(w, r, tc.target, http.StatusFound)
					case redirectedPath:
						http.Redirect(w, r, terminalPath, http.StatusFound)
					case terminalPath:
						if tc.sameOrigin {
							user, secret, ok := r.BasicAuth()
							if !ok || user != ociTestUser || secret != ociTestSecret {
								http.Error(w, "missing realm credentials", http.StatusUnauthorized)
								return true
							}
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprintf(w, `{"token":%q,"expires_in":300}`, ociTestToken)
					default:
						return false
					}
					return true
				}
				if err := runOCIOperation(t, f, push, false, false); err != nil {
					t.Errorf("redirected token operation failed: %v", err)
				}
				basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(ociTestUser+":"+ociTestSecret))
				counts := map[string]int{}
				for _, r := range f.snapshot() {
					u, err := url.Parse(r.URL)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.HasPrefix(u.Path, tokenPath) {
						continue
					}
					counts[u.Path]++
					wantHost, wantAuth := tc.host, basic
					if u.Path != tokenPath && !tc.sameOrigin {
						wantHost, wantAuth = "git.example", ""
					}
					if u.Scheme != "https" || u.Host != wantHost || r.Auth != wantAuth {
						t.Errorf("realm request = %+v, want HTTPS host %q Authorization %q", r, wantHost, wantAuth)
					}
				}
				for _, path := range []string{tokenPath, redirectedPath, terminalPath} {
					if counts[path] != 1 {
						t.Errorf("realm path %s reached %d times, want 1", path, counts[path])
					}
				}
			})
		}
	}
}

const (
	ociTestTokenService         = "tokenService"
	ociTestBasicKind            = "Basic"
	ociTestAuthHost             = "auth.test"
	ociTestExplicitAnonymous    = "explicitAnonymous"
	ociTestCrossOriginPort      = "crossOriginPort"
	ociTestCrossOriginSubdomain = "crossOriginSubdomain"
	ociTestTrue                 = "true"
)

const ociForeignUserinfoMessage = "OCI redirect to a different origin with URL userinfo is not allowed"

func TestOCIRedirectChainsKeepCredentialsAtInitialOrigin(t *testing.T) {
	for _, push := range []bool{false, true} {
		for _, kind := range []string{ociTestBasicKind, ociTestCachedBearer, ociTestTokenService} {
			for _, destination := range []string{"registry.test:8443", "sub.registry.test"} {
				if kind == ociTestTokenService {
					destination = strings.ReplaceAll(destination, "registry.test", ociTestAuthHost)
				}
				t.Run(fmt.Sprintf("push=%t/%s/%s", push, kind, destination), func(t *testing.T) {
					f := newOCIRegistryFixture(t)
					f.basic = kind == ociTestBasicKind
					if kind != ociTestBasicKind {
						f.realm = ociTestHTTPSRealm
					}
					installOCIFixture(t, f, ociTestRegistryMode)
					configureOCIChain(f, kind, destination, "https")
					err := runOCIOperation(t, f, push, false, false)
					if err == nil || !strings.Contains(err.Error(), "418") {
						t.Errorf("terminal response not reached: %v", err)
					}
					assertOCIChainRecords(t, f.snapshot(), kind, destination)
				})
			}
		}
	}
	t.Run("effectivePort", func(t *testing.T) {
		f := newOCIRegistryFixture(t)
		f.basic = true
		installOCIFixture(t, f, ociTestRegistryMode)
		f.redirect = func(r *http.Request) string {
			if r.URL.Host == ociTestHost && r.Header.Get("Authorization") != "" {
				return "https://REGISTRY.test:443" + r.URL.Path
			}
			return ""
		}
		if err := runOCIOperation(t, f, false, false, false); err != nil {
			t.Fatal(err)
		}
		reached := 0
		for _, r := range f.snapshot() {
			if strings.HasPrefix(r.URL, "https://REGISTRY.test:443/") {
				reached++
				if !strings.HasPrefix(r.Auth, "Basic ") {
					t.Errorf("equivalent origin lost credentials: %+v", r)
				}
			}
		}
		if reached == 0 {
			t.Error("equivalent-origin redirect not exercised")
		}
	})

}

func assertOCIChainRecords(t *testing.T, records []ociRequestRecord, kind, destination string) {
	t.Helper()
	initial, foreign, hop, realm, bearers := 0, 0, 0, 0, 0
	for _, r := range records {
		u, err := url.Parse(r.URL)
		if err != nil {
			t.Fatal(err)
		}
		if u.Host == destination {
			if strings.HasPrefix(u.Path, "/hop/") {
				hop++
			} else {
				foreign++
			}
			if r.Auth != "" {
				t.Errorf("foreign chain received Authorization: %+v", r)
			}
		}
		if u.Host == ociTestHost && strings.HasPrefix(r.Auth, "Basic ") && kind == ociTestBasicKind {
			initial++
		}
		if u.Host == ociTestHost && r.Auth == "Bearer "+ociTestToken {
			bearers++
			if strings.Contains(u.Path, "/blobs/") {
				initial++
			}
		}
		if u.Host == ociTestAuthHost {
			realm++
			if kind == ociTestTokenService && strings.HasPrefix(r.Auth, "Basic ") {
				initial++
			}
		}
	}
	if initial == 0 || foreign == 0 || hop == 0 || foreign != hop {
		t.Errorf("chain not exercised: initial=%d foreign=%d hop=%d", initial, foreign, hop)
	}
	if kind == ociTestCachedBearer && (realm != 1 || bearers < 2) {
		t.Errorf("Bearer cache not exercised: realm=%d requests=%d", realm, bearers)
	}
}

func TestOCIUsernameOnlyHelperIsAnonymous(t *testing.T) {
	for _, push := range []bool{false, true} {
		for _, plain := range []bool{false, true} {
			t.Run(fmt.Sprintf("push=%t/http=%t", push, plain), func(t *testing.T) {
				f := newOCIRegistryFixture(t)
				f.realm = ociTestHTTPSRealm
				installOCIFixture(t, f, "userOnly")
				if err := runOCIOperation(t, f, push, plain, false); err != nil {
					t.Fatalf("username-only anonymous operation failed: %v", err)
				}
				realms, bearers := 0, 0
				for _, r := range f.snapshot() {
					if strings.Contains(r.URL, "/token") {
						realms++
						if r.Auth != "" {
							t.Errorf("username-only helper authenticated to realm: %+v", r)
						}
					}
					if r.Auth == "Bearer "+ociTestToken {
						bearers++
					}
				}
				if realms != 1 || bearers < 2 {
					t.Errorf("anonymous token flow not exercised: realms=%d bearers=%d", realms, bearers)
				}
			})
		}
	}
}

func TestOCIClientUserAgentCompatibility(t *testing.T) {
	for _, push := range []bool{false, true} {
		for _, mode := range []string{"anonymous", ociTestExplicitAnonymous, "configured"} {
			if push && mode == ociTestExplicitAnonymous {
				continue
			}
			t.Run(fmt.Sprintf("push=%t/%s", push, mode), func(t *testing.T) {
				f := newOCIRegistryFixture(t)
				creds := ""
				want := "oras-go"
				if mode != "anonymous" {
					creds = ociTestRegistryMode
				}
				if mode == "configured" {
					f.basic = true
					want = ""
				}
				installOCIFixture(t, f, creds)
				if err := runOCIOperation(t, f, push, false, mode == ociTestExplicitAnonymous); err != nil {
					t.Fatal(err)
				}
				records := f.snapshot()
				if len(records) == 0 {
					t.Fatal("no registry requests")
				}
				for _, r := range records {
					if r.UserAgent != want {
						t.Errorf("User-Agent = %q, want %q for %s", r.UserAgent, want, r.URL)
					}
				}
			})
		}
	}
}

func TestOCIForeignRedirectUserinfoIsRefused(t *testing.T) {
	f := newOCIRegistryFixture(t)
	installOCIFixture(t, f, ociTestRegistryMode)
	f.redirect = func(r *http.Request) string {
		if r.URL.Host == ociTestHost {
			return "https://u:server-secret@registry.test:8443" + r.URL.Path
		}
		return ""
	}
	err := runOCIOperation(t, f, false, false, false)
	if err == nil || !strings.Contains(err.Error(), ociForeignUserinfoMessage) {
		t.Errorf("foreign redirect userinfo not refused: %v", err)
	}
	if len(f.snapshot()) != 1 {
		t.Errorf("foreign userinfo reached destination: %+v", f.snapshot())
	}
}

func configureOCIChain(f *ociRegistryFixture, kind, destination, scheme string) {
	f.respond = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Host != destination {
			return false
		}
		if !strings.HasPrefix(r.URL.Path, "/hop/") {
			http.Redirect(w, r, "/hop"+r.URL.Path, http.StatusFound)
		} else {
			http.Error(w, "terminal fixture response", http.StatusTeapot)
		}
		return true
	}
	f.redirect = func(r *http.Request) string {
		eligible := kind == ociTestBasicKind && r.URL.Host == ociTestHost &&
			strings.HasPrefix(r.Header.Get("Authorization"), "Basic ")
		eligible = eligible || kind == ociTestCachedBearer && r.URL.Host == ociTestHost &&
			strings.Contains(r.URL.Path, "/blobs/") && r.Header.Get("Authorization") == "Bearer "+ociTestToken
		eligible = eligible || kind == ociTestTokenService && r.URL.Host == ociTestAuthHost &&
			strings.HasPrefix(r.Header.Get("Authorization"), "Basic ")
		if eligible {
			return scheme + "://" + destination + r.URL.Path
		}
		return ""
	}
}

func TestOCIRedirectFallbackKeepsOriginScope(t *testing.T) {
	f := newOCIRegistryFixture(t)
	installOCIFixture(t, f, ociTestRegistryMode)
	fixture := newOCICommandFixture(t)
	for _, userinfo := range []bool{false, true} {
		t.Run(fmt.Sprintf("userinfo=%t", userinfo), func(t *testing.T) {
			env := "KUBOCD_OCI_TEST_CHAIN=Basic"
			if userinfo {
				env = "KUBOCD_OCI_TEST_USERINFO=true"
			}
			out, records, err := fixture.run(t, []string{sourceDumpCommand, ociTestCommand,
				ociTestReference, ociTestHTTPFlag, ociTestAllowFlag}, env)
			if userinfo {
				if err == nil || !strings.Contains(out, ociForeignUserinfoMessage) {
					t.Errorf("fallback accepted foreign userinfo: %v %s", err, out)
				}
				if len(records) != 1 {
					t.Errorf("foreign userinfo reached transport: %+v", records)
				}
				return
			}
			if err == nil || !strings.Contains(out, "418") {
				t.Errorf("terminal response not reached: %v %s", err, out)
			}
			assertOCIChainRecords(t, records, ociTestBasicKind, "registry.test:8443")
		})
	}
}

func assertOCIControlRedirect(t *testing.T, records []ociRequestRecord, mode string, err error) {
	t.Helper()
	initial, redirected := 0, 0
	destination := "https://other.test"
	switch mode {
	case ociTestCrossOriginPort:
		destination = "https://registry.test:8443"
	case ociTestCrossOriginSubdomain:
		destination = "https://sub.registry.test"
	case ociTestAnonymousDowngrade:
		destination = "http://other.test"
	case ociTestSameOrigin:
		destination = "https://registry.test"
	}
	for _, r := range records {
		u, parseErr := url.Parse(r.URL)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if u.Host == ociTestHost && u.Scheme == "https" && u.RawQuery == "" &&
			(r.Auth != "" || mode == ociTestAnonymousDowngrade) {
			initial++
		}
		if u.Scheme+"://"+u.Host == destination && (mode != ociTestSameOrigin || u.Query().Get("redirected") == "1") {
			redirected++
			if mode == ociTestSameOrigin {
				if !strings.HasPrefix(r.Auth, "Basic ") {
					t.Errorf("same-origin redirect lost auth: %+v", r)
				}
			} else if r.Auth != "" {
				t.Errorf("foreign redirect authenticated: %+v", r)
			}
		}
	}
	if initial == 0 || redirected == 0 {
		t.Errorf("redirect control not exercised: initial=%d target=%d", initial, redirected)
	}
	if strings.HasPrefix(mode, "crossOrigin") && (err == nil || !strings.Contains(err.Error(), "401")) {
		t.Errorf("foreign terminal 401 not observed: %v", err)
	}
}
