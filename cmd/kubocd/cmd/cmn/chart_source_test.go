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
	"fmt"
	"kubocd/internal/kubopackage"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	chart "helm.sh/helm/v4/pkg/chart/v2"
	chartloader "helm.sh/helm/v4/pkg/chart/v2/loader"
	"sigs.k8s.io/yaml"
)

func TestChartSourceOptOut(t *testing.T) {
	chart := t.TempDir()
	sourceChart(t, chart)
	outside := filepath.Join(t.TempDir(), "outside")
	sourceWrite(t, outside, "opt-out-marker")
	sourceLink(t, outside, filepath.Join(chart, "templates", "leak.yaml"))
	sourceHelm(t, ":")
	if _, err := getHelmCharArchiveFromLocal("", chart, "main", t.TempDir()); err == nil {
		t.Fatal("external source bytes were accepted by default")
	}
	archive, err := getHelmCharArchiveFromLocal("", chart, "main", t.TempDir(), SourceOptions{AllowExternalLinks: true})
	if err != nil {
		t.Fatal(err)
	}
	if sourceContents(t, archive)["main/templates/leak.yaml"] != "opt-out-marker" {
		t.Fatal("opt-out did not restore the external source read")
	}
}

func TestFetchArchivesSourceOptions(t *testing.T) {
	for _, gitSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "git"}[gitSource], func(t *testing.T) {
			dir, work := t.TempDir(), t.TempDir()
			chart := filepath.Join(dir, "chart")
			sourceChart(t, chart)
			outside := filepath.Join(t.TempDir(), "secret")
			sourceWrite(t, outside, "opt-out-marker")
			target := outside
			source := "local:\n      path: chart\n"
			if gitSource {
				var err error
				target, err = filepath.Rel(filepath.Join(work, "git-workdir", "repo", "chart", "templates"), outside)
				if err != nil {
					t.Fatal(err)
				}
				source = fmt.Sprintf("git:\n      url: %q\n      branch: main\n      path: chart\n", dir)
			}
			sourceLink(t, target, filepath.Join(chart, "templates", "leak.yaml"))
			if gitSource {
				sourceRepo(t, dir)
			}
			pck := &kubopackage.Package{}
			manifest := "modules:\n- name: main\n  type: HelmChart\n  source:\n    " + source
			if err := yaml.Unmarshal([]byte(manifest), pck); err != nil {
				t.Fatal(err)
			}
			sourceHelm(t, ":")
			if _, _, err := FetchArchives("", pck, t.TempDir(), work, dir); err == nil {
				t.Fatal("external source bytes were accepted by default")
			}
			archives, _, err := FetchArchives("", pck, t.TempDir(), work, dir, SourceOptions{AllowExternalLinks: true})
			if err != nil {
				t.Fatalf("FetchArchives did not forward the source opt-out: %v", err)
			}
			if len(archives) != 1 || sourceContents(t, archives[0].Path)["main/templates/leak.yaml"] != "opt-out-marker" {
				t.Fatal("FetchArchives did not forward the source opt-out")
			}
		})
	}
}

func TestStagedDependencyValidation(t *testing.T) {
	dir := t.TempDir()
	sourceChart(t, dir)
	outside := t.TempDir()
	sourceChart(t, outside)
	sourceWrite(t, filepath.Join(dir, "requirements.yaml"),
		"dependencies:\n- name: dep\n  repository: file://"+outside+"\n")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := validateLocalDependencies(root); err == nil {
		t.Fatal("staged external dependency was accepted")
	}
}

func TestGitMonorepoWithRealHelm(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm executable not installed")
	}
	for _, variable := range []string{"HELM_CONFIG_HOME", "HELM_CACHE_HOME", "HELM_DATA_HOME"} {
		t.Setenv(variable, t.TempDir())
	}
	repo := t.TempDir()
	sourceChart(t, filepath.Join(repo, "apps", "main"))
	sourceWrite(t, filepath.Join(repo, "libs", "common", "Chart.yaml"),
		"apiVersion: v2\nname: common\nversion: 0.1.0\n")
	metadata := "# Application chart, preserve this comment\napiVersion: v2\nname: demo\n" +
		"version: 0.1.0\nappVersion: 1.10\ndependencies:\n" +
		"- name: common\n  version: 0.1.0\n  repository: file://./../../libs/common/\n"
	sourceWrite(t, filepath.Join(repo, "apps", "main", "Chart.yaml"), metadata)
	sourceLink(t, "/unused", filepath.Join(repo, "docs", "unrelated"))
	sourceRepo(t, repo)
	archive, err := getHelmChartArchiveFromGit("", repo, "main", "", "apps/main", "main", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	files := sourceContents(t, archive)
	if files["main/Chart.yaml"] != metadata {
		t.Fatalf("archived Chart.yaml differs from source:\n%s", files["main/Chart.yaml"])
	}
	if len(files["main/charts/common-0.1.0.tgz"]) == 0 {
		t.Fatal("Helm did not package the sibling dependency")
	}
}

func TestGitChartMetadataIsPreserved(t *testing.T) {
	for _, name := range []string{"Chart.yaml", "requirements.yaml", "Chart.lock", "requirements.lock"} {
		for _, repository := range []string{"file://../lib/", "file://./../lib/"} {
			t.Run(name+"/"+repository, func(t *testing.T) {
				repo := t.TempDir()
				chart := filepath.Join(repo, "charts", "app")
				sourceChart(t, chart)
				sourceChart(t, filepath.Join(repo, "charts", "lib"))
				metadata := "# Application chart, comment kept by the author\napiVersion: v2\n" +
					"name: app\nversion: 0.1.0\nappVersion: 1.10\ndependencies:\n" +
					"  - name: lib\n    version: 0.1.0\n    repository: " + repository + "\n"
				sourceWrite(t, filepath.Join(chart, name), metadata)
				sourceRepo(t, repo)
				sourceHelm(t, ":")
				archive, err := getHelmChartArchiveFromGit("", repo, "main", "", "charts/app", "main",
					t.TempDir(), []string{"/requirements"})
				if err != nil {
					t.Fatal(err)
				}
				if got := sourceContents(t, archive)["main/"+name]; got != metadata {
					t.Fatalf("archived %s differs from source:\n%s", name, got)
				}
			})
		}
	}
}

func TestAbsoluteLocalDependencyRequiresOptOut(t *testing.T) {
	for _, name := range []string{"Chart.yaml", "requirements.yaml", "Chart.lock", "requirements.lock"} {
		for _, inside := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/inside=%t", name, inside), func(t *testing.T) {
				chart, dep := t.TempDir(), t.TempDir()
				sourceChart(t, chart)
				if inside {
					dep = filepath.Join(chart, "deps", "common")
				}
				sourceChart(t, dep)
				metadata := "# Keep the absolute dependency unchanged with the opt-out\n" +
					"apiVersion: v2\nname: app\nversion: 0.1.0\nappVersion: 1.10\n" +
					"dependencies:\n- name: dep\n  repository: file://" + dep + "\n"
				sourceWrite(t, filepath.Join(chart, name), metadata)
				marker := sourceHelm(t, ":")
				_, err := getHelmCharArchiveFromLocal("", chart, "main", t.TempDir())
				want := fmt.Sprintf("absolute local chart dependency %q is not allowed; ", "file://"+dep) +
					"use a relative file:// path inside the source root"
				if err == nil || err.Error() != want {
					t.Fatalf("absolute dependency refusal: got %v, want %q", err, want)
				}
				sourceRefused(t, err, marker, want)
				archive, err := getHelmCharArchiveFromLocal("", chart, "main", t.TempDir(),
					SourceOptions{AllowExternalLinks: true})
				if err != nil {
					t.Fatal(err)
				}
				if got := sourceContents(t, archive)["main/"+name]; got != metadata {
					t.Fatalf("opt-out changed %s:\n%s", name, got)
				}
			})
		}
	}
}

func TestDependencyDeclarationsMatchHelm(t *testing.T) {
	for _, file := range []string{"Chart.yaml", "requirements.yaml", "Chart.lock", "requirements.lock"} {
		for _, declaration := range []string{
			"Dependencies:\n- Name: dep\n  Repository: %s\n",
			"DEPENDENCIES:\n- NAME: dep\n  REPOSITORY: %s\n",
			"dependencies:\n- name: dep\n  Repository: %s\n",
			"dependencies: []\nDependencies:\n- name: dep\n  repository: %s\n",
			"Dependencies: []\ndependencies:\n- name: dep\n  repository: %s\n",
			"dependencies:\n- name: dep\n  Repository: https://example.invalid\n  repository: %s\n",
			"dependencies:\n- name: dep\n  repository: https://example.invalid\n  Repository: %s\n",
		} {
			t.Run(file+"/"+declaration, func(t *testing.T) {
				base, outside := t.TempDir(), t.TempDir()
				sourceChart(t, base)
				sourceChart(t, outside)
				content := "apiVersion: v2\nname: demo\nversion: 0.1.0\n" + fmt.Sprintf(declaration, "file://"+outside)
				sourceWrite(t, filepath.Join(base, file), content)
				var helm chart.Metadata
				if err := yaml.Unmarshal([]byte(content), &helm); err != nil {
					t.Fatal(err)
				}
				hostile := false
				for _, dep := range helm.Dependencies {
					hostile = hostile || strings.HasPrefix(dep.Repository, "file://")
				}
				stage := t.TempDir()
				_, err := stageChart(base, ".", stage, false)
				if hostile {
					if err == nil || !strings.Contains(err.Error(), "absolute local chart dependency") {
						t.Fatalf("Helm-visible external dependency was not refused: %v", err)
					}
				} else {
					if err != nil {
						t.Fatalf("dependency decoding differs from Helm: %v", err)
					}
					got, err := os.ReadFile(filepath.Join(stage, file))
					if err != nil || string(got) != content {
						t.Fatalf("dependency manifest changed: %s, %v", got, err)
					}
				}
			})
		}
	}
}

func TestDependencyManifestNamesInTemplates(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			base := t.TempDir()
			sourceChart(t, base)
			template := "{{- if .Values.enabled }}\nkind: ConfigMap\n{{- end }}\n"
			for _, name := range []string{"Chart.yaml", "requirements.yaml", "Chart.lock", "requirements.lock"} {
				sourceWrite(t, filepath.Join(base, "templates", name), template)
				sourceWrite(t, filepath.Join(base, "charts", "child", "templates", name), template)
			}
			sourceChart(t, filepath.Join(base, "charts", "child"))
			sourceHelm(t, ":")
			archive, err := getHelmCharArchiveFromLocal("", base, "main", t.TempDir(), SourceOptions{AllowExternalLinks: allow})
			if err != nil {
				t.Fatalf("template mistaken for dependency manifest: %v", err)
			}
			if got := sourceContents(t, archive)["main/templates/Chart.yaml"]; got != template {
				t.Fatalf("template bytes changed: %q", got)
			}
		})
	}
}

func TestUnpackedSubchartDependenciesAreChecked(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	sourceChart(t, base)
	sourceChart(t, outside)
	sourceWrite(t, filepath.Join(base, "charts", "child", "Chart.yaml"), `apiVersion: v2
name: child
version: 0.1.0
Dependencies:
- name: dep
  repository: file://`+outside+"\n")
	if _, err := stageChart(base, ".", t.TempDir(), false); err == nil {
		t.Fatal("unpacked subchart dependency was not refused")
	}
}

func TestDependencyRootsWithinSelectedChart(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	sourceChart(t, base)
	sourceChart(t, outside)
	sourceWrite(t, filepath.Join(base, "Chart.yaml"), `apiVersion: v2
name: app
version: 0.1.0
dependencies:
- name: child
  repository: file://deps/child
`)
	sourceWrite(t, filepath.Join(base, "deps", "child", "Chart.yaml"), `apiVersion: v2
name: child
version: 0.1.0
Dependencies:
- name: dep
  repository: file://`+outside+"\n")
	if _, err := stageChart(base, ".", t.TempDir(), false); err == nil {
		t.Fatal("local dependency root was not checked")
	}
}

func TestCapitalDependenciesWithRealHelm(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm executable not installed")
	}
	for _, variable := range []string{"HELM_CONFIG_HOME", "HELM_CACHE_HOME", "HELM_DATA_HOME"} {
		t.Setenv(variable, t.TempDir())
	}
	for _, key := range []string{"Dependencies", "DEPENDENCIES"} {
		t.Run(key, func(t *testing.T) {
			base, outside := t.TempDir(), t.TempDir()
			sourceChart(t, base)
			sourceChart(t, outside)
			sourceWrite(t, filepath.Join(outside, "templates", "sentinel.yaml"), "external-dependency-sentinel")
			sourceWrite(t, filepath.Join(base, "Chart.yaml"), fmt.Sprintf(`apiVersion: v2
name: app
version: 0.1.0
%s:
- name: demo
  version: 0.1.0
  repository: file://%s
`, key, outside))
			_, err := getHelmCharArchiveFromLocal("", base, "main", t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "absolute local chart dependency") {
				t.Fatalf("Helm-visible external dependency was not refused: %v", err)
			}
		})
	}
}

func TestDependencyControlsMatchHelm(t *testing.T) {
	for _, file := range []string{"Chart.yaml", "requirements.yaml", "Chart.lock", "requirements.lock"} {
		for _, control := range []string{`\u0000`, `\u0007`} {
			t.Run(file+"/"+control, func(t *testing.T) {
				base, outside := t.TempDir(), t.TempDir()
				sourceChart(t, base)
				sourceChart(t, outside)
				content := "apiVersion: v2\nname: app\nversion: 0.1.0\nDependencies:\n" +
					"- name: demo\n  version: 0.1.0\n  repository: \"fi" + control + "le://" + outside + "\"\n"
				sourceWrite(t, filepath.Join(base, file), content)
				var decoded chart.Metadata
				if err := yaml.Unmarshal([]byte(content), &decoded); err != nil {
					t.Fatal(err)
				}
				if err := decoded.Dependencies[0].Validate(); err != nil {
					t.Fatal(err)
				}
				if decoded.Dependencies[0].Repository != "file://"+outside {
					t.Fatal("fixture did not match Helm sanitization")
				}
				marker := sourceHelm(t, ":")
				_, err := getHelmCharArchiveFromLocal("", base, "main", t.TempDir())
				if err == nil || !strings.Contains(err.Error(), "absolute local chart dependency") {
					t.Fatalf("sanitized external dependency was not refused: %v", err)
				}
				sourceRefused(t, err, marker, "absolute local chart dependency")
				archive, err := getHelmCharArchiveFromLocal("", base, "main", t.TempDir(), SourceOptions{AllowExternalLinks: true})
				if err != nil {
					t.Fatal(err)
				}
				if got := sourceContents(t, archive)["main/"+file]; got != content {
					t.Fatalf("sanitization changed source bytes: %q", got)
				}
			})
		}
	}
}

func TestIgnoredSubchartManifestsStayUnparsed(t *testing.T) {
	for _, name := range []string{"_scratch", ".scratch"} {
		for _, allow := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/allow=%t", name, allow), func(t *testing.T) {
				base := t.TempDir()
				sourceChart(t, base)
				template := "{{- if .Values.enabled }}\nkind: ConfigMap\n{{- end }}\n"
				sourceWrite(t, filepath.Join(base, "charts", name, "Chart.yaml"), template)
				if _, err := chartloader.LoadDir(base); err != nil {
					t.Fatalf("Helm does not accept ignored chart fixture: %v", err)
				}
				sourceHelm(t, ":")
				archive, err := getHelmCharArchiveFromLocal("", base, "main", t.TempDir(), SourceOptions{AllowExternalLinks: allow})
				if err != nil {
					t.Fatalf("ignored subchart was parsed as dependency YAML: %v", err)
				}
				if got := sourceContents(t, archive)["main/charts/"+name+"/Chart.yaml"]; got != template {
					t.Fatalf("ignored subchart bytes changed: %q", got)
				}
			})
		}
	}
}
