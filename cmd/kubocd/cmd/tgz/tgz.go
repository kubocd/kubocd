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

package tgz

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"kubocd/internal/misc"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func UnmarshalDataFromTgz(tgzPath string, fileName string, data interface{}) error {
	ba, err := ExtractDataFromTgz(tgzPath, fileName)
	if err != nil {
		return err
	}
	return misc.YamlDecode(string(ba), data)
}

// ExtractAllFromTgz writes the regular files of the archive under targetPath, created if missing. It fails on an
// entry whose name is not a relative path inside targetPath, and prevents archive paths from escaping targetPath,
// including through existing symbolic links.
func ExtractAllFromTgz(tgzPath string, targetPath string) error {
	tgzFile, err := os.Open(tgzPath)
	if err != nil {
		return err
	}
	defer func() { _ = tgzFile.Close() }()
	target, err := misc.OpenRootedDir(targetPath)
	if err != nil {
		return err
	}
	defer func() { _ = target.Root.Close() }()
	return ExtractAllFromReader(tgzFile, target)
}

func ExtractAllAt(source misc.RootedDir, name string, target misc.RootedDir) error {
	f, err := source.OpenRegular(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return ExtractAllFromReader(f, target)
}

func ExtractAllFromReader(input io.Reader, target misc.RootedDir) error {
	gzReader, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer func() { _ = gzReader.Close() }()
	if err := target.MkdirAll(".", 0755); err != nil {
		return err
	}
	// Create a tar reader
	tarReader := tar.NewReader(gzReader)

	// Iterate through the archive and create files
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			return err
		}
		if !isLocalEntryName(header.Name) {
			return fmt.Errorf("archive entry %q is not a relative path inside the target folder", header.Name)
		}
		// Link entries are skipped, as only regular files are written.
		if header.Typeflag == tar.TypeReg {
			if err := extractFile(target, filepath.FromSlash(header.Name), tarReader); err != nil {
				return err
			}
		}
	}
	return nil
}

// extractFile writes content to the file called name under root, after creating its parent folders.
func extractFile(root misc.RootedDir, name string, content io.Reader) error {
	if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return err
	}
	out, err := root.Create(name)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err = io.Copy(out, content); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	return out.Close()
}

// isLocalEntryName reports whether an archive entry name is a relative path inside the extraction folder. A
// backslash is refused on every OS, as Windows reads it as a separator.
func isLocalEntryName(name string) bool {
	return filepath.IsLocal(name) && !strings.Contains(name, `\`)
}

func ExtractFromTgz(tgzPath string, fileName string, writer io.Writer) error {
	// Open the .tgz file
	tgzFile, err := os.Open(tgzPath)
	if err != nil {
		return err
	}
	defer tgzFile.Close()

	return ExtractFromReader(tgzFile, fileName, writer)
}

func ExtractFromReader(input io.Reader, fileName string, writer io.Writer) error {
	// Create a gzip reader
	gzReader, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer gzReader.Close()

	// Create a tar reader
	tarReader := tar.NewReader(gzReader)

	// Iterate through the archive to find the YAML file
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			return err
		}
		// Check if the file is the target YAML file
		if header.Typeflag == tar.TypeReg {
			currentFile := header.Name
			//fmt.Printf("archive file: %s\n", currentFile)
			if path.Base(currentFile) == fileName {
				// Got it. Read the file content
				_, err := io.Copy(writer, tarReader)
				if err != nil {
					return err
				}
				return nil
			}
		}
	}
	return fmt.Errorf("file '%s' not found in archive", fileName)
}

func ExtractDataFromTgz(tgzPath string, fileName string) ([]byte, error) {
	bb := &bytes.Buffer{}
	err := ExtractFromTgz(tgzPath, fileName, bb)
	if err != nil {
		return nil, err
	}
	return bb.Bytes(), nil
}

func AddToArchive(tw *tar.Writer, filePath string, inArchiveName string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file '%s': %w", filePath, err)
	}
	defer f.Close()

	// Get FileInfo about our file providing file size, mode, etc.
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file '%s': %w", filePath, err)
	}
	// Create a tar Header from the FileInfo data
	header, err := tar.FileInfoHeader(info, info.Name())
	if err != nil {
		return fmt.Errorf("failed to create header for file '%s': %w", filePath, err)
	}
	// https://golang.org/src/archive/tar/common.go?#L626
	header.Name = inArchiveName
	// Write file header to the tar archive
	err = tw.WriteHeader(header)
	if err != nil {
		return fmt.Errorf("failed to write tar header for file '%s': %w", filePath, err)
	}
	// Copy file content to tar archive
	_, err = io.Copy(tw, f)
	if err != nil {
		return fmt.Errorf("failed to copy file %s to archive: %w", filePath, err)
	}
	return nil
}

func ExtractDataAt(source misc.RootedDir, name, fileName string) ([]byte, error) {
	f, err := source.OpenRegular(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	bb := &bytes.Buffer{}
	if err := ExtractFromReader(f, fileName, bb); err != nil {
		return nil, err
	}
	return bb.Bytes(), nil
}

func UnmarshalDataAt(source misc.RootedDir, name, fileName string, data interface{}) error {
	ba, err := ExtractDataAt(source, name, fileName)
	if err != nil {
		return err
	}
	return misc.YamlDecode(string(ba), data)
}

func AddToArchiveAt(tw *tar.Writer, source misc.RootedDir, name, inArchiveName string) error {
	f, err := source.OpenRegular(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, info.Name())
	if err != nil {
		return err
	}
	header.Name = inArchiveName
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}
