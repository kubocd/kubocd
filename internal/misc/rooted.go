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

package misc

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type RootedDir struct {
	Root *os.Root
	Rel  string
}

func OpenRootedDir(name string) (RootedDir, error) {
	if name == "" {
		name = "."
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return RootedDir{}, err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return RootedDir{}, err
	}
	root, err := os.OpenRoot(abs)
	return RootedDir{Root: root, Rel: "."}, err
}

func (d RootedDir) Path() string { return filepath.Join(d.Root.Name(), d.Rel) }

func (d RootedDir) Child(name string) (RootedDir, error) {
	if filepath.Clean(name) == "." {
		return RootedDir{}, pathBoundary(name)
	}
	resolved, err := d.checked(name)
	if err != nil {
		return RootedDir{}, err
	}
	base, err := d.resolve(d.Rel, 0)
	if err != nil {
		return RootedDir{}, err
	}
	if resolved == base {
		return RootedDir{}, pathBoundary(name)
	}
	return RootedDir{Root: d.Root, Rel: resolved}, nil
}

func pathBoundary(name string) error {
	return fmt.Errorf("path %q is not inside its trusted parent directory", name)
}

func (d RootedDir) resolve(name string, links int) (string, error) {
	if name == "" || filepath.IsAbs(name) || links > 255 {
		return "", pathBoundary(name)
	}
	parts := strings.Split(name, string(filepath.Separator))
	current := "."
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." && current == "." {
			return "", pathBoundary(name)
		}
		next := joinPathComponents(current, part)
		info, err := d.Root.Lstat(next)
		current = filepath.Clean(next)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := d.Root.Readlink(current)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			var ok bool
			target, ok = rootLinkTarget(d.Root.Name(), target)
			if !ok {
				return "", pathBoundary(name)
			}
		} else {
			target = joinPathComponents(filepath.Dir(current), target)
		}
		return d.resolve(joinPathComponents(append([]string{target}, parts[i+1:]...)...), links+1)
	}
	return current, nil
}

func (d RootedDir) checked(name string) (string, error) {
	if !filepath.IsLocal(name) {
		return "", pathBoundary(name)
	}
	base, err := d.resolve(d.Rel, 0)
	if err != nil {
		return "", err
	}
	scope, err := filepath.Rel(d.Rel, base)
	if err != nil || !filepath.IsLocal(scope) {
		return "", pathBoundary(d.Rel)
	}
	resolved, err := d.resolve(joinPathComponents(d.Rel, name), 0)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(base, resolved)
	if err != nil || !filepath.IsLocal(relative) {
		return "", pathBoundary(name)
	}
	return resolved, nil
}

func (d RootedDir) Relative(hostPath string) (string, error) {
	abs, err := filepath.Abs(hostPath)
	if err != nil {
		return "", err
	}
	name, err := filepath.Rel(d.Path(), abs)
	if err != nil {
		return "", err
	}
	_, err = d.checked(name)
	return name, err
}

func (d RootedDir) MkdirAll(name string, mode fs.FileMode) error {
	resolved, err := d.checked(name)
	if err != nil {
		return err
	}
	return d.Root.MkdirAll(resolved, mode)
}

func (d RootedDir) OpenRegular(name string) (*os.File, error) {
	resolved, err := d.checked(name)
	if err != nil {
		return nil, err
	}
	info, err := d.Root.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path %q is not a regular file", name)
	}
	return d.Root.Open(resolved)
}

func (d RootedDir) Create(name string) (*os.File, error) {
	return d.openFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
}

func (d RootedDir) openFile(name string, flags int, mode fs.FileMode) (*os.File, error) {
	resolved, err := d.checked(name)
	if err != nil {
		return nil, err
	}
	info, err := d.Root.Stat(resolved)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path %q is not a regular file", name)
	}
	return d.Root.OpenFile(resolved, flags, mode)
}

func (d RootedDir) WriteFile(name string, data []byte, mode fs.FileMode) (err error) {
	f, err := d.openFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	_, err = f.Write(data)
	return err
}

func (d RootedDir) ReadFile(name string) (data []byte, err error) {
	f, err := d.OpenRegular(name)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return io.ReadAll(f)
}

func (d RootedDir) ReadDir(name string) ([]os.DirEntry, error) {
	resolved, err := d.checked(name)
	if err != nil {
		return nil, err
	}
	f, err := d.Root.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(-1)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, err
}

func (d RootedDir) CopyFrom(name string, source RootedDir, sourceName string) (err error) {
	in, err := source.OpenRegular(sourceName)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	out, err := d.Create(name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	_, err = io.Copy(out, in)
	return err
}

func PrepareDir(parent RootedDir, name string) (RootedDir, error) {
	if err := SafeEnsureEmptyAt(parent, name); err != nil {
		return RootedDir{}, err
	}
	return parent.Child(name)
}

func PrepareOutput(output, name string) (RootedDir, string, error) {
	parent, err := OpenRootedDir(output)
	if err != nil {
		return RootedDir{}, "", err
	}
	if output == "" {
		return parent, "", nil
	}
	child, err := PrepareDir(parent, name)
	if err != nil {
		_ = parent.Root.Close()
		return RootedDir{}, "", err
	}
	return child, child.Path(), nil
}

func (d RootedDir) LoadYaml(name string, objects ...interface{}) error {
	data, err := d.ReadFile(name)
	if err != nil {
		return err
	}
	for _, object := range objects {
		if err := YamlDecode(string(data), object); err != nil {
			return err
		}
	}
	return nil
}
