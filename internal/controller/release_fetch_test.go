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

//nolint:goconst // test inputs read better inline
package controller

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/opencontainers/go-digest"
)

type archiveEntry struct {
	header  tar.Header
	content string
}

func fileEntry(name, content string) archiveEntry {
	return archiveEntry{header: tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}, content: content}
}

// gzippedTar returns a tar.gz of the entries, and the sum of the sizes of its
// files.
func gzippedTar(t *testing.T, entries ...archiveEntry) ([]byte, int64) {
	t.Helper()
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(zipped)
	var size int64
	for _, entry := range entries {
		header := entry.header
		if err := archive.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := archive.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
			size += header.Size
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes(), size
}

// rawHeader is a ustar header block of the name, type and size given.
func rawHeader(name string, typeflag byte, size int64) []byte {
	block := make([]byte, 512)
	copy(block[0:], name)
	copy(block[100:], "0000644\x00")
	copy(block[108:], "0000000\x00")
	copy(block[116:], "0000000\x00")
	copy(block[124:], fmt.Sprintf("%011o\x00", size))
	copy(block[136:], fmt.Sprintf("%011o\x00", 0))
	block[156] = typeflag
	copy(block[257:], "ustar\x0000")
	copy(block[148:], "        ")
	sum := 0
	for _, b := range block {
		sum += int(b)
	}
	copy(block[148:], fmt.Sprintf("%06o\x00 ", sum))
	return block
}

// padded pads data to a whole number of tar blocks.
func padded(data []byte) []byte {
	return append(data, make([]byte, (512-len(data)%512)%512)...)
}

// paxRecord formats a PAX record, whose length counts itself.
func paxRecord(key, value string) string {
	body := fmt.Sprintf(" %s=%s\n", key, value)
	length := len(body) + 1
	for len(fmt.Sprint(length))+len(body) != length {
		length++
	}
	return fmt.Sprintf("%d%s", length, body)
}

// sparseArchive returns a tar.gz holding sparse.bin, a PAX 1.0 sparse file of
// the logical size given, whose one byte of data is the last one. Go cannot
// write such a file.
func sparseArchive(t *testing.T, logicalSize int64) []byte {
	t.Helper()
	const name = "sparse.bin"
	records := paxRecord("GNU.sparse.major", "1") + paxRecord("GNU.sparse.minor", "0") + paxRecord("GNU.sparse.name", name) +
		paxRecord("GNU.sparse.realsize", fmt.Sprint(logicalSize))
	sparseMap := padded([]byte(fmt.Sprintf("1\n%d\n1\n", logicalSize-1)))
	stream := make([]byte, 0, 4096)
	stream = append(stream, rawHeader("PaxHeaders.0/"+name, tar.TypeXHeader, int64(len(records)))...)
	stream = append(stream, padded([]byte(records))...)
	stream = append(stream, rawHeader("GNUSparseFile.0/"+name, tar.TypeReg, int64(len(sparseMap)+1))...)
	stream = append(stream, sparseMap...)
	stream = append(stream, padded([]byte{'x'})...)
	stream = append(stream, make([]byte, 1024)...)
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	if _, err := zipped.Write(stream); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// artifactServer serves one body, and counts the requests and the bytes it
// managed to send.
type artifactServer struct {
	*httptest.Server
	mutex    sync.Mutex
	requests int
	sent     int64
}

// serveArtifact serves body, followed by extra bytes of zeros, then waits for
// stall to close when it is not nil.
func serveArtifact(t *testing.T, body []byte, extra int64, stall chan struct{}) *artifactServer {
	t.Helper()
	server := &artifactServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		server.mutex.Lock()
		server.requests++
		server.mutex.Unlock()
		count := func(n int, err error) bool {
			server.mutex.Lock()
			defer server.mutex.Unlock()
			server.sent += int64(n)
			return err == nil
		}
		if !count(w.Write(body)) {
			return
		}
		chunk := make([]byte, 32<<10)
		for left := extra; left > 0; left -= int64(len(chunk)) {
			if !count(w.Write(chunk[:min(int64(len(chunk)), left)])) {
				return
			}
		}
		if stall != nil {
			w.(http.Flusher).Flush()
			<-stall
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func (s *artifactServer) counts() (int, int64) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.requests, s.sent
}

// artifactOf is the artifact of body served by server.
func artifactOf(server *artifactServer, body []byte) *meta.Artifact {
	size := int64(len(body))
	return &meta.Artifact{URL: server.URL + "/package.tgz", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(body)), Size: &size}
}

// openFiles counts the regular files that the process holds open.
func openFiles(t *testing.T) int {
	t.Helper()
	count := 0
	for fd := 0; fd < 4096; fd++ {
		var stat syscall.Stat_t
		if syscall.Fstat(fd, &stat) == nil && stat.Mode&syscall.S_IFMT == syscall.S_IFREG {
			count++
		}
	}
	return count
}

// fetchInto fetches artifact into a new folder, with a temporary folder of its
// own, and checks that the fetch leaves no temporary file and no file open.
func fetchInto(t *testing.T, fetcher *PackageFetcher, artifact *meta.Artifact) (string, error) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	folder := filepath.Join(t.TempDir(), "package")
	before := openFiles(t)
	err := fetcher.Fetch(context.Background(), artifact, folder)
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("the fetch left %d entries in the temporary folder", len(left))
	}
	if after := openFiles(t); after != before {
		t.Errorf("the fetch left %d files open", after-before)
	}
	return folder, err
}

// mustRefuse fails the test unless err is the refusal on a limit given.
func mustRefuse(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want || !errors.Is(err, errPackageLimit) {
		t.Fatalf("got the error %v, want the refusal %q on a limit", err, want)
	}
}

func TestAPackageArchiveWithinItsLimitsIsExtracted(t *testing.T) {
	body, size := gzippedTar(t,
		archiveEntry{header: tar.Header{Name: "charts/", Mode: 0o755, Typeflag: tar.TypeDir}},
		fileEntry("original.yaml", "name: demo\n"),
		fileEntry("charts/demo-1.0.0.tgz", strings.Repeat("c", 3000)),
		fileEntry("empty/folder/inside.txt", ""),
		archiveEntry{header: tar.Header{Name: "link", Linkname: "original.yaml", Typeflag: tar.TypeSymlink}},
		archiveEntry{header: tar.Header{Name: "hard", Linkname: "original.yaml", Typeflag: tar.TypeLink}},
	)
	server := serveArtifact(t, body, 0, nil)
	// Every limit reached exactly: 6 entries, and the folders empty and empty/folder
	fetcher := NewPackageFetcher(PackageLimits{ArtifactSize: int64(len(body)), ContentSize: size, Entries: 8}, 0, "")
	folder, err := fetchInto(t, fetcher, artifactOf(server, body))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"original.yaml": "name: demo\n", "charts/demo-1.0.0.tgz": strings.Repeat("c", 3000),
		"empty/folder/inside.txt": ""} {
		if got, err := os.ReadFile(filepath.Join(folder, name)); err != nil || string(got) != want {
			t.Errorf("got %s with %d bytes (%v), want %d bytes", name, len(got), err, len(want))
		}
	}
	// A hard link is written as an empty file, with the mode of its header
	if info, err := os.Stat(filepath.Join(folder, "hard")); err != nil || info.Size() != 0 || !info.Mode().IsRegular() {
		t.Errorf("got the hard link %v (%v), want an empty file", info, err)
	}
	if _, err := os.Lstat(filepath.Join(folder, "link")); !os.IsNotExist(err) {
		t.Errorf("got the symbolic link (%v), want it skipped", err)
	}
}

func TestAnArtifactDeclaredOverTheLimitIsNotRequested(t *testing.T) {
	body, _ := gzippedTar(t, fileEntry("original.yaml", "name: demo\n"))
	server := serveArtifact(t, body, 0, nil)
	artifact := artifactOf(server, body)
	limits := DefaultPackageLimits
	limits.ArtifactSize = *artifact.Size - 1
	_, err := fetchInto(t, NewPackageFetcher(limits, 0, ""), artifact)
	mustRefuse(t, err, fmt.Sprintf("the package artifact is %d bytes, more than the limit of %d bytes", *artifact.Size, limits.ArtifactSize))
	if requests, _ := server.counts(); requests != 0 {
		t.Fatalf("got %d requests, want none", requests)
	}
}

func TestADownloadPastTheLimitIsCutShort(t *testing.T) {
	body, _ := gzippedTar(t, fileEntry("original.yaml", "name: demo\n"))
	const extra = 64 << 20
	understated := int64(10)
	for name, size := range map[string]*int64{"size absent": nil, "size understated": &understated} {
		t.Run(name, func(t *testing.T) {
			server := serveArtifact(t, body, extra, nil)
			artifact := artifactOf(server, body)
			artifact.Size = size
			limits := DefaultPackageLimits
			limits.ArtifactSize = 64 << 10
			folder, err := fetchInto(t, NewPackageFetcher(limits, 0, ""), artifact)
			mustRefuse(t, err, "the package artifact is more than the limit of 65536 bytes")
			if _, err := os.Stat(folder); !os.IsNotExist(err) {
				t.Errorf("got the folder (%v), want nothing extracted", err)
			}
			server.CloseClientConnections()
			if _, sent := server.counts(); sent >= int64(len(body))+extra {
				t.Errorf("the server sent the whole body, %d bytes, want the download stopped", sent)
			}
		})
	}
}

func TestAStalledDownloadEndsAtTheDeadline(t *testing.T) {
	body, _ := gzippedTar(t, fileEntry("original.yaml", "name: demo\n"))
	stall := make(chan struct{})
	server := serveArtifact(t, body[:len(body)/2], 0, stall)
	t.Cleanup(func() { close(stall) })
	fetcher := NewPackageFetcher(DefaultPackageLimits, 0, "")
	fetcher.timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := fetchInto(t, fetcher, artifactOf(server, body))
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") || errors.Is(err, errPackageLimit) {
		t.Fatalf("got the error %v, want the deadline, not a limit", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the fetch took %s", elapsed)
	}
}

func TestAnArtifactOfAnotherDigestIsRefused(t *testing.T) {
	body, _ := gzippedTar(t, fileEntry("original.yaml", "name: demo\n"))
	server := serveArtifact(t, body, 0, nil)
	artifact := artifactOf(server, body)
	artifact.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("another")))
	_, err := fetchInto(t, NewPackageFetcher(DefaultPackageLimits, 0, ""), artifact)
	if err == nil || !strings.Contains(err.Error(), "the digest of the artifact is not") || errors.Is(err, errPackageLimit) {
		t.Fatalf("got the error %v, want the digest refused", err)
	}
}

func TestEveryDigestAlgorithmOfSourceControllerIsChecked(t *testing.T) {
	body, _ := gzippedTar(t, fileEntry("original.yaml", "name: demo\n"))
	server := serveArtifact(t, body, 0, nil)
	fetcher := NewPackageFetcher(DefaultPackageLimits, 0, "")
	sum := sha256.Sum256(body)
	for name, value := range map[string]string{
		"sha256":           digest.SHA256.FromBytes(body).String(),
		"sha256, no label": fmt.Sprintf("%x", sum),
		"sha384":           digest.SHA384.FromBytes(body).String(),
		"sha512":           digest.SHA512.FromBytes(body).String(),
		"blake3":           digest.Algorithm("blake3").FromBytes(body).String(),
	} {
		artifact := artifactOf(server, body)
		artifact.Digest = value
		if _, err := fetchInto(t, fetcher, artifact); err != nil {
			t.Errorf("%s: got the error %v", name, err)
		}
	}
	for name, value := range map[string]string{"empty": "", "malformed": "sha256:xyz", "unknown": "md5:0123456789abcdef"} {
		artifact := artifactOf(server, body)
		artifact.Digest = value
		if _, err := fetchInto(t, fetcher, artifact); err == nil || errors.Is(err, errPackageLimit) {
			t.Errorf("%s: got the error %v, want the digest refused", name, err)
		}
	}
}

func TestADownloadIsRetriedOnAServerErrorOnly(t *testing.T) {
	body, _ := gzippedTar(t, fileEntry("original.yaml", "name: demo\n"))
	for name, c := range map[string]struct {
		first    int
		succeeds bool
		requests int
	}{"503 then 200": {http.StatusServiceUnavailable, true, 2}, "404": {http.StatusNotFound, false, 1}} {
		t.Run(name, func(t *testing.T) {
			var mutex sync.Mutex
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mutex.Lock()
				requests++
				first := requests == 1
				mutex.Unlock()
				if first || c.first == http.StatusNotFound {
					w.WriteHeader(c.first)
					return
				}
				_, _ = w.Write(body)
			}))
			t.Cleanup(server.Close)
			fetcher := NewPackageFetcher(DefaultPackageLimits, 3, "")
			fetcher.client.RetryWaitMin, fetcher.client.RetryWaitMax = time.Millisecond, time.Millisecond
			_, err := fetchInto(t, fetcher, artifactOf(&artifactServer{Server: server}, body))
			mutex.Lock()
			defer mutex.Unlock()
			if (err == nil) != c.succeeds || requests != c.requests {
				t.Fatalf("got the error %v after %d requests, want success %t after %d", err, requests, c.succeeds, c.requests)
			}
		})
	}
}

func TestTheHostnameOverrideKeepsThePathAndTheQuery(t *testing.T) {
	body, _ := gzippedTar(t, fileEntry("original.yaml", "name: demo\n"))
	var requested string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.RequestURI()
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	artifact := artifactOf(&artifactServer{Server: server}, body)
	artifact.URL = "http://source-controller.flux-system.svc.cluster.local./ocirepository/apps/kcd-demo/abc.tar.gz?x=1"
	if _, err := fetchInto(t, NewPackageFetcher(DefaultPackageLimits, 0, strings.TrimPrefix(server.URL, "http://")), artifact); err != nil {
		t.Fatal(err)
	}
	if requested != "/ocirepository/apps/kcd-demo/abc.tar.gz?x=1" {
		t.Fatalf("got the request %q", requested)
	}
}

func TestAPackageArchiveKeepsTheModesAndTimesOfItsEntries(t *testing.T) {
	past := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	body, _ := gzippedTar(t,
		archiveEntry{header: tar.Header{Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "global"}}},
		archiveEntry{header: tar.Header{Name: "locked/", Mode: 0o000, Typeflag: tar.TypeDir}},
		archiveEntry{header: tar.Header{Name: "locked/file", Mode: 0o640, Size: 1, Typeflag: tar.TypeReg, ModTime: past}, content: "f"},
		archiveEntry{header: tar.Header{Name: "tool", Mode: 0o750, Size: 1, Typeflag: tar.TypeReg, ModTime: time.Now().Add(time.Hour)},
			content: "t"},
	)
	before := time.Now()
	folder, err := fetchInto(t, NewPackageFetcher(DefaultPackageLimits, 0, ""), artifactOf(serveArtifact(t, body, 0, nil), body))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{"locked": os.ModeDir | 0o700, "locked/file": 0o640, "tool": 0o750} {
		if info, err := os.Stat(filepath.Join(folder, name)); err != nil || info.Mode() != want {
			t.Errorf("got %s with the mode %v (%v), want %v", name, info.Mode(), err, want)
		}
	}
	if info, err := os.Stat(filepath.Join(folder, "locked/file")); err != nil || !info.ModTime().Equal(past) {
		t.Errorf("got the time %v (%v), want %v", info.ModTime(), err, past)
	}
	if info, err := os.Stat(filepath.Join(folder, "tool")); err != nil || info.ModTime().Before(before.Add(-time.Second)) ||
		info.ModTime().After(time.Now()) {
		t.Errorf("got the time %v (%v), want the start of the extraction", info.ModTime(), err)
	}
	// A global header is written as an empty file, with the mode of its header
	if info, err := os.Stat(filepath.Join(folder, "GlobalHead.0.0")); err != nil || info.Size() != 0 || info.Mode() != 0 {
		t.Errorf("got the global header %v (%v), want an empty file", info, err)
	}
	if err := os.RemoveAll(folder); err != nil {
		t.Errorf("got %v when the folder is removed", err)
	}
}

func TestASmallSparseFileIsWrittenWhole(t *testing.T) {
	body := sparseArchive(t, 4096)
	folder, err := fetchInto(t, NewPackageFetcher(DefaultPackageLimits, 0, ""), artifactOf(serveArtifact(t, body, 0, nil), body))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(folder, "sparse.bin"))
	if want := append(make([]byte, 4095), 'x'); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %d bytes (%v), want 4095 zeros and the byte of data", len(got), err)
	}
}

func TestPackageArchivesAtTheEdgeOfTheLimits(t *testing.T) {
	small := PackageLimits{ArtifactSize: 20 << 20, ContentSize: 512, Entries: 1}
	exact, _ := gzippedTar(t, fileEntry("one", strings.Repeat("a", 512)))
	// The same name twice counts twice
	twice, _ := gzippedTar(t, fileEntry("a", strings.Repeat("a", 40<<10)), fileEntry("a", strings.Repeat("b", 40<<10)))
	// Data after the end of the tar stream is not read
	trailing := append(append([]byte{}, exact...), gzippedBytes(t, bytes.Repeat([]byte("garbage"), 1<<16))...)
	cases := map[string]struct {
		limits  PackageLimits
		body    []byte
		refusal string
	}{
		"512 bytes in one entry": {small, exact, ""},
		"the same name twice":    {PackageLimits{ArtifactSize: 20 << 20, ContentSize: 64 << 10, Entries: 10}, twice, "the files of the package archive hold more than 65536 bytes"},
		"data after the end":     {small, trailing, ""},
		"the limit between entries": {PackageLimits{ArtifactSize: 20 << 20, ContentSize: 1024, Entries: 2}, streamOfTheLimit(t),
			"the package archive is more than 1057792 bytes once uncompressed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			folder, err := fetchInto(t, NewPackageFetcher(c.limits, 0, ""), artifactOf(serveArtifact(t, c.body, 0, nil), c.body))
			if c.refusal == "" {
				if err != nil {
					t.Fatalf("got the error %v", err)
				}
				if got, err := os.ReadFile(filepath.Join(folder, "one")); err != nil || len(got) != 512 {
					t.Fatalf("got %d bytes (%v)", len(got), err)
				}
				return
			}
			mustRefuse(t, err, c.refusal)
		})
	}
}

// gzippedBytes returns data, gzipped.
func gzippedBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	if _, err := zipped.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// streamOfTheLimit returns an archive whose two first entries, of 512 bytes
// each with a PAX header of 527360 bytes, take exactly the stream limit of a
// content limit of 1024 bytes and 2 entries, and which holds a third one.
func streamOfTheLimit(t *testing.T) []byte {
	t.Helper()
	// A record of 527360 bytes: its length, a space, the key, =, the value and a newline
	padding := map[string]string{"KUBOCD.padding": strings.Repeat("p", 527360-len("527360 KUBOCD.padding=\n"))}
	body, _ := gzippedTar(t,
		archiveEntry{header: tar.Header{Name: "a", Mode: 0o644, Size: 512, Typeflag: tar.TypeReg, PAXRecords: padding}, content: strings.Repeat("a", 512)},
		archiveEntry{header: tar.Header{Name: "b", Mode: 0o644, Size: 512, Typeflag: tar.TypeReg, PAXRecords: padding}, content: strings.Repeat("b", 512)},
		fileEntry("c", "c"))
	return body
}

// sparseAfterAFile returns an archive with a file of 100 bytes, then a sparse
// file of a logical size close to the largest int64.
func sparseAfterAFile(t *testing.T) []byte {
	t.Helper()
	file, _ := gzippedTar(t, fileEntry("file", strings.Repeat("f", 100)))
	unzipped, err := gzip.NewReader(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	if _, err := stream.ReadFrom(unzipped); err != nil {
		t.Fatal(err)
	}
	sparse, err := gzip.NewReader(bytes.NewReader(sparseArchive(t, 1<<63-51)))
	if err != nil {
		t.Fatal(err)
	}
	var tail bytes.Buffer
	if _, err := tail.ReadFrom(sparse); err != nil {
		t.Fatal(err)
	}
	// The end of the first archive, two zero blocks, goes
	return gzippedBytes(t, append(stream.Bytes()[:stream.Len()-1024], tail.Bytes()...))
}

// cancelledAfter is a context that its call n of Err, and the next ones, find
// cancelled.
type cancelledAfter struct {
	context.Context
	calls, n int
}

func (c *cancelledAfter) Err() error {
	c.calls++
	if c.calls >= c.n {
		return context.Canceled
	}
	return nil
}

// extractInto extracts body into a new folder with the limits and the context
// given, and returns the folder.
func extractInto(t *testing.T, ctx context.Context, limits PackageLimits, body []byte) (string, error) {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "package.tgz")
	if err := os.WriteFile(archive, body, 0o600); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "package")
	before := openFiles(t)
	err := NewPackageFetcher(limits, 0, "").extract(ctx, archive, folder)
	if after := openFiles(t); after != before {
		t.Errorf("the extraction left %d files open", after-before)
	}
	return folder, err
}

func TestTheSizesOfAnArchiveAreAddedWithoutOverflow(t *testing.T) {
	// The context stops a copy of the sparse file within 64 reads, should the content limit let it through
	limits := PackageLimits{ArtifactSize: 20 << 20, ContentSize: 1024, Entries: 10}
	_, err := extractInto(t, &cancelledAfter{Context: context.Background(), n: 64}, limits, sparseAfterAFile(t))
	mustRefuse(t, err, "the files of the package archive hold more than 1024 bytes")
}

func TestTheFoldersOfANameCountAsEntries(t *testing.T) {
	limits := PackageLimits{ArtifactSize: 20 << 20, ContentSize: 1024, Entries: 3}
	within, _ := gzippedTar(t, fileEntry("a/b/file", "x"))
	if _, err := extractInto(t, context.Background(), limits, within); err != nil {
		t.Fatalf("got the error %v for two folders and a file", err)
	}
	past, _ := gzippedTar(t, fileEntry("a/b/c/file", "x"))
	folder, err := extractInto(t, context.Background(), limits, past)
	mustRefuse(t, err, "the package archive has more than 3 entries")
	if _, err := os.Stat(filepath.Join(folder, "a")); !os.IsNotExist(err) {
		t.Errorf("got the folder a (%v), want nothing created", err)
	}
	deep, _ := gzippedTar(t, fileEntry(strings.Repeat("d/", 32)+"file", "x"))
	folder, err = extractInto(t, context.Background(), DefaultPackageLimits, deep)
	mustRefuse(t, err, "the package archive holds a name of more than 32 levels")
	if _, err := os.Stat(filepath.Join(folder, "d")); !os.IsNotExist(err) {
		t.Errorf("got the folder d (%v), want nothing created", err)
	}
	if _, err := extractInto(t, context.Background(), DefaultPackageLimits, gzippedTarOf(t, strings.Repeat("d/", 31)+"file")); err != nil {
		t.Errorf("got the error %v for 32 levels", err)
	}
}

// gzippedTarOf returns a tar.gz of one file of the name given.
func gzippedTarOf(t *testing.T, name string) []byte {
	t.Helper()
	body, _ := gzippedTar(t, fileEntry(name, "x"))
	return body
}

func TestAnExtractionStopsWithItsContextInsideAnEntry(t *testing.T) {
	// The tenth check of the context falls in the copy of the holes of the sparse file
	folder, err := extractInto(t, &cancelledAfter{Context: context.Background(), n: 10}, DefaultPackageLimits, sparseArchive(t, 16<<20))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got the error %v, want the cancellation", err)
	}
	if info, err := os.Stat(filepath.Join(folder, "sparse.bin")); err != nil || info.Size() == 0 || info.Size() >= 16<<20 {
		t.Fatalf("got the sparse file %v (%v), want its copy begun and stopped", info, err)
	}
	// The fourth check falls in the reading of a PAX header, before the file
	padding := map[string]string{"KUBOCD.padding": strings.Repeat("p", 900<<10)}
	metadata, _ := gzippedTar(t, archiveEntry{header: tar.Header{Name: "a", Mode: 0o644, Typeflag: tar.TypeReg, PAXRecords: padding}})
	folder, err = extractInto(t, &cancelledAfter{Context: context.Background(), n: 4}, DefaultPackageLimits, metadata)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got the error %v, want the cancellation", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "a")); !os.IsNotExist(err) {
		t.Fatalf("got the file a (%v), want the extraction stopped in its header", err)
	}
}

func TestAFailedCopyLeavesNoFileOpen(t *testing.T) {
	// A file whose header announces 1000 bytes, and whose stream ends after 500
	truncated := append(rawHeader("short", tar.TypeReg, 1000), bytes.Repeat([]byte("s"), 500)...)
	// Two files, the second one crossing the stream limit of 64 KiB, 2 entries: 1122304 bytes
	first := map[string]string{"KUBOCD.padding": strings.Repeat("p", 900<<10)}
	second := map[string]string{"KUBOCD.padding": strings.Repeat("q", 150000)}
	crossing, _ := gzippedTar(t,
		archiveEntry{header: tar.Header{Name: "a", Mode: 0o644, Size: 512, Typeflag: tar.TypeReg, PAXRecords: first}, content: strings.Repeat("a", 512)},
		archiveEntry{header: tar.Header{Name: "b", Mode: 0o644, Size: 65024, Typeflag: tar.TypeReg, PAXRecords: second}, content: strings.Repeat("b", 65024)})
	cases := map[string]struct {
		limits PackageLimits
		body   []byte
		want   string
	}{
		"content cut short": {DefaultPackageLimits, gzippedBytes(t, truncated), "the package archive is invalid: unexpected EOF"},
		"the stream limit within a file": {PackageLimits{ArtifactSize: 20 << 20, ContentSize: 64 << 10, Entries: 2}, crossing,
			"the package archive is more than 1122304 bytes once uncompressed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := fetchInto(t, NewPackageFetcher(c.limits, 0, ""), artifactOf(serveArtifact(t, c.body, 0, nil), c.body))
			if err == nil || err.Error() != c.want {
				t.Fatalf("got the error %v, want %q", err, c.want)
			}
		})
	}
}

func TestAFileWrittenAgainReplacesTheFormerOne(t *testing.T) {
	body, _ := gzippedTar(t,
		archiveEntry{header: tar.Header{Name: "tool", Mode: 0o555, Size: 5, Typeflag: tar.TypeReg}, content: "first"},
		archiveEntry{header: tar.Header{Name: "tool", Mode: 0o555, Size: 6, Typeflag: tar.TypeReg}, content: "second"})
	folder, err := extractInto(t, context.Background(), DefaultPackageLimits, body)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(folder, "tool")); err != nil || string(got) != "second" {
		t.Fatalf("got %q (%v), want the second content", got, err)
	}
}

func TestAnExtractionStopsWithItsContext(t *testing.T) {
	body, _ := gzippedTar(t, fileEntry("original.yaml", "name: demo\n"))
	archive := filepath.Join(t.TempDir(), "package.tgz")
	if err := os.WriteFile(archive, body, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	folder := filepath.Join(t.TempDir(), "package")
	if err := NewPackageFetcher(DefaultPackageLimits, 0, "").extract(ctx, archive, folder); !errors.Is(err, context.Canceled) {
		t.Fatalf("got the error %v, want the cancellation", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "original.yaml")); !os.IsNotExist(err) {
		t.Fatalf("got the file (%v), want nothing extracted", err)
	}
}

func TestAPackageArchivePastAnExtractionLimitIsRefused(t *testing.T) {
	limits := PackageLimits{ArtifactSize: 20 << 20, ContentSize: 64 << 10, Entries: 1000}
	var files, folders []archiveEntry
	for i := 0; i <= limits.Entries; i++ {
		files = append(files, fileEntry(fmt.Sprintf("f%04d", i), "x"))
		folders = append(folders, archiveEntry{header: tar.Header{Name: fmt.Sprintf("d%04d/", i), Mode: 0o755, Typeflag: tar.TypeDir}})
	}
	padding := map[string]string{"KUBOCD.padding": strings.Repeat("p", 900<<10)}
	// 64 KiB, 10 entries: a stream limit of 1155072 bytes
	fewEntries := PackageLimits{ArtifactSize: 20 << 20, ContentSize: 64 << 10, Entries: 10}
	cases := map[string]struct {
		limits  PackageLimits
		body    func(t *testing.T) []byte
		refusal string
		absent  string
	}{
		"1001 files": {limits, func(t *testing.T) []byte { b, _ := gzippedTar(t, files...); return b },
			"the package archive has more than 1000 entries", "f1000"},
		"1001 folders": {limits, func(t *testing.T) []byte { b, _ := gzippedTar(t, folders...); return b },
			"the package archive has more than 1000 entries", "d1000"},
		"files past the content limit": {limits, func(t *testing.T) []byte {
			b, _ := gzippedTar(t, fileEntry("a", strings.Repeat("a", 32<<10)), fileEntry("b", strings.Repeat("b", 32<<10+1)))
			return b
		}, "the files of the package archive hold more than 65536 bytes", "b"},
		"a sparse file past the content limit": {limits, func(t *testing.T) []byte { return sparseArchive(t, 16<<20) },
			"the files of the package archive hold more than 65536 bytes", "sparse.bin"},
		"headers past the stream limit": {fewEntries, func(t *testing.T) []byte {
			b, _ := gzippedTar(t,
				archiveEntry{header: tar.Header{Name: "a", Mode: 0o644, Typeflag: tar.TypeReg, PAXRecords: padding}},
				archiveEntry{header: tar.Header{Name: "b", Mode: 0o644, Typeflag: tar.TypeReg, PAXRecords: padding}})
			return b
		}, "the package archive is more than 1155072 bytes once uncompressed", "b"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			body := c.body(t)
			fetcher := NewPackageFetcher(c.limits, 0, "")
			folder, err := fetchInto(t, fetcher, artifactOf(serveArtifact(t, body, 0, nil), body))
			mustRefuse(t, err, c.refusal)
			if _, err := os.Lstat(filepath.Join(folder, c.absent)); !os.IsNotExist(err) {
				t.Errorf("got %s (%v), want the extraction stopped before it", c.absent, err)
			}
			// The same fetcher then extracts a valid archive
			valid, _ := gzippedTar(t, fileEntry("original.yaml", "name: demo\n"))
			if _, err := fetchInto(t, fetcher, artifactOf(serveArtifact(t, valid, 0, nil), valid)); err != nil {
				t.Fatalf("got the error %v on a valid archive after the refusal", err)
			}
		})
	}
}

func TestAPackageArchiveWithAnEntryOutsideItsFolderIsRefused(t *testing.T) {
	inside := archiveEntry{header: tar.Header{Name: "inside/", Mode: 0o755, Typeflag: tar.TypeDir}}
	cases := map[string][]archiveEntry{
		"parent":        {fileEntry("../escaped", "x")},
		"absolute":      {fileEntry("/escaped", "x")},
		"parent inside": {inside, fileEntry("inside/../escaped", "x")},
		"backslash":     {inside, fileEntry(`inside\escaped`, "x")},
		"device":        {{header: tar.Header{Name: "device", Mode: 0o644, Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			body, _ := gzippedTar(t, entries...)
			folder, err := fetchInto(t, NewPackageFetcher(DefaultPackageLimits, 0, ""), artifactOf(serveArtifact(t, body, 0, nil), body))
			if err == nil || errors.Is(err, errPackageLimit) {
				t.Fatalf("got the error %v, want the entry refused", err)
			}
			if _, err := os.Lstat(filepath.Join(filepath.Dir(folder), "escaped")); !os.IsNotExist(err) {
				t.Errorf("got a file outside the folder (%v)", err)
			}
		})
	}
}

func TestARefusedPackageArchiveIsReportedAndNotRetried(t *testing.T) {
	var entries []archiveEntry
	for i := 0; i <= DefaultPackageLimits.Entries; i++ {
		entries = append(entries, fileEntry(fmt.Sprintf("f%04d", i), "x"))
	}
	body, _ := gzippedTar(t, entries...)
	writes := &helmRepositoryWrites{}
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext, coldFolder(t),
		servedArchive(t, body), countingHelmRepositoryWrites(writes))
	got := testbed.reconcile(t)
	want := "unable to fetch artifact: the package archive has more than 1000 entries"
	if got.status != want || got.err != nil || !got.result.IsZero() || phaseOf(t, testbed) != kv1alpha1.ReleasePhaseError {
		t.Fatalf("got the phase %q, the status %q, the error %v and the result %+v, want ERROR, the status %q, no error and no requeue",
			phaseOf(t, testbed), got.status, got.err, got.result, want)
	}
	left, err := os.ReadDir(filepath.Join(testbed.reconciler.ServerRoot, "hr", "apps", "demo"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(left))
	for _, entry := range left {
		names = append(names, entry.Name())
	}
	if !slices.Equal(names, []string{".transient"}) {
		t.Errorf("got %d entries in the folder, want it emptied", len(names))
	}
	if deletes, patches := writes.counts(); deletes != 0 || patches != 0 {
		t.Errorf("got %d deletions and %d patches of the HelmRepository, want none", deletes, patches)
	}
}

func TestPackageLimitsMustBePositive(t *testing.T) {
	for name, limits := range map[string]PackageLimits{
		"artifact":           {ArtifactSize: 0, ContentSize: 1, Entries: 1, ManifestSize: 1},
		"content":            {ArtifactSize: 1, ContentSize: -1, Entries: 1, ManifestSize: 1},
		"entries":            {ArtifactSize: 1, ContentSize: 1, Entries: 0, ManifestSize: 1},
		"entries too many":   {ArtifactSize: 1, ContentSize: 1, Entries: maxPackageEntries + 1, ManifestSize: 1},
		"artifact too large": {ArtifactSize: 1 << 62, ContentSize: 1, Entries: 1, ManifestSize: 1},
		"manifest":           {ArtifactSize: 1, ContentSize: 1, Entries: 1, ManifestSize: 0},
	} {
		if err := limits.Validate(); err == nil {
			t.Errorf("%s: got no error, want the limits refused", name)
		}
	}
	if err := DefaultPackageLimits.Validate(); err != nil {
		t.Errorf("got %v on the default limits", err)
	}
}

// manifestOf pads manifest with a YAML comment to size bytes.
func manifestOf(t *testing.T, manifest string, size int) string {
	t.Helper()
	padding := size - len(manifest) - len("#\n")
	if padding < 0 {
		t.Fatalf("the manifest is longer than %d bytes", size)
	}
	return manifest + "#" + strings.Repeat("x", padding) + "\n"
}

func TestAPackageManifestLoadsUpToTheLimitAndNoMore(t *testing.T) {
	limits := DefaultPackageLimits
	limits.ManifestSize = 4096
	fetcher := NewPackageFetcher(limits, 0, "")
	cases := map[string]struct {
		manifest string
		target   func() interface{}
	}{
		"original.yaml": {"apiVersion: v1alpha1\nname: demo\ntag: 0.1.0\n", func() interface{} { return &kubopackage.Package{} }},
		"status.yaml":   {"apiVersion: v1alpha1\nchartByModule:\n  main: {name: demo, version: 1.0.0}\n", func() interface{} { return &kubopackage.Status{} }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			folder := t.TempDir()
			write := func(content string) {
				if err := os.WriteFile(filepath.Join(folder, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(manifestOf(t, c.manifest, 4096))
			target := c.target()
			if size, err := fetcher.LoadFile(folder, name, target); err != nil || size != 4096 {
				t.Fatalf("got %d bytes and the error %v, want the manifest of the limit loaded", size, err)
			}
			if reflect.ValueOf(target).Elem().IsZero() {
				t.Fatalf("got nothing decoded from %s", name)
			}
			for _, oversized := range []string{manifestOf(t, c.manifest, 4097), strings.Repeat("\x00", 4097)} {
				write(oversized)
				target = c.target()
				_, err := fetcher.LoadFile(folder, name, target)
				mustRefuse(t, err, "the file is more than the limit of 4096 bytes")
				if !reflect.ValueOf(target).Elem().IsZero() {
					t.Fatalf("got %s decoded past the limit", name)
				}
			}
		})
	}
}

func TestAPackageManifestPastTheLimitIsReported(t *testing.T) {
	manifest := manifestOf(t, fmt.Sprintf(afterRenderPackage, "", ""), int(DefaultPackageLimits.ManifestSize)+1)
	got := reconcileRelease(t, manifest, "", afterRenderContext)
	want := "error while parsing package original.yaml file: the file is more than the limit of 1048576 bytes"
	if got.status != want || got.err != nil || !got.result.IsZero() {
		t.Fatalf("got the status %q, the error %v and the result %+v, want the status %q, no error and no requeue", got.status,
			got.err, got.result, want)
	}
}

type countedReader struct {
	reader io.Reader
	read   int64
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	return n, err
}

func TestAManifestPastTheLimitIsNotReadWhole(t *testing.T) {
	counted := &countedReader{reader: strings.NewReader(manifestOf(t, "name: demo\n", 1<<20))}
	_, err := decodeManifest(counted, 4096, &kubopackage.Package{})
	mustRefuse(t, err, "the file is more than the limit of 4096 bytes")
	if counted.read > 4097 {
		t.Fatalf("got %d bytes read, want the read stopped one byte past the limit", counted.read)
	}
}
