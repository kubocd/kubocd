/*
Copyright 2026 Alexandre MABROUK

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

package cmd

import (
	"context"
	"encoding/json"
	"kubocd/internal/global"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/file"
)

const exampleMediaType = "application/vnd.example.config.v1+json"

func TestBuildImage(t *testing.T) {
	const tag = "1.0.0"
	cases := map[string]struct {
		configFile, configMediaType, artifactType string
	}{
		"default":       {"manifest.json", global.PackageConfigMediaType, ""},
		"custom config": {"config.json", exampleMediaType, exampleMediaType},
	}
	for name, c := range cases {
		dir := t.TempDir()
		config := []byte(`{"name":"podinfo"}`)
		if err := os.WriteFile(path.Join(dir, "assembly.tgz"), []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path.Join(dir, c.configFile), config, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		ociFs, err := file.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := buildImage(ctx, ociFs, tag, c.configFile, c.configMediaType, c.artifactType); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		desc, err := ociFs.Resolve(ctx, tag)
		if err != nil {
			t.Fatal(err)
		}
		data, err := content.FetchAll(ctx, ociFs, desc)
		if err != nil {
			t.Fatal(err)
		}
		_ = ociFs.Close()
		var manifest v1.Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		if manifest.Config.MediaType != c.configMediaType || manifest.Config.Digest != digest.FromBytes(config) ||
			manifest.ArtifactType != c.artifactType {
			t.Errorf("%s: config %+v, artifact type %q", name, manifest.Config, manifest.ArtifactType)
		}
		if len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != global.PackageContentMediaType {
			t.Errorf("%s: layers %+v", name, manifest.Layers)
		}
	}
}

func TestPackConfigOptions(t *testing.T) {
	defer func(config, mediaType string) {
		packageParams.config, packageParams.configMediaType = config, mediaType
	}(packageParams.config, packageParams.configMediaType)
	for _, c := range [][2]string{{"config.json", ""}, {"", exampleMediaType}} {
		packageParams.config, packageParams.configMediaType = c[0], c[1]
		if err := pack("package.yaml"); err == nil || !strings.Contains(err.Error(), "--configMediaType") {
			t.Errorf("%v: got %v", c, err)
		}
	}
}
