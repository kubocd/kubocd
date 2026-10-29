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

//nolint:goconst // Protocol names in the test fixture are independent of production constants.
package cmn

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	gittransport "github.com/go-git/go-git/v5/plumbing/transport"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

const gitTestToken = "fake-git-token-123456"

type gitRecordedRequest struct{ method, target, auth string }
type gitRecordingTransport struct {
	mu       sync.Mutex
	requests []gitRecordedRequest
	handler  http.Handler
}

func (r *gitRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.requests = append(r.requests, gitRecordedRequest{req.Method, req.URL.String(), req.Header.Get("Authorization")})
	r.mu.Unlock()
	result := httptest.NewRecorder()
	r.handler.ServeHTTP(result, req)
	response := result.Result()
	response.Request = req
	return response, nil
}
func (r *gitRecordingTransport) snapshot() []gitRecordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]gitRecordedRequest(nil), r.requests...)
}

type gitRoundTripFunc func(*http.Request) (*http.Response, error)

func (f gitRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func gitTestBasic(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}
func installGitBase(t *testing.T, base http.RoundTripper) {
	t.Helper()
	previous := http.DefaultTransport
	protocols := map[string]gittransport.Transport{
		"http":  gitclient.Protocols["http"],
		"https": gitclient.Protocols["https"],
	}
	http.DefaultTransport = base
	for scheme := range protocols {
		gitclient.InstallProtocol(scheme, githttp.NewClient(&http.Client{Transport: base}))
	}
	t.Cleanup(func() {
		http.DefaultTransport = previous
		for scheme, value := range protocols {
			gitclient.InstallProtocol(scheme, value)
		}
	})
	sourceHelm(t, ":")
}
func installGitRecorder(t *testing.T, handler http.Handler) *gitRecordingTransport {
	t.Helper()
	rec := &gitRecordingTransport{handler: handler}
	installGitBase(t, rec)
	t.Setenv("GITHUB_TOKEN", gitTestToken)
	t.Setenv("KCD_GIT_TOKEN_HOSTS", "")
	return rec
}
func gitSmartFixture(t *testing.T) http.Handler {
	t.Helper()
	source := t.TempDir()
	sourceChart(t, source)
	sourceRepo(t, source)
	root := t.TempDir()
	bare := filepath.Join(root, "repo.git")
	command := exec.Command("git", "clone", "--bare", source, bare)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("smart HTTP fixture: %v: %s", err, out)
	}
	executable, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	return &cgi.Handler{
		Path: executable,
		Args: []string{"http-backend"},
		Root: "/",
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
}

type gitAuthRecorder struct {
	called bool
	auth   gittransport.AuthMethod
	err    error
}

func (r *gitAuthRecorder) NewUploadPackSession(
	_ *gittransport.Endpoint,
	auth gittransport.AuthMethod,
) (gittransport.UploadPackSession, error) {
	r.called = true
	r.auth = auth
	if r.err != nil {
		return nil, r.err
	}
	return nil, errors.New("fixture stopped native transport")
}

func (r *gitAuthRecorder) NewReceivePackSession(
	_ *gittransport.Endpoint,
	_ gittransport.AuthMethod,
) (gittransport.ReceivePackSession, error) {
	return nil, errors.New("unexpected receive pack")
}
