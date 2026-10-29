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

package cmn

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

const (
	gitHTTP              = "http"
	gitHTTPS             = "https"
	gitCredentialsTLS    = "Git credentials require HTTPS"
	gitCredentialsOrigin = "Git credentials cannot be sent to a different origin"
	gitDowngrade         = "Git HTTPS-to-HTTP redirect is not allowed"
)

// InstallProtocol mutates an unlocked process-global map. Every CLI go-git clone,
// including native SSH/file clones, must share this lock while reading that map.
var gitCloneMutex sync.Mutex

var gitEscapedByte = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)

var gitDiagnosticAuthority = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^/?#\r\n<>]*`)

var gitDiagnosticURL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"<>]+`)

type gitOrigin struct {
	scheme, host string
	port         int
}
type gitCredentialPolicy struct {
	origin        gitOrigin
	token         string
	authenticated bool
	secrets       []string
}

func asciiGitHostname(host string) bool {
	for i := 0; i < len(host); i++ {
		if host[i] >= 128 {
			return false
		}
	}
	return true
}

func validGitHostname(host string) bool {
	if !asciiGitHostname(host) || len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.ToLower(host), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			letter := c >= 'a' && c <= 'z'
			digit := c >= '0' && c <= '9'
			if !letter && !digit && c != '-' {
				return false
			}
		}
	}
	return true
}

func gitPort(raw string) (int, error) {
	if raw == "" {
		return 0, errors.New("invalid port")
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid port")
		}
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("invalid port")
	}
	return port, nil
}

func gitTokenHosts(value string) (map[gitOrigin]bool, error) {
	if strings.TrimSpace(value) == "" {
		value = "github.com"
	}
	hosts := make(map[gitOrigin]bool)
	for index, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		host, rawPort, hasPort := strings.Cut(entry, ":")
		port := 443
		var err error
		if hasPort {
			port, err = gitPort(rawPort)
		}
		if err != nil || !validGitHostname(host) {
			return nil, fmt.Errorf(
				"invalid KCD_GIT_TOKEN_HOSTS entry %d: expected a hostname with an optional port from 1 to 65535 "+
					"(no scheme, path, userinfo, wildcard, or IPv6 literal)",
				index+1,
			)
		}
		hosts[gitOrigin{scheme: gitHTTPS, host: strings.ToLower(host), port: port}] = true
	}
	return hosts, nil
}

func gitURLOrigin(u *url.URL) (gitOrigin, error) {
	invalid := errors.New("invalid Git URL")
	scheme := strings.ToLower(u.Scheme)
	if (scheme != gitHTTPS && scheme != gitHTTP) || u.Opaque != "" || u.Host == "" || strings.HasSuffix(u.Host, ":") {
		return gitOrigin{}, invalid
	}
	if !asciiGitHostname(u.Hostname()) {
		return gitOrigin{}, invalid
	}
	host := strings.ToLower(u.Hostname())
	if address, err := netip.ParseAddr(host); err == nil {
		host = address.String()
	} else if !validGitHostname(host) {
		return gitOrigin{}, invalid
	}
	port := 443
	if scheme == gitHTTP {
		port = 80
	}
	if raw := u.Port(); raw != "" {
		var err error
		port, err = gitPort(raw)
		if err != nil {
			return gitOrigin{}, invalid
		}
	}
	return gitOrigin{scheme: scheme, host: host, port: port}, nil
}

func newGitCredentialPolicy(rawURL string) (*gitCredentialPolicy, error) {
	hosts, err := gitTokenHosts(os.Getenv("KCD_GIT_TOKEN_HOSTS"))
	if err != nil {
		return nil, err
	}
	token := os.Getenv("GITHUB_TOKEN")
	policy := &gitCredentialPolicy{secrets: []string{token}}
	u, err := url.Parse(rawURL)
	if err == nil && u.User != nil {
		password, _ := u.User.Password()
		policy.secrets = append(
			policy.secrets,
			password,
			base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+password)),
		)
	}
	lowerURL := strings.ToLower(rawURL)
	if !strings.HasPrefix(lowerURL, gitHTTPS+"://") && !strings.HasPrefix(lowerURL, gitHTTP+"://") {
		return policy, nil
	}
	if err != nil {
		return nil, errors.New("invalid Git URL")
	}
	origin, err := gitURLOrigin(u)
	if err != nil {
		return nil, err
	}
	policy.origin = origin
	if u.User != nil {
		if origin.scheme != gitHTTPS {
			return nil, errors.New(gitCredentialsTLS) //nolint:staticcheck // Git is a proper noun.
		}

		policy.authenticated = true
	}
	if origin.scheme == gitHTTPS && hosts[origin] && token != "" {
		policy.token = token
		policy.authenticated = true
	}
	policy.secrets = append(policy.secrets, base64.StdEncoding.EncodeToString([]byte("git:"+token)))
	return policy, nil
}

type gitCredentialTransport struct {
	policy *gitCredentialPolicy
	base   http.RoundTripper
}

func gitRequestHasCredentials(req *http.Request) bool {
	if req.URL.User != nil {
		return true
	}
	for _, value := range req.Header.Values("Authorization") {
		if value != "" {
			return true
		}
	}
	return false
}

func refuseGitRequest(req *http.Request, message string) (*http.Response, error) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return nil, errors.New(message)
}

func (t *gitCredentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, err := gitURLOrigin(req.URL); err != nil {
		return refuseGitRequest(req, err.Error())
	}
	if gitRequestHasCredentials(req) {
		if !strings.EqualFold(req.URL.Scheme, gitHTTPS) {
			return refuseGitRequest(req, gitCredentialsTLS)
		}
		origin, err := gitURLOrigin(req.URL)
		if err != nil || origin != t.policy.origin {
			return refuseGitRequest(req, gitCredentialsOrigin)
		}
	}
	return t.base.RoundTrip(req)
}

func (p *gitCredentialPolicy) checkRedirect(req *http.Request, via []*http.Request) error {
	if _, err := gitURLOrigin(req.URL); err != nil {
		return err
	}
	if len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, gitHTTPS) &&
		strings.EqualFold(req.URL.Scheme, gitHTTP) {
		return errors.New(gitDowngrade) //nolint:staticcheck // Git is a proper noun.
	}
	if p.authenticated {
		origin, err := gitURLOrigin(req.URL)
		if err != nil || origin != p.origin {
			return errors.New(gitCredentialsOrigin) //nolint:staticcheck // Git is a proper noun.
		}
	}
	if len(via) >= 10 {
		return errors.New("http redirect: too many redirects")
	}
	return nil
}

func cloneGitWithCredentials(
	location string,
	options *git.CloneOptions,
	policy *gitCredentialPolicy,
	base http.RoundTripper,
) error {
	gitCloneMutex.Lock()
	defer gitCloneMutex.Unlock()
	if policy.token != "" {
		options.Auth = &githttp.BasicAuth{Username: "git", Password: policy.token}
	}
	if policy.origin.scheme != "" {
		if base == nil {
			base = http.DefaultTransport
		}
		client := &http.Client{
			Transport:     &gitCredentialTransport{policy: policy, base: base},
			CheckRedirect: policy.checkRedirect,
		}
		previousHTTP, previousHTTPS := gitclient.Protocols[gitHTTP], gitclient.Protocols[gitHTTPS]
		defer func() {
			gitclient.InstallProtocol(gitHTTP, previousHTTP)
			gitclient.InstallProtocol(gitHTTPS, previousHTTPS)
		}()
		transport := githttp.NewClient(client)
		gitclient.InstallProtocol(gitHTTP, transport)
		gitclient.InstallProtocol(gitHTTPS, transport)
	}
	_, err := git.PlainClone(location, false, options)
	if err != nil {
		// Do not retain an unsafe library error through Unwrap, including net/url errors.
		return errors.New(policy.redact(err.Error()))
	}
	return nil
}

func (p *gitCredentialPolicy) redact(text string) string {
	secrets := append([]string(nil), p.secrets...)
	malformed := false
	text = gitDiagnosticURL.ReplaceAllStringFunc(text, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			malformed = true
			return raw
		}
		if u.User != nil {
			password, _ := u.User.Password()
			secrets = append(
				secrets,
				password,
				base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+password)),
			)
			u.User = nil
		}
		return u.String()
	})
	if malformed || unresolvedGitUserinfo(text) {
		return "Git clone failed"
	}
	variants := make([]string, 0, len(secrets)*3)
	for _, secret := range secrets {
		if secret != "" {
			quoted := strconv.Quote(secret)
			encoded, _ := json.Marshal(secret)
			variants = append(
				variants,
				quoted, quoted[1:len(quoted)-1],
				string(encoded), string(encoded[1:len(encoded)-1]),
				secret,
				url.QueryEscape(secret),
				url.PathEscape(secret),
				url.UserPassword("", secret).String()[1:],
			)
		}
	}
	for _, encoded := range append([]string(nil), variants...) {
		variants = append(variants, gitEscapedByte.ReplaceAllStringFunc(encoded, strings.ToLower))
	}
	sort.Slice(variants, func(i, j int) bool { return len(variants[i]) > len(variants[j]) })
	for _, secret := range variants {
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	return text
}

func unresolvedGitUserinfo(text string) bool {
	for _, raw := range gitDiagnosticAuthority.FindAllString(text, -1) {
		_, authority, _ := strings.Cut(raw, "://")
		fields := strings.Fields(authority)
		for index, field := range fields {
			before, _, hasAt := strings.Cut(field, "@")
			// A split user:password@authority may follow whitespace in a malformed URL.
			if hasAt && (index == 0 || strings.Contains(before, ":")) {
				return true
			}
		}
	}
	return false
}
