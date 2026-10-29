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
	"io"
	"kubocd/internal/archiveio"
	"os"
	"strings"
)

func ListTarGzContents(filename string, limits ...*archiveio.Limits) (string, error) {
	// Open the tar.gz file
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer func(file *os.File) {
		_ = file.Close()
	}(file)

	return ListTarGzContentsReader(file, limits...)
}

func ListTarGzContentsAt(source RootedDir, name string, limits ...*archiveio.Limits) (string, error) {
	f, err := source.OpenRegular(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return ListTarGzContentsReader(f, limits...)
}

func ListTarGzContentsReader(input io.Reader, limits ...*archiveio.Limits) (string, error) {
	reader, err := archiveio.New(input, limits...)
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()
	var sb strings.Builder
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return sb.String(), nil
		}
		if err != nil {
			return "", err
		}
		if err := reader.ReserveListing(int64(len(header.Name)) + 1); err != nil {
			return "", err
		}
		sb.WriteString(header.Name)
		sb.WriteByte('\n')
	}
}

func LoadArchiveYaml(source RootedDir, name string, limits *archiveio.Limits, targets ...interface{}) error {
	file, err := source.OpenRegular(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	data, err := archiveio.ReadMetadata(file, info.Size(), limits)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := YamlDecode(string(data), target); err != nil {
			return err
		}
	}
	return nil
}
