package misc

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"strings"
)

func ListTarGzContents(filename string) (string, error) {
	// Open the tar.gz file
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer func(file *os.File) {
		_ = file.Close()
	}(file)

	// Create a gzip reader
	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer func(gzReader *gzip.Reader) {
		_ = gzReader.Close()
	}(gzReader)

	// Create a tar reader
	tarReader := tar.NewReader(gzReader)

	// Iterate through the tar archive
	var sb strings.Builder
	for {
		header, err := tarReader.Next()
		if err != nil {
			break // End of archive
		}
		sb.WriteString(header.Name)
		sb.WriteString("\n")
	}

	return sb.String(), nil
}
