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
	"os"
	"path/filepath"
	"strings"
)

type ChartSource struct {
	Root    *os.Root
	SkipGit bool
	prefix  string
}

func OpenChartSource(dir string, allowExternal bool) (*ChartSource, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	rootDir, prefix := abs, "."
	if allowExternal {
		rootDir = filepath.VolumeName(abs) + string(filepath.Separator)
		prefix, err = filepath.Rel(rootDir, abs)
		if err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return nil, err
	}
	return &ChartSource{Root: root, prefix: prefix}, nil
}

func (s *ChartSource) Close() error { return s.Root.Close() }

func (s *ChartSource) Resolve(name string) (string, os.FileInfo, error) {
	return s.resolve(joinPathComponents(s.prefix, name), name, 0)
}

func (s *ChartSource) resolve(name, display string, links int) (string, os.FileInfo, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", nil, fmt.Errorf("chart source path %q is outside its trusted root", display)
	}
	if links > 255 {
		return "", nil, fmt.Errorf("chart source link %q is dangling or cyclic", display)
	}
	parts := strings.Split(name, string(filepath.Separator))
	current := "."
	var info os.FileInfo
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." && current == "." {
			return "", nil, fmt.Errorf("chart source path %q is outside its trusted root", display)
		}
		next := joinPathComponents(current, part)
		var err error
		info, err = s.Root.Lstat(next)
		if err != nil {
			if links > 0 && errors.Is(err, os.ErrNotExist) {
				return "", nil, fmt.Errorf("chart source link %q is dangling or cyclic", display)
			}
			return "", nil, err
		}
		current = filepath.Clean(next)
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := s.Root.Readlink(current)
		if err != nil {
			return "", nil, err
		}
		if filepath.IsAbs(target) {
			var ok bool
			target, ok = rootLinkTarget(s.Root.Name(), target)
			if !ok {
				return "", nil, fmt.Errorf("chart source path %q is outside its trusted root", display)
			}
		} else {
			target = joinPathComponents(filepath.Dir(current), target)
		}
		return s.resolve(joinPathComponents(append([]string{target}, parts[i+1:]...)...), display, links+1)
	}
	if info == nil {
		var err error
		info, err = s.Root.Lstat(current)
		if err != nil {
			return "", nil, err
		}
	}
	return current, info, nil
}

func (s *ChartSource) OpenRegular(name string) (*os.File, error) {
	resolved, info, err := s.Resolve(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("chart source path %q is not a regular file or directory", name)
	}
	f, err := s.Root.Open(resolved)
	if err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("chart source path %q is not a regular file or directory", name)
	}
	return f, nil
}

func (s *ChartSource) CopyTree(name string, dst *os.Root, target string, preserveLinks bool) error {
	return s.copyTree(name, dst, target, preserveLinks, nil)
}

func (s *ChartSource) copyTree(name string, dst *os.Root, target string, preserveLinks bool, active []os.FileInfo) error {
	resolved, info, err := s.Resolve(name)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("chart source path %q is not a regular file or directory", name)
	}
	if preserveLinks {
		linkInfo, err := s.Root.Lstat(filepath.Join(s.prefix, name))
		if err != nil {
			return err
		}
		if linkInfo.Mode()&os.ModeSymlink != 0 {
			link, err := s.Root.Readlink(filepath.Join(s.prefix, name))
			if err != nil {
				return err
			}
			return dst.Symlink(link, target)
		}
	}
	if !info.IsDir() {
		return s.copyFile(name, dst, target, info.Mode().Perm())
	}
	for _, ancestor := range active {
		if os.SameFile(ancestor, info) {
			return fmt.Errorf("chart source link %q is dangling or cyclic", name)
		}
	}
	active = append(active, info)
	if err := dst.MkdirAll(target, 0700); err != nil {
		return err
	}
	dir, err := s.Root.Open(resolved)
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(-1)
	if err := errors.Join(readErr, dir.Close()); err != nil {
		return err
	}
	for _, entry := range entries {
		if s.SkipGit && entry.Name() == ".git" {
			continue
		}
		if err := s.copyTree(filepath.Join(name, entry.Name()), dst,
			filepath.Join(target, entry.Name()), preserveLinks, active); err != nil {
			return err
		}
	}
	return nil
}

func (s *ChartSource) copyFile(name string, dst *os.Root, target string, mode os.FileMode) error {
	in, err := s.OpenRegular(name)
	if err != nil {
		return err
	}
	out, err := dst.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return errors.Join(err, in.Close())
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close(), in.Close())
}

// Joining must preserve .. until the preceding symlink components have been resolved.
func joinPathComponents(parts ...string) string {
	return strings.Join(parts, string(filepath.Separator))
}

func rootLinkTarget(root, target string) (string, bool) {
	root = filepath.Clean(root)
	if target == root {
		return ".", true
	}
	prefix := strings.TrimSuffix(root, string(filepath.Separator)) + string(filepath.Separator)
	if !strings.HasPrefix(target, prefix) {
		return "", false
	}
	return strings.TrimPrefix(target, prefix), true
}
