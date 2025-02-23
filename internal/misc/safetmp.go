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
