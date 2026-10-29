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

//nolint:goconst // Fixture names are clearer beside their cases.
package cmn

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sourceWrite(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func sourceLink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func sourceChart(t *testing.T, dir string) {
	t.Helper()
	sourceWrite(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	sourceWrite(t, filepath.Join(dir, "values.yaml"), "healthy: true\n")
}

func sourceHelm(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "helm-called")
	name := filepath.Join(bin, "helm")
	sourceWrite(t, name, "#!/bin/sh\nset -eu\nprintf '%s' \"$3\" > \"$HELM_MARKER\"\n"+script+"\n")
	if err := os.Chmod(name, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HELM_MARKER", marker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

func sourceRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"add", "."},
		{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "fixture"}} {
		command := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v: %s", err, output)
		}
	}

}

func sourceContents(t *testing.T, archive string) map[string]string {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[h.Name] = string(b)
	}
}

func sourceRefused(t *testing.T, err error, marker, diagnostic string) {
	t.Helper()
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Errorf("Helm ran before the unsafe source was refused")
	}
	if err == nil || !strings.Contains(err.Error(), diagnostic) {
		t.Errorf("unsafe source was not refused with %q: %v", diagnostic, err)
	}
}

func TestGitChartIsStagedBeforeHelm(t *testing.T) {
	for _, link := range []string{"outgoing", "internal", "directory", "absolute-internal"} {
		t.Run(link, func(t *testing.T) {
			base := t.TempDir()
			repo := filepath.Join(base, "repo")
			chart := filepath.Join(repo, "chart")
			sourceChart(t, chart)
			sourceWrite(t, filepath.Join(repo, "shared", "x.yaml"), "shared-marker")
			sourceWrite(t, filepath.Join(base, "secret"), "outside-marker")
			work := t.TempDir()
			target := "../../shared/x.yaml"
			if link == "outgoing" {
				target, _ = filepath.Rel(filepath.Join(work, "git-workdir", "repo", "chart", "templates"),
					filepath.Join(base, "secret"))
			}
			if link == "directory" {
				target = "../../shared"
			}
			// go-git rebases absolute link targets onto the checkout root.
			if link == "absolute-internal" {
				target = "/shared/x.yaml"
			}
			sourceLink(t, target, filepath.Join(chart, "templates", "x.yaml"))
			sourceLink(t, "/missing/unrelated", filepath.Join(repo, "docs", "dangling"))
			sourceLink(t, filepath.Join(base, "secret"), filepath.Join(repo, "examples", "outgoing"))
			sourceRepo(t, repo)
			marker := sourceHelm(t, ":")
			archive, err := getHelmChartArchiveFromGit("", repo, "main", "", "chart", "main", work, nil)
			if link == "outgoing" {
				sourceRefused(t, err, marker, "outside its trusted root")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			staged, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(string(staged), "..", "docs")); !os.IsNotExist(err) {
				t.Fatal("unrelated checkout files were staged")
			}
			files := sourceContents(t, archive)
			name := "main/templates/x.yaml"
			if link == "directory" {
				name += "/x.yaml"
			}
			if files[name] != "shared-marker" {
				t.Fatalf("internal link was not materialized: %v", files)
			}
		})
	}
}

func TestGitDependencyClosure(t *testing.T) {
	for _, hostile := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "outgoing-nested"}[hostile], func(t *testing.T) {
			repo := t.TempDir()
			main := filepath.Join(repo, "apps", "main")
			common := filepath.Join(repo, "libs", "common")
			leaf := filepath.Join(repo, "libs", "space %2e leaf")
			for _, dir := range []string{main, common, leaf} {
				sourceChart(t, dir)
			}
			sourceWrite(t, filepath.Join(main, "requirements.yaml"),
				"dependencies:\n- name: common\n  version: 0.1.0\n  repository: file://../../libs/common\n")
			ref := "file://../space %2e leaf"
			if hostile {
				outside := t.TempDir()
				sourceChart(t, outside)
				ref = "file://" + outside
			}
			sourceWrite(t, filepath.Join(common, "requirements.yaml"),
				"dependencies:\n- name: leaf\n  version: 0.1.0\n  repository: '"+ref+"'\n")
			sourceLink(t, "/not-present", filepath.Join(repo, "docs", "bad"))
			sourceRepo(t, repo)
			marker := sourceHelm(t, ":")
			archive, err := getHelmChartArchiveFromGit("", repo, "main", "", "apps/main", "main", t.TempDir(), nil)
			if hostile {
				sourceRefused(t, err, marker, "local chart dependency")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			staged, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"../../libs/common/Chart.yaml", "../../libs/space %2e leaf/Chart.yaml"} {
				if _, err := os.Stat(filepath.Join(string(staged), name)); err != nil {
					t.Fatalf("local dependency closure was not staged: %v", err)
				}
			}
			if sourceContents(t, archive)["main/values.yaml"] != "healthy: true\n" {
				t.Fatal("chart payload changed")
			}
		})
	}
}

func TestGitChartPathStaysInClone(t *testing.T) {
	for _, kind := range []string{"parent", "absolute", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			repo := t.TempDir()
			sourceChart(t, repo)
			outside := t.TempDir()
			sourceChart(t, outside)
			work := t.TempDir()
			chartPath := outside
			if kind == "absolute" {
				sourceChart(t, filepath.Join(repo, outside))
			}
			if kind == "parent" {
				chartPath = "../../outside"
				sourceChart(t, filepath.Join(work, "outside"))
			}
			if kind == "symlink" {
				relative, err := filepath.Rel(filepath.Join(work, "git-workdir", "repo"), outside)
				if err != nil {
					t.Fatal(err)
				}
				sourceLink(t, relative, filepath.Join(repo, "escape"))
				chartPath = "escape"
			}
			sourceRepo(t, repo)
			marker := sourceHelm(t, ":")
			_, err := getHelmChartArchiveFromGit("", repo, "main", "", chartPath, "main", work, nil)
			want := "not inside the checkout"
			if kind == "symlink" {
				want = "outside its trusted root"
			}
			sourceRefused(t, err, marker, want)
		})
	}
}

func TestChartDependenciesStayInStaging(t *testing.T) {
	for _, manifest := range []string{"Chart.yaml", "requirements.yaml", "Chart.lock", "requirements.lock"} {
		t.Run(manifest, func(t *testing.T) {
			chart := t.TempDir()
			sourceChart(t, chart)
			outside := t.TempDir()
			sourceChart(t, outside)
			prefix := ""
			if manifest == "Chart.yaml" {
				prefix = "apiVersion: v2\nname: demo\nversion: 0.1.0\n"
			}
			sourceWrite(t, filepath.Join(chart, manifest),
				prefix+"dependencies:\n- name: dep\n  version: 0.1.0\n  repository: file://"+outside+"\n")
			marker := sourceHelm(t, ":")
			_, err := getHelmCharArchiveFromLocal("", chart, "main", t.TempDir())
			sourceRefused(t, err, marker, "local chart dependency")
		})
	}
}

func TestLocalChartStagingContainsReads(t *testing.T) {
	for _, kind := range []string{"outgoing", "dangling", "cycle", "healthy", "sibling"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			chart := filepath.Join(base, "chart")
			sourceChart(t, chart)
			sourceChart(t, filepath.Join(base, "sibling"))
			sourceWrite(t, filepath.Join(base, "secret"), "outside-marker")
			want := "outside its trusted root"
			switch kind {
			case "outgoing":
				sourceLink(t, filepath.Join(base, "secret"), filepath.Join(chart, "leak"))
			case "dangling":
				sourceLink(t, "missing", filepath.Join(chart, "leak"))
				want = "dangling or cyclic"
			case "cycle":
				sourceLink(t, ".", filepath.Join(chart, "leak"))
				want = "dangling or cyclic"
			case "healthy":
				sourceLink(t, "values.yaml", filepath.Join(chart, "extra"))
			case "sibling":
				sourceWrite(t, filepath.Join(chart, "requirements.yaml"),
					"dependencies:\n- name: dep\n  version: 0.1.0\n  repository: file://../sibling\n")
				want = "local chart dependency"
			}
			marker := sourceHelm(t, ":")
			archive, err := getHelmCharArchiveFromLocal("", chart, "main", t.TempDir())
			if kind != "healthy" {
				sourceRefused(t, err, marker, want)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if sourceContents(t, archive)["main/extra"] != "healthy: true\n" {
				t.Fatal("internal local link was not materialized")
			}
		})
	}
}

func TestGeneratedChartTreeIsValidated(t *testing.T) {
	for _, target := range []string{"../values.yaml", "/outside"} {
		t.Run(target, func(t *testing.T) {
			chart := t.TempDir()
			sourceChart(t, chart)
			outside := filepath.Join(t.TempDir(), "secret")
			sourceWrite(t, outside, "outside-marker")
			if target == "/outside" {
				target = outside
			}
			t.Setenv("GENERATED_TARGET", target)
			sourceHelm(t, "mkdir -p \"$3/charts\"\nln -s \"$GENERATED_TARGET\" \"$3/charts/generated\"")
			_, err := getHelmCharArchiveFromLocal("", chart, "main", t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "generated chart path") {
				t.Fatalf("generated link was not refused: %v", err)
			}
		})
	}
}
