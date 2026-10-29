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

package pck

import (
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/cmd/kubocd/cmd/tgz"
	"kubocd/internal/archiveio"
	"kubocd/internal/global"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"kubocd/internal/tmpl"
	"path/filepath"
	"strings"
)

func Dump(arg, workDir string, insecure, anonymous, charts bool, output string,
	sourceOpts ...cmn.SourceOptions) error {
	var limits *archiveio.Limits
	if len(sourceOpts) > 0 {
		limits = sourceOpts[0].ArchiveLimits
	}
	work, err := misc.OpenRootedDir(workDir)
	if err != nil {
		return err
	}
	defer func() { _ = work.Root.Close() }()
	if strings.HasPrefix(arg, "oci://") {
		imageRepo, imageTag, err := misc.DecodeImageUrl(arg)
		if err != nil {
			return err
		}
		op := &oci.Operation{WorkDir: work.Path(), ImageRepo: imageRepo, ImageTag: imageTag,
			Insecure: insecure, Anonymous: anonymous}
		archive, err := oci.GetContentFromOci("", op, global.PackageContentMediaType)
		if err != nil {
			return err
		}
		name, err := work.Relative(archive)
		if err != nil {
			return err
		}
		return dumpPackageArchive(work, name, output, charts, limits)
	}
	original, groomed := &kubopackage.Package{}, &kubopackage.Package{}
	if err := misc.LoadYaml(arg, original, groomed); err != nil {
		return err
	}
	if err := groomed.Groom(nil, tmpl.PackageFunctions(false)); err != nil {
		return err
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return err
	}
	var status *kubopackage.Status
	var manifest misc.RootedDir
	if charts {
		manifest, err = misc.PrepareDir(work, "manifest.tar")
		if err != nil {
			return err
		}
		_, status, err = cmn.FetchArchivesAt("", groomed, manifest, work, filepath.Dir(abs), sourceOpts...)
		if err != nil {
			return err
		}
	}
	return dumpPackageOutput(original, nil, status, manifest, output, charts, limits)
}

func dumpPackageArchive(work misc.RootedDir, archive, output string, charts bool, limits ...*archiveio.Limits) error {
	var option *archiveio.Limits
	if len(limits) > 0 {
		option = limits[0]
	}
	manifest, err := misc.PrepareDir(work, "manifest.tar")
	if err != nil {
		return err
	}
	if err := tgz.ExtractAllAt(work, archive, manifest, limits...); err != nil {
		return err
	}
	original, groomed := &kubopackage.Package{}, &kubopackage.Package{}
	status := &kubopackage.Status{}
	if err := misc.LoadArchiveYaml(manifest, "original.yaml", option, original); err != nil {
		return err
	}
	if err := misc.LoadArchiveYaml(manifest, "groomed.yaml", option, groomed); err != nil {
		return err
	}
	if err := misc.LoadArchiveYaml(manifest, "status.yaml", option, status); err != nil {
		return err
	}
	return dumpPackageOutput(original, groomed, status, manifest, output, charts, limits...)
}

func dumpPackageOutput(original, groomed *kubopackage.Package, status *kubopackage.Status,
	manifest misc.RootedDir, output string, charts bool, limits ...*archiveio.Limits) error {
	target, dumpPath, err := misc.PrepareOutput(output, original.Name)
	if err != nil {
		return err
	}
	defer func() { _ = target.Root.Close() }()
	if groomed != nil {
		cmn.Dump(dumpPath, "groomed-oci.yaml", groomed)
	}
	if charts {
		if err := cmn.ExtractPackageCharts(manifest, target, status, limits...); err != nil {
			return err
		}
	}
	if status != nil {
		cmn.Dump(dumpPath, "status.yaml", status)
	}
	cmn.Dump(dumpPath, "original.yaml", original)
	container := &kubopackage.PckContainer{}
	err = container.SetPackage(original, nil, "0.0.0@sha256:0000000000000000000000000", nil, tmpl.PackageFunctions(false))
	cmn.Dump(dumpPath, "groomed.yaml", container.Package)
	cmn.Dump(dumpPath, "default-parameters.yaml", container.DefaultParameters)
	cmn.Dump(dumpPath, "default-context.yaml", container.DefaultContext)
	return err
}
