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

//nolint:goconst // Keep table inputs and expected diagnostics together.
package cmn

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gittransport "github.com/go-git/go-git/v5/plumbing/transport"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
)

func TestGitTokenInitialScope(t *testing.T) {
	for _, tc := range []struct {
		name, hosts, target string
		want                bool
	}{
		{"default", "", "https://github.com/repo.git", true},
		{"explicit443", "", "https://github.com:443/repo.git", true},
		{"otherPort", "", "https://github.com:8443/repo.git", false},
		{"subdomain", "", "https://sub.github.com/repo.git", false},
		{"suffix", "", "https://github.com.evil.test/repo.git", false},
		{"unrelated", "", "https://other.test/repo.git", false},
		{"http", "", "http://github.com/repo.git", false},
		{"arbitraryHTTP", "", "http://other.test/repo.git", false},
		{"replacement", "enterprise.test", "https://github.com/repo.git", false},
		{"enterprise", "enterprise.test:8443", "https://enterprise.test:8443/repo.git", true},
		{"trimCase", "  GITHUB.com:00443 , enterprise.test  ", "https://GitHub.COM/repo.git", true},
		{"whitespace", " \t ", "https://github.com/repo.git", true},
		{"duplicate", "github.com,github.com:443", "https://github.com/repo.git", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := installGitRecorder(t, http.NotFoundHandler())
			t.Setenv("KCD_GIT_TOKEN_HOSTS", tc.hosts)
			_ = credentialClone(t, tc.target)
			seen := rec.snapshot()
			if len(seen) != 1 {
				t.Fatalf("expected one discovery request, got %d", len(seen))
			}
			want := ""
			if tc.want {
				want = gitTestBasic("git", gitTestToken)
			}
			if seen[0].auth != want {
				t.Errorf("wrong initial credential scope: auth=%q want=%q", seen[0].auth, want)
			}
		})
	}
}

func TestGitTokenHostConfiguration(t *testing.T) {
	invalid := []string{
		"https://github.com",
		"github.com/path",
		"user:pass@github.com",
		"*.github.com",
		"github.com?x",
		"github.com#x",
		`github.com\path`,
		"git hub.com",
		"git\nhub.com",
		",github.com",
		"github.com,",
		"github.com,,other.test",
		":443",
		"github.com:",
		"github.com:+443",
		"github.com:-1",
		"github.com:abc",
		"github.com:0",
		"github.com:65536",
		"-git.test",
		"git-.test",
		"git..test",
		"github.com.",
		"githüb.test",
		"[::1]",
		"::1",
		"[fe80::1%lo0]:443",
	}
	for _, entry := range invalid {
		t.Run(entry, func(t *testing.T) {
			rec := installGitRecorder(t, http.NotFoundHandler())
			t.Setenv("KCD_GIT_TOKEN_HOSTS", entry)
			previousSSH := gitclient.Protocols["ssh"]
			native := &gitAuthRecorder{}
			gitclient.InstallProtocol("ssh", native)
			t.Cleanup(func() { gitclient.InstallProtocol("ssh", previousSSH) })
			index := 1
			if entry == "github.com," || entry == "github.com,,other.test" {
				index = 2
			}
			want := fmt.Sprintf(
				"invalid KCD_GIT_TOKEN_HOSTS entry %d: expected a hostname with an optional port from 1 to 65535 "+
					"(no scheme, path, userinfo, wildcard, or IPv6 literal)",
				index,
			)
			for _, token := range []string{gitTestToken, ""} {
				t.Setenv("GITHUB_TOKEN", token)
				for _, target := range []string{"https://github.com/repo.git", "ssh://git@git.test/repo", "file:///missing/repo"} {
					err := credentialClone(t, target)
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Errorf("invalid policy not refused before clone: got %v, want %s", err, want)
					}
				}
			}
			if len(rec.snapshot()) != 0 || native.called {
				t.Error("invalid policy reached the transport")
			}
		})
	}
}

func TestGitRedirectCredentialScope(t *testing.T) {
	const scopeDiagnostic = "Git credentials cannot be sent to a different origin"
	for _, tc := range []struct {
		name, destination string
		anonymous         bool
		want              string
	}{
		{"port", "https://github.com:8443/repo.git/info/refs", false, scopeDiagnostic},
		{"subdomain", "https://sub.github.com/repo.git/info/refs", false, scopeDiagnostic},
		{"unrelated", "https://other.test/repo.git/info/refs", false, scopeDiagnostic},
		{"alsoAllowed", "https://enterprise.test/repo.git/info/refs", false, scopeDiagnostic},
		{"downgrade", "http://github.com/repo.git/info/refs", false, "Git HTTPS-to-HTTP redirect is not allowed"},
		{"anonymousDowngrade", "http://github.com/repo.git/info/refs", true, "Git HTTPS-to-HTTP redirect is not allowed"},
		{"injectedUserinfo", "https://someone:redirect-secret@other.test/repo.git/info/refs", true, scopeDiagnostic},
	} {
		for _, chain := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chain=%t", tc.name, chain), func(t *testing.T) {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Host != "github.com" || r.URL.Scheme != "https" {
						http.Redirect(w, r, "https://github.com/return/info/refs", http.StatusFound)
						return
					}
					if chain && strings.HasPrefix(r.URL.Path, "/repo.git/") {
						http.Redirect(w, r, "/step/info/refs?service=git-upload-pack", http.StatusFound)
						return
					}
					http.Redirect(w, r, tc.destination+"?service=git-upload-pack", http.StatusFound)
				})
				rec := installGitRecorder(t, handler)
				t.Setenv("KCD_GIT_TOKEN_HOSTS", "github.com,enterprise.test")
				if tc.anonymous {
					t.Setenv("GITHUB_TOKEN", "")
				}
				err := credentialClone(t, "https://github.com/repo.git")
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("redirect not refused with scope diagnostic: %v", err)
				}
				limit := 1
				if chain {
					limit = 2
				}
				if len(rec.snapshot()) != limit {
					t.Errorf(
						"forbidden redirect reached base transport: %d requests, want %d",
						len(rec.snapshot()),
						limit,
					)
				}
			})
		}
	}
}

func TestGitUploadPackCredentialScope(t *testing.T) {
	backend := gitSmartFixture(t)
	for _, tc := range []struct {
		name, target                       string
		redirect, anonymousRedirect, token bool
	}{
		{"allowed", "https://github.com/repo.git", false, false, true},
		{"sameOrigin", "https://github.com/start", true, false, true},
		{"unlisted", "https://other.test/repo.git", false, false, false},
		{"http", "http://github.com/repo.git", false, false, false},
		{"anonymousCrossOrigin", "https://public.test/start", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := installGitRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/start/") {
					destination := "/repo.git/info/refs?service=git-upload-pack"
					if tc.anonymousRedirect {
						destination = "https://other.test" + destination
					}
					http.Redirect(w, r, destination, http.StatusFound)
					return
				}
				backend.ServeHTTP(w, r)
			}))
			protocols := map[string]gittransport.Transport{
				"http":  gitclient.Protocols["http"],
				"https": gitclient.Protocols["https"],
			}
			archive, err := credentialCloneArchive(t, tc.target)
			for scheme, previous := range protocols {
				if gitclient.Protocols[scheme] != previous {
					t.Errorf("%s protocol not restored after clone", scheme)
				}
			}
			if err != nil {
				t.Fatalf("smart HTTP clone failed: %v", err)
			}
			if sourceContents(t, archive)["module/Chart.yaml"] != "apiVersion: v2\nname: demo\nversion: 0.1.0\n" {
				t.Error("cloned chart bytes differ")
			}
			posts := 0
			for _, r := range rec.snapshot() {
				if r.method == http.MethodPost {
					posts++
				}
				want := ""
				if tc.token {
					want = gitTestBasic("git", gitTestToken)
				}
				if r.auth != want {
					t.Errorf("credential on %s %s: got %q want %q", r.method, r.target, r.auth, want)
				}
			}
			if posts == 0 {
				t.Error("fixture never reached upload-pack")
			}
		})
	}
	t.Run("uploadPackRedirectRefused", func(t *testing.T) {
		rec := installGitRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				http.Redirect(w, r, "/redirected-upload", http.StatusTemporaryRedirect)
				return
			}
			backend.ServeHTTP(w, r)
		}))
		err := credentialClone(t, "https://github.com/repo.git")
		if err == nil || !strings.Contains(err.Error(), "redirect on non-initial request") {
			t.Errorf("upload-pack redirect unexpectedly allowed: %v", err)
		}
		for _, r := range rec.snapshot() {
			if strings.Contains(r.target, "redirected-upload") {
				t.Error("upload-pack redirect reached target")
			}
		}
	})
	t.Run("trustedTLSServer", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("TLS listener proof blocked: %v", err)
		}
		record := &gitRecordingTransport{}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			record.mu.Lock()
			record.requests = append(record.requests,
				gitRecordedRequest{r.Method, r.URL.String(), r.Header.Get("Authorization")})
			record.mu.Unlock()
			backend.ServeHTTP(w, r)
		})
		server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
		server.StartTLS()
		t.Cleanup(server.Close)
		base := server.Client().Transport
		installGitBase(t, base)
		t.Setenv("GITHUB_TOKEN", gitTestToken)
		t.Setenv("KCD_GIT_TOKEN_HOSTS", strings.TrimPrefix(server.URL, "https://"))
		if _, err := credentialCloneArchive(t, server.URL+"/repo.git"); err != nil {
			t.Fatalf("trusted TLS clone failed: %v", err)
		}
		posts := 0
		for _, request := range record.snapshot() {
			if request.auth != gitTestBasic("git", gitTestToken) {
				t.Error("TLS server did not receive scoped credential")
			}
			if request.method == http.MethodPost {
				posts++
			}
		}
		if posts == 0 {
			t.Error("TLS server did not receive upload-pack")
		}
	})
}

func TestGitURLCredentialScope(t *testing.T) {
	backend := gitSmartFixture(t)
	t.Run("sameOrigin", func(t *testing.T) {
		rec := installGitRecorder(t, backend)
		t.Setenv("GITHUB_TOKEN", "")
		if _, err := credentialCloneArchive(t, "https://alice:fake-password@git.test/repo.git"); err != nil {
			t.Fatal(err)
		}
		for _, r := range rec.snapshot() {
			if r.auth != gitTestBasic("alice", "fake-password") {
				t.Error("URL credential missing from discovery/upload-pack")
			}
		}
	})
	for _, user := range []string{"alice:fake-password", "alice", ""} {
		t.Run("HTTP/"+user, func(t *testing.T) {
			rec := installGitRecorder(t, http.NotFoundHandler())
			t.Setenv("GITHUB_TOKEN", "")
			err := credentialClone(t, "http://"+user+"@git.test/repo.git")
			if err == nil || !strings.Contains(err.Error(), "Git credentials require HTTPS") {
				t.Errorf("HTTP URL credentials not refused: %v", err)
			}
			if len(rec.snapshot()) != 0 {
				t.Error("HTTP URL credentials reached base transport")
			}
		})
	}
	for _, target := range []string{"https://git.test:8443", "https://sub.git.test", "https://other.test"} {
		t.Run(target, func(t *testing.T) {
			rec := installGitRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target+"/repo.git/info/refs?service=git-upload-pack", http.StatusFound)
			}))
			t.Setenv("GITHUB_TOKEN", "")
			err := credentialClone(t, "https://alice:fake-password@git.test/repo.git")
			if err == nil || !strings.Contains(err.Error(), "Git credentials cannot be sent to a different origin") {
				t.Errorf("URL-derived credential scope not enforced: %v", err)
			}
			if len(rec.snapshot()) != 1 {
				t.Error("URL-derived credential redirect reached target")
			}
		})
	}
	t.Run("HTTPRedirectUserinfo", func(t *testing.T) {
		rec := installGitRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.User != nil {
				http.NotFound(w, r)
				return
			}
			http.Redirect(
				w,
				r,
				"http://alice:fake-password@git.test/repo.git/info/refs?service=git-upload-pack",
				http.StatusFound,
			)
		}))
		t.Setenv("GITHUB_TOKEN", "")
		err := credentialClone(t, "http://git.test/repo.git")
		if err == nil || !strings.Contains(err.Error(), "Git credentials require HTTPS") {
			t.Errorf("HTTP redirect userinfo not refused: %v", err)
		}
		if len(rec.snapshot()) != 1 {
			t.Error("HTTP redirect userinfo reached base transport")
		}
	})
	t.Run("tokenPrecedence", func(t *testing.T) {
		rec := installGitRecorder(t, http.NotFoundHandler())
		_ = credentialClone(t, "https://alice:fake-password@github.com/repo.git")
		if seen := rec.snapshot(); len(seen) != 1 || seen[0].auth != gitTestBasic("git", gitTestToken) {
			t.Error("environment token precedence changed")
		}
	})
}

func TestGitNonHTTPDoesNotReceiveToken(t *testing.T) {
	installGitRecorder(t, http.NotFoundHandler())
	for _, tc := range []struct{ scheme, target string }{
		{"ssh", "ssh://git@git.test/repo"},
		{"ssh", "git@git.test:repo"},
		{"ssh", "https:repo"},
		{"git", "git://git.test/repo"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			old := gitclient.Protocols[tc.scheme]
			t.Cleanup(func() { gitclient.InstallProtocol(tc.scheme, old) })
			stub := &gitAuthRecorder{}
			gitclient.InstallProtocol(tc.scheme, stub)
			_ = credentialClone(t, tc.target)
			if !stub.called {
				t.Error("native Git transport was not used")
			}
			if stub.auth != nil {
				t.Errorf("non-HTTP transport received GITHUB_TOKEN auth: %T", stub.auth)
			}
		})
	}
	source := t.TempDir()
	sourceChart(t, source)
	sourceRepo(t, source)
	for _, target := range []string{source, "file://" + filepath.ToSlash(source)} {
		if _, err := credentialCloneArchive(t, target); err != nil {
			t.Errorf("local Git clone changed: %v", err)
		}
	}
	before := map[string]gittransport.Transport{
		"http":  gitclient.Protocols["http"],
		"https": gitclient.Protocols["https"],
	}
	for _, target := range []string{"https://github.com/repo.git", "https://other.test/repo.git"} {
		_ = credentialClone(t, target)
		for scheme, previous := range before {
			if !reflect.DeepEqual(gitclient.Protocols[scheme], previous) {
				t.Errorf("%s protocol not restored", scheme)
			}
		}
	}
}

func TestGitCredentialDiagnosticsAreRedacted(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, status := range []int{http.StatusInternalServerError, http.StatusNotFound} {
			t.Run(fmt.Sprintf("allow=%t/status=%d", allow, status), func(t *testing.T) {
				installGitRecorder(
					t,
					http.HandlerFunc(
						func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fixture failure", status) },
					),
				)
				t.Setenv("GITHUB_TOKEN", "")
				target := "https://alice:fake%2Fpassword@git.test/repo.git"
				out, err := gitCaptureClone(t, target, SourceOptions{AllowExternalLinks: allow})
				assertGitRedacted(t, out, err, []string{"alice:", "fake/password", "fake%2Fpassword"})
				if !strings.Contains(out, "https://git.test/repo.git") {
					t.Error("sanitized clone log lost safe host/path")
				}
			})
		}
	}
	t.Run("nativeError", func(t *testing.T) {
		installGitRecorder(t, http.NotFoundHandler())
		previous := gitclient.Protocols["ssh"]
		t.Cleanup(func() { gitclient.InstallProtocol("ssh", previous) })
		gitclient.InstallProtocol("ssh", &gitAuthRecorder{err: errors.New("native failure: fake/password fake%2Fpassword")})
		out, err := gitCaptureClone(t, "ssh://alice:fake%2Fpassword@git.test/repo", SourceOptions{})
		assertGitRedacted(t, out, err, []string{"alice:", "fake/password", "fake%2Fpassword"})
	})
	t.Run("transportError", func(t *testing.T) {
		installGitBase(t, gitRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf(
				"fixture at https://extra:fresh-secret@elsewhere.test/repo: %s %s %s %s %s",
				gitTestToken,
				"fake/password",
				"fake%2Fpassword",
				"fake%2fpassword",
				gitTestBasic("git", gitTestToken),
			)
		}))
		t.Setenv("GITHUB_TOKEN", gitTestToken)
		t.Setenv("KCD_GIT_TOKEN_HOSTS", "")
		out, err := gitCaptureClone(t, "https://alice:fake%2Fpassword@github.com/repo.git", SourceOptions{})
		assertGitRedacted(
			t,
			out,
			err,
			[]string{
				gitTestToken,
				"fake/password",
				"fake%2Fpassword",
				"fake%2fpassword",
				gitTestBasic("git", gitTestToken),
				"fresh-secret",
				"extra:",
			},
		)
		for e := err; e != nil; e = errors.Unwrap(e) {
			assertGitRedacted(t, "", e, []string{gitTestToken, "fake/password", "fresh-secret"})
		}
	})
	t.Run("malformed", func(t *testing.T) {
		installGitRecorder(t, http.NotFoundHandler())
		out, err := gitCaptureClone(t, "https://alice:fake-secret@git.test:bad/repo.git", SourceOptions{})
		assertGitRedacted(t, out, err, []string{"alice:", "fake-secret"})
		if err == nil || !strings.Contains(err.Error(), "invalid Git URL") {
			t.Errorf("unsafe invalid URL diagnostic: %v", err)
		}
	})
	t.Run("malformedErrorUserinfo", func(t *testing.T) {
		installGitBase(t, gitRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("unexpected https://new user:fresh-secret@elsewhere.test/repo")
		}))
		t.Setenv("GITHUB_TOKEN", gitTestToken)
		t.Setenv("KCD_GIT_TOKEN_HOSTS", "")
		out, err := gitCaptureClone(t, "https://github.com/repo.git", SourceOptions{})
		assertGitRedacted(t, out, err, []string{"fresh-secret", "new user:"})
	})
	t.Run("redirectURL", func(t *testing.T) {
		installGitRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(
				w,
				r,
				"https://extra:fresh-secret@elsewhere.test/repo.git/info/refs?service=git-upload-pack",
				http.StatusFound,
			)
		}))
		out, err := gitCaptureClone(t, "https://github.com/repo.git", SourceOptions{})
		assertGitRedacted(t, out, err, []string{"extra:", "fresh-secret", gitTestToken})
	})
}

func TestGitCredentialDiagnosticsRemainReadable(t *testing.T) {
	t.Run("oneLetterUser", func(t *testing.T) {
		installGitRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "fixture failure", http.StatusInternalServerError)
		}))
		t.Setenv("GITHUB_TOKEN", "")
		out, err := gitCaptureClone(t, "https://u:FAKE-LOT3-URLPASS@git.test/charts.git", SourceOptions{})
		assertGitRedacted(t, out, err, []string{"u:", "FAKE-LOT3-URLPASS"})
		if err == nil {
			t.Fatal("expected server failure")
		}
		for _, word := range []string{"unexpected", "git-upload-pack", "charts.git"} {
			if !strings.Contains(err.Error(), word) {
				t.Errorf("diagnostic lost %q: %v", word, err)
			}
		}
	})
	t.Run("sshUser", func(t *testing.T) {
		installGitRecorder(t, http.NotFoundHandler())
		previous := gitclient.Protocols["ssh"]
		t.Cleanup(func() { gitclient.InstallProtocol("ssh", previous) })
		gitclient.InstallProtocol("ssh", &gitAuthRecorder{err: errors.New("unexpected native failure for charts.git")})
		out, err := gitCaptureClone(t, "ssh://git@git.test/charts.git", SourceOptions{})
		if !strings.Contains(out, "ssh://git.test/charts.git") || err == nil || !strings.Contains(err.Error(), "charts.git") {
			t.Errorf("username damaged diagnostic: %s %v", out, err)
		}
		if strings.Contains(out, "git@") {
			t.Errorf("userinfo remains: %s", out)
		}
	})
}

func credentialClone(t *testing.T, target string) error {
	t.Helper()
	_, err := credentialCloneArchive(t, target)
	return err
}
func credentialCloneArchive(t *testing.T, target string) (string, error) {
	t.Helper()
	return getHelmChartArchiveFromGit("", target, "main", "", ".", "module", t.TempDir(), nil)
}
func gitCaptureClone(t *testing.T, target string, opts SourceOptions) (string, error) {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	previous := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = previous }()
	_, cloneErr := getHelmChartArchiveFromGit("", target, "main", "", ".", "module", t.TempDir(), nil, opts)
	b, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b), cloneErr
}
func assertGitRedacted(t *testing.T, out string, err error, secrets []string) {
	t.Helper()
	text := out
	if err != nil {
		text += err.Error()
	}
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Errorf("credential diagnostic leaked %q", secret)
		}
	}
}
