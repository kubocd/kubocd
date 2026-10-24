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

package controller

import (
	"archive/tar"
	"compress/gzip"
	"context"
	_ "crypto/sha256" // digest algorithms of source-controller
	_ "crypto/sha512"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/opencontainers/go-digest"
	_ "github.com/opencontainers/go-digest/blake3" // digest algorithm of source-controller
)

// PackageLimits bounds what the controller takes in from the package of a
// Release.
type PackageLimits struct {
	// ArtifactSize is the most bytes of the archive that source-controller
	// serves.
	ArtifactSize int64
	// ContentSize is the most bytes that the files of the archive hold.
	ContentSize int64
	// Entries is the most entries of the archive.
	Entries int
}

// DefaultPackageLimits are the defaults of the flags of the controller.
var DefaultPackageLimits = PackageLimits{ArtifactSize: 20 << 20, ContentSize: 40 << 20, Entries: 1000}

// Validate refuses a limit that is not positive, or too large to be counted.
func (l PackageLimits) Validate() error {
	if l.ArtifactSize <= 0 || l.ArtifactSize > math.MaxInt64/2 {
		return fmt.Errorf("the artifact limit must be a positive number of bytes, up to %d", int64(math.MaxInt64/2))
	}
	if l.ContentSize <= 0 || l.ContentSize > math.MaxInt64/2 {
		return fmt.Errorf("the content limit must be a positive number of bytes, up to %d", int64(math.MaxInt64/2))
	}
	if l.Entries <= 0 || l.Entries > maxPackageEntries {
		return fmt.Errorf("the entry limit must be a positive number, up to %d", maxPackageEntries)
	}
	return nil
}

// maxPackageEntries bounds the entry limit, so that the stream limit can be
// counted.
const maxPackageEntries = 1 << 20

// streamLimit is the most bytes of the uncompressed tar stream: the content,
// 4 KiB per entry for its header, a PAX header and their padding, and 1 MiB
// for the end of the archive.
func (l PackageLimits) streamLimit() int64 {
	return l.ContentSize + int64(l.Entries)*(4<<10) + 1<<20
}

// errPackageLimit is matched, with errors.Is, by the refusal of a package
// past a limit. The same artifact is refused the same way.
var errPackageLimit = errors.New("package limit")

type packageLimitError struct {
	msg string
}

func (e *packageLimitError) Error() string {
	return e.msg
}

func (e *packageLimitError) Is(target error) bool {
	return target == errPackageLimit
}

func packageLimitf(format string, args ...interface{}) error {
	return &packageLimitError{msg: fmt.Sprintf(format, args...)}
}

// fetchTimeout bounds a fetch, the retries of its download included.
const fetchTimeout = 5 * time.Minute

// PackageFetcher downloads the package artifacts that source-controller
// serves, checks their digest and extracts them, within its limits.
type PackageFetcher struct {
	limits            PackageLimits
	hostnameOverwrite string
	client            *retryablehttp.Client
	timeout           time.Duration
}

// NewPackageFetcher returns a fetcher that retries a failed download up to
// retries times, 5 to 30 seconds apart unless a Retry-After header asks for
// another wait, and sends it to hostnameOverwrite when set.
func NewPackageFetcher(limits PackageLimits, retries int, hostnameOverwrite string) *PackageFetcher {
	client := retryablehttp.NewClient()
	client.RetryWaitMin = 5 * time.Second
	client.RetryWaitMax = 30 * time.Second
	client.RetryMax = retries
	client.Logger = nil
	return &PackageFetcher{limits: limits, hostnameOverwrite: hostnameOverwrite, client: client, timeout: fetchTimeout}
}

// Fetch downloads the artifact, checks its digest, and extracts it into
// folder. A refusal on a limit matches errPackageLimit.
func (f *PackageFetcher) Fetch(ctx context.Context, artifact *meta.Artifact, folder string) error {
	if artifact.Size != nil && *artifact.Size > f.limits.ArtifactSize {
		return packageLimitf("the package artifact is %d bytes, more than the limit of %d bytes", *artifact.Size, f.limits.ArtifactSize)
	}
	tmp, err := os.MkdirTemp("", "kubocd-package-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	archive := filepath.Join(tmp, "package.tgz")
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	if err := f.download(ctx, artifact, archive); err != nil {
		return err
	}
	return f.extract(ctx, archive, folder)
}

// download writes the artifact into the file archive, and checks its digest.
// It stops reading one byte past the limit.
func (f *PackageFetcher) download(ctx context.Context, artifact *meta.Artifact, archive string) error {
	expected, err := artifactDigest(artifact.Digest)
	if err != nil {
		return err
	}
	address := artifact.URL
	if f.hostnameOverwrite != "" {
		u, err := url.Parse(address)
		if err != nil {
			return fmt.Errorf("invalid artifact URL: %w", err)
		}
		u.Host = f.hostnameOverwrite
		address = u.String()
	}
	request, err := retryablehttp.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return fmt.Errorf("failed to create the request of the artifact: %w", err)
	}
	response, err := f.client.Do(request)
	if err != nil {
		return fmt.Errorf("failed to download the artifact: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download the artifact (status: %s)", response.Status)
	}
	file, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	verifier := expected.Verifier()
	written, err := io.Copy(io.MultiWriter(file, verifier), io.LimitReader(response.Body, f.limits.ArtifactSize+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("failed to download the artifact: %w", err)
	}
	if written > f.limits.ArtifactSize {
		return packageLimitf("the package artifact is more than the limit of %d bytes", f.limits.ArtifactSize)
	}
	if !verifier.Verified() {
		return fmt.Errorf("the digest of the artifact is not '%s'", expected)
	}
	return nil
}

// artifactDigest parses the digest of an artifact, a digest without algorithm
// being sha256.
func artifactDigest(value string) (digest.Digest, error) {
	if value == "" {
		return "", errors.New("the artifact has no digest")
	}
	if !strings.Contains(value, ":") {
		value = "sha256:" + value
	}
	parsed, err := digest.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid digest of the artifact: %w", err)
	}
	return parsed, nil
}

// maxPackageLevels is the most levels of the name of an entry of a package
// archive.
const maxPackageLevels = 32

// extract writes the folders and the files of the gzipped tar archive under
// folder, with the permission bits of their header and, for the files, its
// modification time, capped at the start. A hard link and a global header are
// written as empty files, as their header has the mode of a regular one, and
// a symbolic link is skipped. The folders that a name needs count as entries.
// The tar stream is read up to its limit, which bounds the PAX and GNU headers
// that the tar reader handles without returning them.
func (f *PackageFetcher) extract(ctx context.Context, archive string, folder string) error {
	start := time.Now()
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	unzipped, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("the package archive is not gzipped: %w", err)
	}
	defer func() { _ = unzipped.Close() }()
	if err := os.MkdirAll(folder, 0o750); err != nil {
		return err
	}
	root, err := os.OpenRoot(folder)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	reader := tar.NewReader(&limitedStream{ctx: ctx, reader: unzipped, left: f.limits.streamLimit()})
	folders := map[string]bool{}
	var entries int
	var size int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return f.extractionError(ctx, err)
		}
		entries++
		if !packageEntryName(header.Name) {
			return fmt.Errorf("the package archive holds an entry whose name is not a relative path inside its folder")
		}
		clean := path.Clean(header.Name)
		if strings.Count(clean, "/") >= maxPackageLevels {
			return packageLimitf("the package archive holds a name of more than %d levels", maxPackageLevels)
		}
		needed := path.Dir(clean)
		if header.Typeflag == tar.TypeDir {
			needed = clean
		}
		for dir := needed; dir != "." && !folders[dir]; dir = path.Dir(dir) {
			folders[dir] = true
			if dir != clean {
				entries++
			}
		}
		if entries > f.limits.Entries {
			return packageLimitf("the package archive has more than %d entries", f.limits.Entries)
		}
		name := filepath.FromSlash(clean)
		mode := header.FileInfo().Mode()
		switch header.Typeflag {
		case tar.TypeDir:
			// The owner keeps the access that the cleanup of the folder needs
			if err := root.MkdirAll(name, mode.Perm()|0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeGNUSparse, tar.TypeLink, tar.TypeXGlobalHeader:
			// The size of a sparse file is its logical size, the holes not being in the stream
			if header.Size > f.limits.ContentSize-size {
				return packageLimitf("the files of the package archive hold more than %d bytes", f.limits.ContentSize)
			}
			size += header.Size
			if err := writePackageFile(root, name, mode.Perm(), &contextReader{ctx: ctx, reader: reader}); err != nil {
				return f.extractionError(ctx, err)
			}
			modified := header.ModTime
			if modified.After(start) {
				modified = start
			}
			if !modified.IsZero() {
				if err := root.Chtimes(name, modified, modified); err != nil {
					return err
				}
			}
		case tar.TypeSymlink:
		default:
			return fmt.Errorf("the package archive holds an entry of type '%c', which is not a file nor a folder", header.Typeflag)
		}
	}
}

// extractionError is the error of an extraction that read err: the end of
// its context, a refusal on the stream limit, or an invalid archive.
func (f *PackageFetcher) extractionError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, errPackageLimit) {
		return packageLimitf("the package archive is more than %d bytes once uncompressed", f.limits.streamLimit())
	}
	return fmt.Errorf("the package archive is invalid: %w", err)
}

// packageEntryName tells whether name is a relative slash path without a ..
// component nor a backslash.
func packageEntryName(name string) bool {
	return filepath.IsLocal(name) && !strings.Contains(name, `\`) && !slices.Contains(strings.Split(name, "/"), "..")
}

// writePackageFile writes content to the file called name under root, with the
// permission bits perm, after creating its parent folders.
func writePackageFile(root *os.Root, name string, perm os.FileMode, content io.Reader) error {
	if err := root.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		return err
	}
	// A file written again is removed first: a read-only one cannot be
	// truncated, and darwin keeps the signature of a replaced binary
	if info, err := root.Lstat(name); err == nil && info.Mode().IsRegular() {
		if err := root.Remove(name); err != nil {
			return err
		}
	}
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, content)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

// limitedStream reads from reader up to left bytes, then fails with
// errPackageLimit, and stops with its context.
type limitedStream struct {
	ctx    context.Context
	reader io.Reader
	left   int64
}

func (s *limitedStream) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if s.left > 0 {
		if int64(len(p)) > s.left {
			p = p[:s.left]
		}
		n, err := s.reader.Read(p)
		s.left -= int64(n)
		return n, err
	}
	// A stream of exactly the limit ends there
	var probe [1]byte
	n, err := s.reader.Read(probe[:])
	if n > 0 {
		return 0, &packageLimitError{msg: "package stream limit"}
	}
	return 0, err
}

// contextReader reads from reader until its context ends. The tar reader
// writes the holes of a sparse file without reading its stream.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
