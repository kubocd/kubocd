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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
)

func TestGitMalformedURLDiagnosticsAreRedacted(t *testing.T) {
	for _, secret := range []string{"sec<ret", "sec>ret"} {
		t.Run(secret, func(t *testing.T) {
			installGitRecorder(t, http.NotFoundHandler())
			target := "ssh://alice:" + secret + "@git.test/repo"
			previous := gitclient.Protocols["ssh"]
			t.Cleanup(func() { gitclient.InstallProtocol("ssh", previous) })
			gitclient.InstallProtocol("ssh", &gitAuthRecorder{err: errors.New("native failure: " + target)})
			out, err := gitCaptureClone(t, target, SourceOptions{})
			if err == nil {
				t.Fatal("expected malformed clone to fail")
			}
			assertGitRedacted(t, out, err, []string{secret, "alice:"})
			if !strings.Contains(out, "Git clone failed") || !strings.Contains(err.Error(), "Git clone failed") {
				t.Errorf("malformed URL did not suppress the diagnostic: stdout=%q error=%v", out, err)
			}
		})
	}
}

func TestGitUnicodeOriginsAreRefused(t *testing.T) {
	for _, token := range []string{"", gitTestToken} {
		for _, scheme := range []string{"http", "https"} {
			t.Run(fmt.Sprintf("initial/%s/token=%t", scheme, token != ""), func(t *testing.T) {
				recorder := installGitRecorder(t, http.NotFoundHandler())
				t.Setenv("GITHUB_TOKEN", token)
				t.Setenv("KCD_GIT_TOKEN_HOSTS", "git.test")
				err := credentialClone(t, scheme+"://gİt.test/repo.git")
				if err == nil || !strings.Contains(err.Error(), "invalid Git URL") {
					t.Errorf("non-ASCII URL not refused: %v", err)
				}
				if len(recorder.snapshot()) != 0 {
					t.Error("non-ASCII initial URL reached transport")
				}
			})
		}
		t.Run(fmt.Sprintf("redirect/token=%t", token != ""), func(t *testing.T) {
			recorder := installGitRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Host == "git.test" {
					http.Redirect(w, r, "https://gİt.test/repo.git/info/refs", http.StatusFound)
					return
				}
				http.NotFound(w, r)
			}))
			t.Setenv("GITHUB_TOKEN", token)
			t.Setenv("KCD_GIT_TOKEN_HOSTS", "git.test")
			err := credentialClone(t, "https://git.test/repo.git")
			if err == nil || !strings.Contains(err.Error(), "invalid Git URL") {
				t.Errorf("non-ASCII redirect not refused: %v", err)
			}
			if len(recorder.snapshot()) != 1 {
				t.Errorf("non-ASCII redirect reached transport: %+v", recorder.snapshot())
			}
		})
	}
	t.Run("allowlist", func(t *testing.T) {
		recorder := installGitRecorder(t, http.NotFoundHandler())
		t.Setenv("KCD_GIT_TOKEN_HOSTS", "gİt.test")
		err := credentialClone(t, "https://git.test/repo.git")
		if err == nil || !strings.Contains(err.Error(), "invalid KCD_GIT_TOKEN_HOSTS") {
			t.Errorf("non-ASCII list accepted: %v", err)
		}
		if len(recorder.snapshot()) != 0 {
			t.Error("invalid list reached transport")
		}
	})
	t.Run("punycode", func(t *testing.T) {
		recorder := installGitRecorder(t, http.NotFoundHandler())
		t.Setenv("KCD_GIT_TOKEN_HOSTS", "xn--git-9dc.test")
		_ = credentialClone(t, "https://xn--git-9dc.test/repo.git")
		records := recorder.snapshot()
		if len(records) != 1 || records[0].auth != gitTestBasic("git", gitTestToken) {
			t.Errorf("punycode scope broken: %+v", records)
		}
	})
}

func TestGitQuotedSecretsAreRedacted(t *testing.T) {
	secret := `pa"ss\word<end>`
	jsonSecret, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	quoted := strconv.Quote(secret)
	for _, body := range []string{`{"error":` + string(jsonSecret) + `}`, "password = " + quoted} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			t.Run(fmt.Sprintf("status=%d/body=%s", status, body), func(t *testing.T) {
				installGitRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, body, status)
				}))
				t.Setenv("GITHUB_TOKEN", "")
				out, err := gitCaptureClone(t, "https://"+url.UserPassword("u", secret).String()+"@git.test/repo.git",
					SourceOptions{})
				if err == nil {
					t.Fatal("expected authentication failure")
				}
				secrets := []string{secret, quoted, quoted[1 : len(quoted)-1], string(jsonSecret),
					string(jsonSecret[1 : len(jsonSecret)-1])}
				assertGitRedacted(t, out, err, secrets)
				for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
					assertGitRedacted(t, "", cause, secrets)
				}
			})
		}
	}
}

func TestGitSafeAtSignsRemainReadable(t *testing.T) {
	t.Run("pathAndQuery", func(t *testing.T) {
		installGitRecorder(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "fixture failure", 500)
		}))
		target := "https://git.test/team@work/repo.git"
		out, err := gitCaptureClone(t, target, SourceOptions{})
		if !strings.Contains(out, target) {
			t.Errorf("safe URL lost: %s", out)
		}
		if err == nil || !strings.Contains(err.Error(), "team@work") || !strings.Contains(err.Error(), "500") {
			t.Errorf("safe error lost: %v", err)
		}
	})
	t.Run("query", func(t *testing.T) {
		installGitBase(t, gitRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("failure at https://git.test/repo.git?owner=dev@work")
		}))
		_, err := gitCaptureClone(t, "https://git.test/repo.git", SourceOptions{})
		if err == nil || !strings.Contains(err.Error(), "owner=dev@work") {
			t.Errorf("query lost: %v", err)
		}
	})
	t.Run("emailInProse", func(t *testing.T) {
		installGitBase(t, gitRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("failure at https://git.test contact support@example.test")
		}))
		_, err := gitCaptureClone(t, "https://git.test/repo.git", SourceOptions{})
		if err == nil || !strings.Contains(err.Error(), "contact support@example.test") {
			t.Errorf("prose email lost: %v", err)
		}
	})
}
