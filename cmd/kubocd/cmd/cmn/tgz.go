package cmn

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
)

func ExtractDataFromTgz(tgzPath string, fileName string) ([]byte, error) {
	// Open the .tgz file
	tgzFile, err := os.Open(tgzPath)
	if err != nil {
		return nil, err
	}
	defer tgzFile.Close()

	// Create a gzip reader
	gzReader, err := gzip.NewReader(tgzFile)
	if err != nil {
		return nil, err
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
			return nil, err
		}
		// Check if the file is the target YAML file
		if header.Typeflag == tar.TypeReg {
			currentFile := header.Name
			//fmt.Printf("archive file: %s\n", currentFile)
			if path.Base(currentFile) == fileName {
				// Got it. Read the file content
				ba, err := io.ReadAll(tarReader)
				if err != nil {
					return nil, err
				}
				return ba, nil
			}
		}
	}
	return nil, fmt.Errorf("file '%s' not found in archive", fileName)

}
