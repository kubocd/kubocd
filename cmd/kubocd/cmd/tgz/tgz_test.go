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

package tgz

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	refusedEntry = "is not a relative path inside the target folder"
	chartYaml    = "name: mychart\n"
	// folder stands for a directory in the result of tree.
	folder = "dir"
)

// entry is a tar entry, a regular file unless typeflag says otherwise.
type entry struct {
	name     string
	typeflag byte
	linkname string
	body     string
}

func regular(name string, body string) entry {
	return entry{name: name, typeflag: tar.TypeReg, body: body}
}

// writeTgz writes the entries as a gzipped tar archive, in a folder of its own.
func writeTgz(t *testing.T, entries ...entry) string {
	t.Helper()
	buf := &bytes.Buffer{}
	gzWriter := gzip.NewWriter(buf)
	tarWriter := tar.NewWriter(gzWriter)
	for _, e := range entries {
		header := &tar.Header{Name: e.name, Typeflag: e.typeflag, Linkname: e.linkname, Mode: 0o644}
		if e.typeflag == tar.TypeReg {
			header.Size = int64(len(e.body))
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("could not write the header of %q: %v", e.name, err)
		}
		if _, err := tarWriter.Write([]byte(e.body)); err != nil {
			t.Fatalf("could not write the content of %q: %v", e.name, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzWriter.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "archive.tgz")
	if err := os.WriteFile(archive, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return archive
}

// sandbox creates a folder holding outside/victim.txt. It returns this folder and the path of the target
// folder beside outside, not created yet.
func sandbox(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "outside", "victim.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	return base, filepath.Join(base, "target")
}

// tree describes every path under dir except the skipped folder, by its path relative to dir: folder for a
// directory, the destination of a symbolic link, or the content of a file.
func tree(t *testing.T, dir string, skipped string) map[string]string {
	t.Helper()
	paths := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == skipped {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			dest, err := os.Readlink(p)
			if err != nil {
				return err
			}
			paths[rel] = "link to " + dest
		case d.IsDir():
			paths[rel] = folder
		default:
			content, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			paths[rel] = string(content)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestExtractAllFromTgzWritesNestedFiles(t *testing.T) {
	target := filepath.Join(t.TempDir(), "charts", "main")
	archive := writeTgz(t,
		entry{name: "mychart/", typeflag: tar.TypeDir},
		regular("mychart/Chart.yaml", chartYaml),
		regular("mychart/templates/deployment.yaml", "kind: Deployment\n"),
		regular("mychart/templates/tests/test.yaml", "kind: Pod\n"),
		regular("./README.md", "readme\n"),
	)
	if err := ExtractAllFromTgz(archive, target); err != nil {
		t.Fatalf("extraction failed: %v", err)
	}
	want := map[string]string{
		".":                                 folder,
		"README.md":                         "readme\n",
		"mychart":                           folder,
		"mychart/Chart.yaml":                chartYaml,
		"mychart/templates":                 folder,
		"mychart/templates/deployment.yaml": "kind: Deployment\n",
		"mychart/templates/tests":           folder,
		"mychart/templates/tests/test.yaml": "kind: Pod\n",
	}
	if got := tree(t, target, ""); !reflect.DeepEqual(got, want) {
		t.Errorf("extracted %v, want %v", got, want)
	}
}

func TestExtractAllFromTgzRefusesNamesOutsideTarget(t *testing.T) {
	entries := map[string]entry{
		"parent":                regular("../escaped.txt", "overwritten"),
		"parent of outside":     regular("../outside/victim.txt", "overwritten"),
		"nested parent":         regular("mychart/../../escaped.txt", "overwritten"),
		"absolute":              regular("{base}/outside/victim.txt", "overwritten"),
		"windows parent":        regular(`..\escaped.txt`, "overwritten"),
		"windows nested parent": regular(`mychart\..\..\escaped.txt`, "overwritten"),
		"windows drive":         regular(`C:\escaped.txt`, "overwritten"),
		"windows rooted":        regular(`\escaped.txt`, "overwritten"),
		"windows unc":           regular(`\\server\share\escaped.txt`, "overwritten"),
		"link":                  {name: "../escaped-link", typeflag: tar.TypeSymlink, linkname: "outside"},
		"folder":                {name: "../escaped-folder/", typeflag: tar.TypeDir},
	}
	for title, e := range entries {
		t.Run(title, func(t *testing.T) {
			base, target := sandbox(t)
			e.name = strings.ReplaceAll(e.name, "{base}", filepath.ToSlash(base))
			archive := writeTgz(t, regular("mychart/Chart.yaml", chartYaml), e)
			before := tree(t, base, target)
			err := ExtractAllFromTgz(archive, target)
			if err == nil || !strings.Contains(err.Error(), refusedEntry) {
				t.Errorf("got error %v, want one saying %q", err, refusedEntry)
			}
			if after := tree(t, base, target); !reflect.DeepEqual(after, before) {
				t.Errorf("outside of the target folder, got %v, want %v", after, before)
			}
		})
	}
}

func TestExtractAllFromTgzSkipsLinks(t *testing.T) {
	base, target := sandbox(t)
	outside := filepath.Join(base, "outside")
	archive := writeTgz(t,
		entry{name: "mychart/escape", typeflag: tar.TypeSymlink, linkname: outside},
		entry{name: "mychart/up", typeflag: tar.TypeSymlink, linkname: "../../outside"},
		entry{name: "mychart/hard", typeflag: tar.TypeLink, linkname: filepath.Join(outside, "victim.txt")},
		regular("mychart/escape/victim.txt", "overwritten"),
		regular("mychart/Chart.yaml", chartYaml),
	)
	before := tree(t, base, target)
	if err := ExtractAllFromTgz(archive, target); err != nil {
		t.Fatalf("extraction failed: %v", err)
	}
	want := map[string]string{
		".":                         folder,
		"mychart":                   folder,
		"mychart/Chart.yaml":        chartYaml,
		"mychart/escape":            folder,
		"mychart/escape/victim.txt": "overwritten",
	}
	if got := tree(t, target, ""); !reflect.DeepEqual(got, want) {
		t.Errorf("extracted %v, want %v", got, want)
	}
	if after := tree(t, base, target); !reflect.DeepEqual(after, before) {
		t.Errorf("outside of the target folder, got %v, want %v", after, before)
	}
}

func TestExtractAllFromTgzStaysInsideThroughExistingSymlinks(t *testing.T) {
	names := map[string]string{
		"new file under an absolute link":      "escape/new.txt",
		"existing file under an absolute link": "escape/victim.txt",
		"existing file under a relative link":  "up/victim.txt",
		"absolute link to a file":              "victim-link",
		"relative link to a file":              "victim-up",
	}
	for title, name := range names {
		t.Run(title, func(t *testing.T) {
			base, target := sandbox(t)
			outside := filepath.Join(base, "outside")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			links := map[string]string{
				"escape":      outside,
				"up":          "../outside",
				"victim-link": filepath.Join(outside, "victim.txt"),
				"victim-up":   "../outside/victim.txt",
			}
			for link, dest := range links {
				if err := os.Symlink(dest, filepath.Join(target, link)); err != nil {
					t.Fatal(err)
				}
			}
			archive := writeTgz(t, regular(name, "overwritten"))
			before := tree(t, base, target)
			if err := ExtractAllFromTgz(archive, target); err == nil {
				t.Errorf("extraction through a link out of the target folder succeeded")
			}
			if after := tree(t, base, target); !reflect.DeepEqual(after, before) {
				t.Errorf("outside of the target folder, got %v, want %v", after, before)
			}
		})
	}
}

func TestExtractDataFromTgzFindsPrefixedChartYaml(t *testing.T) {
	archive := writeTgz(t,
		regular("mychart/Chart.yaml", chartYaml),
		regular("mychart/values.yaml", "replicas: 1\n"),
		regular("mychart/templates/deployment.yaml", "kind: Deployment\n"),
	)
	data, err := ExtractDataFromTgz(archive, "Chart.yaml")
	if err != nil {
		t.Fatalf("lookup failed: %v", err)
	}
	if string(data) != chartYaml {
		t.Errorf("got %q, want the content of mychart/Chart.yaml", data)
	}
}
