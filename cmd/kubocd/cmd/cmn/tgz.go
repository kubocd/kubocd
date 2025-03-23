package cmn

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"sigs.k8s.io/yaml"
)

func UnmarshalDataFromTgz(tgzPath string, fileName string, data interface{}) error {
	ba, err := ExtractDataFromTgz(tgzPath, fileName)
	if err != nil {
		return err
	}
	return yaml.UnmarshalStrict(ba, data)
}

func ExtractAllFromTgz(tgzPath string, targetPath string) error {
	// Open the .tgz file
	tgzFile, err := os.Open(tgzPath)
	if err != nil {
		return err
	}
	defer tgzFile.Close()

	// Create a gzip reader
	gzReader, err := gzip.NewReader(tgzFile)
	if err != nil {
		return err
	}
	defer gzReader.Close()

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
		// Check if the file is the target YAML file
		if header.Typeflag == tar.TypeReg {
			currentFile := header.Name
			tf := path.Join(targetPath, currentFile)
			err := os.MkdirAll(path.Dir(tf), 0755)
			if err != nil {
				return err
			}
			out, err := os.Create(tf)
			if err != nil {
				return err
			}
			if _, err = io.Copy(out, tarReader); err != nil {
				return err
			}
			if err = out.Sync(); err != nil {
				return err
			}
			if err = out.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

func ExtractFromTgz(tgzPath string, fileName string, writer io.Writer) error {
	// Open the .tgz file
	tgzFile, err := os.Open(tgzPath)
	if err != nil {
		return err
	}
	defer tgzFile.Close()

	// Create a gzip reader
	gzReader, err := gzip.NewReader(tgzFile)
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

func ExtractFileFromTgz(tgzPath string, fileName string, targetFile string) error {
	out, err := os.Create(targetFile)
	if err != nil {
		return err
	}
	defer func(out *os.File) { _ = out.Close() }(out)
	err = ExtractFromTgz(tgzPath, fileName, out)
	return err
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
