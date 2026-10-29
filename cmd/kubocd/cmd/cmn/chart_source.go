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
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"kubocd/internal/misc"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	chart "helm.sh/helm/v4/pkg/chart/v2"
	"sigs.k8s.io/yaml"
)

type SourceOptions struct {
	AllowExternalLinks bool
}

const ChartSourceHelp = `Git charts may read files and file:// dependencies only within their checkout.
Local charts may read only within source.local.path, which can be absolute.
Internal links and Git sibling dependencies are supported.
Local file://../sibling dependencies are not supported.
Absolute file:// dependencies are refused; use a relative file:// path inside the source root.
--allowExternalLinks restores external reads and accepts absolute file:// dependencies
in the previous source layout; special files are always refused.`

func sourceOptions(options []SourceOptions) SourceOptions {
	if len(options) == 0 {
		return SourceOptions{}
	}
	return options[0]
}

type chartStager struct {
	source *misc.ChartSource
	root   *os.Root
	staged map[string]bool
}

func stageChart(base, chartDir, stageDir string, skipGit bool) (string, error) {
	var err error
	base, err = filepath.Abs(base)
	if err != nil {
		return "", err
	}
	source, err := misc.OpenChartSource(base, false)
	if err != nil {
		return "", err
	}
	defer func() { _ = source.Close() }()
	source.SkipGit = skipGit
	if err := os.MkdirAll(stageDir, 0700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(stageDir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	stager := &chartStager{source: source, root: root, staged: map[string]bool{}}
	if err := stager.stage(chartDir); err != nil {
		return "", err
	}
	for dir := range stager.staged {
		if err := validateLocalDependencies(root, dir); err != nil {
			return "", err
		}
	}
	return filepath.Join(stageDir, chartDir), nil
}

func (s *chartStager) stage(dir string) error {
	if s.staged[dir] {
		return nil
	}
	copied := false
	for existing := range s.staged {
		rel, err := filepath.Rel(existing, dir)
		if err == nil && filepath.IsLocal(rel) {
			copied = true
			break
		}
	}
	if !copied {
		if err := s.source.CopyTree(dir, s.root, dir, false); err != nil {
			return err
		}
	}
	s.staged[dir] = true
	return walkDependencyFiles(s.root, dir, func(name string, data dependencyManifest) error {
		return eachDependency(data, func(repository, depName string) error {
			local, ok := localDependency(filepath.Dir(name), repository, depName)
			if !ok {
				return nil
			}
			if err := checkLocalDependency(s.source, local, repository); err != nil {
				return err
			}
			return s.stage(local)
		})
	})
}

func localDependency(dir, repository, name string) (string, bool) {
	if repository == "" {
		return filepath.Join(dir, "charts", name), true
	}
	if !strings.HasPrefix(repository, "file://") {
		return "", false
	}
	local := strings.TrimPrefix(repository, "file://")
	if filepath.IsAbs(local) {
		return local, true
	}
	return filepath.Join(dir, local), true
}

func checkLocalDependency(source *misc.ChartSource, local, display string) error {
	if filepath.IsAbs(local) {
		return fmt.Errorf("absolute local chart dependency %q is not allowed; "+
			"use a relative file:// path inside the source root", display)
	}
	if !filepath.IsLocal(local) {
		return fmt.Errorf("local chart dependency %q is outside its trusted root", display)
	}
	_, info, err := source.Resolve(local)
	if err != nil {
		return fmt.Errorf("local chart dependency %q: %w", display, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("local chart dependency %q is not a directory", display)
	}
	return nil
}

type dependencyManifest struct {
	Dependencies []*chart.Dependency `json:"dependencies"`
}

func eachDependency(data dependencyManifest, visit func(string, string) error) error {
	for _, dep := range data.Dependencies {
		if dep == nil {
			return fmt.Errorf("chart dependency must be an object")
		}
		if err := dep.Validate(); err != nil {
			return err
		}
		if err := visit(dep.Repository, dep.Name); err != nil {
			return err
		}
	}
	return nil
}

func walkDependencyFiles(root *os.Root, dir string, visit func(string, dependencyManifest) error) error {
	for _, file := range []string{"Chart.yaml", "requirements.yaml", "Chart.lock", "requirements.lock"} {
		name := filepath.Join(dir, file)
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("chart source path %q is not a regular file or directory", name)
		}
		content, err := root.ReadFile(name)
		if err != nil {
			return err
		}
		var data dependencyManifest
		if err := yaml.Unmarshal(content, &data); err != nil {
			return fmt.Errorf("read chart dependencies %q: %w", name, err)
		}
		if err := visit(name, data); err != nil {
			return err
		}
	}
	charts, err := root.Open(filepath.Join(dir, "charts"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := charts.ReadDir(-1)
	if err = errors.Join(err, charts.Close()); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.IndexAny(entry.Name(), "_.") != 0 {
			if err := walkDependencyFiles(root, filepath.Join(dir, "charts", entry.Name()), visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateLocalDependencies(root *os.Root, dirs ...string) error {
	source, err := misc.OpenChartSource(root.Name(), false)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	if len(dirs) == 0 {
		dirs = []string{"."}
	}
	for _, dir := range dirs {
		if err := walkDependencyFiles(root, dir, func(name string, data dependencyManifest) error {
			return eachDependency(data, func(repository, depName string) error {
				local, ok := localDependency(filepath.Dir(name), repository, depName)
				if !ok {
					return nil
				}
				return checkLocalDependency(source, local, repository)
			})
		}); err != nil {
			return err
		}
	}
	return nil
}

func validateGeneratedTree(root *os.Root, chartDir string) error {
	return fs.WalkDir(root.FS(), filepath.ToSlash(chartDir), func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type().IsRegular() {
			return nil
		}
		return fmt.Errorf("generated chart path %q is not a regular file or directory", name)
	})
}

func updateChartDependencies(chartDir string) error {
	cmd := exec.Command("helm", "dependency", "update", chartDir)
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to build chart dependencies: %w", err)
	}
	return nil
}

func checkLegacyChart(chartDir, scratch string, visited map[string]bool) error {
	abs, err := filepath.Abs(chartDir)
	if err != nil {
		return err
	}
	if visited[abs] {
		return nil
	}
	visited[abs] = true
	source, err := misc.OpenChartSource(abs, true)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	checkDir, err := os.MkdirTemp(scratch, "check-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(checkDir) }()
	root, err := os.OpenRoot(checkDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := source.CopyTree(".", root, ".", false); err != nil {
		return err
	}
	return walkDependencyFiles(root, ".", func(name string, data dependencyManifest) error {
		return eachDependency(data, func(repository, depName string) error {
			local, ok := localDependency(filepath.Join(abs, filepath.Dir(name)), repository, depName)
			if !ok {
				return nil
			}
			return checkLegacyChart(local, scratch, visited)
		})
	})
}

func archiveChart(chartDir string, destination misc.RootedDir, archive, module string,
	prefixes []string, gitChart, allowExternal bool) (err error) {
	source, err := misc.OpenChartSource(chartDir, allowExternal)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	if !allowExternal {
		if err := validateGeneratedTree(source.Root, "."); err != nil {
			return err
		}
	}
	out, err := destination.Create(archive)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	defer func() { err = errors.Join(err, tw.Close(), gz.Close(), out.Close()) }()
	start, _, err := source.Resolve(".")
	if err != nil {
		return err
	}
	return fs.WalkDir(source.Root.FS(), filepath.ToSlash(start),
		func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(start, filepath.FromSlash(name))
			if err != nil {
				return err
			}
			if gitChart && !isChartItem("/"+filepath.ToSlash(rel), prefixes) {
				return nil
			}
			return addChartFile(tw, source, rel, path.Join(module, filepath.ToSlash(rel)))
		})
}

func addChartFile(tw *tar.Writer, source *misc.ChartSource, name, archiveName string) (err error) {
	f, err := source.OpenRegular(name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = archiveName
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}
