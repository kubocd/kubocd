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
	"archive/tar"
	"compress/gzip"
	"io"
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

	return ListTarGzContentsReader(file)
}

func ListTarGzContentsAt(source RootedDir, name string) (string, error) {
	f, err := source.OpenRegular(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return ListTarGzContentsReader(f)
}

func ListTarGzContentsReader(input io.Reader) (string, error) {
	// Create a gzip reader
	gzReader, err := gzip.NewReader(input)
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
