package misc

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CopyDir copies the content of src to dst. src should be a full path.
func CopyDir(src, dst string) error {

	return filepath.Walk(src, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// copy to this path
		outpath := filepath.Join(dst, strings.TrimPrefix(path, src))

		if info.IsDir() {
			err := os.MkdirAll(outpath, info.Mode())
			if err != nil {
				return err
			}
			return nil // means recursive
		}

		// handle irregular files
		if !info.Mode().IsRegular() {
			switch info.Mode().Type() & os.ModeType {
			case os.ModeSymlink:
				link, err := os.Readlink(path)
				if err != nil {
					return err
				}
				return os.Symlink(link, outpath)
			}
			return nil
		}

		// copy contents of regular file efficiently

		// open input
		in, _ := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()

		// create output
		fh, err := os.Create(outpath)
		if err != nil {
			return err
		}
		defer fh.Close()

		// make it the same
		err = fh.Chmod(info.Mode())
		if err != nil {
			return err
		}

		// copy content
		_, err = io.Copy(fh, in)
		return err
	})
}
