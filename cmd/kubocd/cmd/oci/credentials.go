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

package oci

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const CredentialHelp = "Allow OCI authentication over HTTP, including token services and redirects. " +
	"Disabled by default; anonymous HTTP token flows remain supported. " +
	"Does not select HTTP, disable certificate checks, or relax Git protections."

const credentialTLSMessage = "OCI credentials require HTTPS; " +
	"use --allowInsecureCredentials to allow authentication over HTTP"
const credentialDowngradeMessage = "OCI HTTPS-to-HTTP redirect with credentials is not allowed; " +
	"use --allowInsecureCredentials to allow authentication over HTTP"

type CredentialOptions struct {
	AllowInsecureCredentials bool
}

func NewRegistryClient(registry string, plainHTTP, anonymous bool, options CredentialOptions) (*auth.Client, error) {
	credential := auth.Credential{}
	if !anonymous {
		user, secret, err := GetCredentials(registry)
		if err != nil {
			return nil, fmt.Errorf("failed to get credentials for repository '%s': %w", registry, err)
		}
		if secret != "" {
			credential.Username, credential.Password = user, secret
		}
	}
	configured := credential != (auth.Credential{})
	if plainHTTP && configured && !options.AllowInsecureCredentials {
		return nil, errors.New(credentialTLSMessage)
	}
	policy := credentialTransport{base: retry.NewTransport(http.DefaultTransport), configured: configured,
		allow: options.AllowInsecureCredentials}
	var header http.Header
	if !configured {
		header = auth.DefaultClient.Header.Clone()
	}
	return &auth.Client{
		Client: &http.Client{Transport: policy, CheckRedirect: policy.checkRedirect},
		Header: header,
		Cache:  auth.NewCache(), Credential: auth.StaticCredential(registry, credential),
	}, nil
}

type credentialTransport struct {
	base       http.RoundTripper
	configured bool
	allow      bool
}

func (t credentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Token requests can put credentials in a body; an empty Authorization header is not sufficient.
	if !t.allow && (t.configured || req.URL.User != nil) && !strings.EqualFold(req.URL.Scheme, "https") {
		return nil, errors.New(credentialTLSMessage)
	}
	return t.base.RoundTrip(req)
}
func (t credentialTransport) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("OCI redirect limit exceeded")
	}
	protected := t.configured || req.URL.User != nil
	for _, previous := range via {
		protected = protected || previous.URL.User != nil
	}
	if !t.allow && protected && len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https") &&
		strings.EqualFold(req.URL.Scheme, "http") {
		return errors.New(credentialDowngradeMessage)
	}
	if len(via) > 0 && !sameOCIOrigin(req.URL, via[0].URL) {
		// net/http may restore original headers on later hops within a foreign origin.
		req.Header.Del("Authorization")
		if req.URL.User != nil {
			return errors.New("OCI redirect to a different origin with URL userinfo is not allowed")
		}
	}
	return nil
}

type ociOrigin struct {
	scheme, host string
	port         int
}

func redirectOCIOrigin(u *url.URL) (ociOrigin, bool) {
	host := u.Hostname()
	if host == "" {
		return ociOrigin{}, false
	}
	// Do not fold Unicode hosts before net/http applies IDNA to the actual destination.
	ascii := true
	for i := 0; i < len(host); i++ {
		if host[i] >= 128 {
			ascii = false
			break
		}
	}
	if ascii {
		host = strings.ToLower(host)
	}
	scheme := strings.ToLower(u.Scheme)
	port := 443
	switch scheme {
	case "http":
		port = 80
	case "https":
	default:
		return ociOrigin{}, false
	}
	if raw := u.Port(); raw != "" {
		var err error
		port, err = strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return ociOrigin{}, false
		}
	}
	return ociOrigin{scheme: scheme, host: host, port: port}, true
}

func sameOCIOrigin(a, b *url.URL) bool {
	originA, validA := redirectOCIOrigin(a)
	originB, validB := redirectOCIOrigin(b)
	return validA && validB && originA == originB
}
