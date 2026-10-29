/*
Copyright 2025 Kubotal

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
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func SafeEnsureEmpty(loc string) error {
	err := SafeRemove(loc)
	if err != nil {
		return err
	}
	err = ensureLocation(loc)
	if err != nil {
		return err
	}
	err = setTransientFlag(loc)
	if err != nil {
		return err
	}
	return nil
}

var transientFlag = ".transient"

// ensureLocation() ensure folder exists and is empty
func ensureLocation(loc string) error {
	info, err := os.Stat(loc)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(loc, os.ModePerm)
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is a file, not a folder", loc)
	}
	// it is an existing folder. Check if empty
	f, err := os.Open(loc)
	if err != nil {
		return err // Should not occurs, as os.Stat() was ok
	}
	defer func() {
		_ = f.Close()
	}()
	// read in ONLY one file
	_, err = f.Readdir(1)
	// and if the file is EOF... well, the dir is empty.
	if err == io.EOF {
		return nil
	}
	return fmt.Errorf("folder %s is not empty", loc)
}

func SafeRemove(loc string) error {
	_, err := os.Stat(loc)
	if err != nil {
		if os.IsNotExist(err) {
			return nil //
		}
		return err
	}
	// Folder exist. Must check if flag is set
	flag := filepath.Join(loc, transientFlag)
	_, err = os.Stat(flag)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("unable to remove %s. Missing %s flag", loc, transientFlag)
		}
		return err
	}
	return os.RemoveAll(loc)
}

func setTransientFlag(loc string) error {
	flag := filepath.Join(loc, transientFlag)
	f, err := os.Create(flag)
	if err != nil {
		return err
	}
	return f.Close()
}

func SafeRemoveAt(parent RootedDir, name string) error {
	if name == "" || filepath.Clean(name) == "." {
		return fmt.Errorf("refusing to clean the trusted parent directory")
	}
	if !filepath.IsLocal(name) {
		return pathBoundary(name)
	}
	if _, err := parent.checked(filepath.Dir(name)); err != nil {
		return err
	}
	info, err := parent.Root.Lstat(joinPathComponents(parent.Rel, name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to clean symlink directory %q", name)
	}
	child, err := parent.Child(name)
	if err != nil {
		return err
	}
	parentInfo, err := parent.Root.Stat(parent.Rel)
	if err != nil {
		return err
	}
	targetInfo, err := parent.Root.Stat(child.Rel)
	if err != nil {
		return err
	}
	if os.SameFile(targetInfo, parentInfo) {
		return fmt.Errorf("refusing to clean the trusted parent directory")
	}
	marker := filepath.Join(child.Rel, transientFlag)
	info, err = parent.Root.Lstat(marker)
	if os.IsNotExist(err) {
		return fmt.Errorf("unable to remove %s. Missing %s flag", name, transientFlag)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("transient marker %q is not a regular file", marker)
	}
	return parent.Root.RemoveAll(child.Rel)
}

func SafeEnsureEmptyAt(parent RootedDir, name string) error {
	if err := SafeRemoveAt(parent, name); err != nil {
		return err
	}
	child, err := parent.Child(name)
	if err != nil {
		return err
	}
	if err := child.MkdirAll(".", os.ModePerm); err != nil {
		return err
	}
	f, err := child.Create(transientFlag)
	if err != nil {
		return err
	}
	return f.Close()
}
