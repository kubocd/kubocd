package pck

import (
	"fmt"
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/cmd/kubocd/cmd/tgz"
	"kubocd/internal/global"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"path"
	"path/filepath"
	"strings"
)

func Dump(arg string, workDir string, insecure bool, anonymous bool, charts bool, output string) error {
	pckOriginal := &kubopackage.Package{}
	status := &kubopackage.Status{}
	if strings.HasPrefix(arg, "oci://") {
		imageRepo, imageTag, err := misc.DecodeImageUrl(arg)
		if err != nil {
			return err
		}
		//fmt.Printf("OCI dump of %s:%s\n", imageRepo, imageTag)
		op := &oci.Operation{
			WorkDir:   workDir,
			ImageRepo: imageRepo,
			ImageTag:  imageTag,
			Insecure:  insecure,
			Anonymous: anonymous,
		}

		archive, err := oci.GetContentFromOci("", op, global.PackageContentMediaType)
		if err != nil {
			return err
		}

		tarManifest := path.Join(workDir, "manifest.tar")
		if err = misc.SafeEnsureEmpty(tarManifest); err != nil {
			return err
		}
		err = tgz.ExtractAllFromTgz(archive, tarManifest)
		if err != nil {
			return err
		}
		//fmt.Printf("Fetched OCI image content: %s\n", archive)
		pckGroomedOci := &kubopackage.Package{}
		if err = misc.LoadYaml(path.Join(tarManifest, "original.yaml"), pckOriginal); err != nil {
			return err
		}
		if err = misc.LoadYaml(path.Join(tarManifest, "groomed.yaml"), pckGroomedOci); err != nil {
			return err
		}
		if err = misc.LoadYaml(path.Join(tarManifest, "status.yaml"), status); err != nil {
			return err
		}
		if output != "" {
			output = filepath.Join(output, pckOriginal.Name)
			err := misc.SafeEnsureEmpty(output)
			if err != nil {
				return err
			}
		}
		cmn.Dump(output, "groomed-oci.yaml", pckGroomedOci)
		if charts {
			chartsDir := path.Join(output, "charts")
			for moduleName, chartRef := range status.ChartByModule {
				// fmt.Printf("Expand chart %s\n", chartRef.Name)
				target := path.Join(chartsDir, moduleName)
				fmt.Printf("Create %s\n", target)
				err := tgz.ExtractAllFromTgz(path.Join(tarManifest, fmt.Sprintf("%s-%s.tgz", chartRef.Name, chartRef.Version)), target)
				if err != nil {
					return err
				}
			}
		}
	} else {
		// The manifest is a local file
		pckGroomed := &kubopackage.Package{}
		err := misc.LoadYaml(arg, pckOriginal, pckGroomed)
		if err != nil {
			fmt.Printf("Error loading manifest: %s\n", err)
			return err
		}
		abs, err := filepath.Abs(arg)
		if err != nil {
			return err
		}
		packageFolder := filepath.Dir(abs)

		err = pckGroomed.Groom()
		if err != nil {
			return err
		}
		if output != "" {
			output = filepath.Join(output, pckOriginal.Name)
			err := misc.SafeEnsureEmpty(output)
			if err != nil {
				return err
			}
		}
		if charts {
			tarManifest := path.Join(workDir, "manifest.tar")
			if err = misc.SafeEnsureEmpty(tarManifest); err != nil {
				return err
			}
			_, status, err = cmn.FetchArchives("", pckGroomed, tarManifest, workDir, packageFolder)
			if err != nil {
				return err
			}
			chartsDir := path.Join(output, "charts")
			for moduleName, chartRef := range status.ChartByModule {
				//fmt.Printf("Expand chart %s\n", chartRef.Name)
				target := path.Join(chartsDir, moduleName)
				fmt.Printf("Create %s\n", target)
				err := tgz.ExtractAllFromTgz(path.Join(tarManifest, fmt.Sprintf("%s-%s.tgz", chartRef.Name, chartRef.Version)), target)
				if err != nil {
					return err
				}
			}
		} else {
			status = nil // Flag as un-relevant
		}
	}
	if status != nil {
		cmn.Dump(output, "status.yaml", status)
	}
	cmn.Dump(output, "original.yaml", pckOriginal)

	pckContainer := &kubopackage.PckContainer{}
	err := pckContainer.SetPackage(pckOriginal, nil, "0.0.0@sha256:0000000000000000000000000")
	// We dump even in case of error, to let user have a look.
	cmn.Dump(output, "groomed.yaml", pckContainer.Package)
	cmn.Dump(output, "default-parameters.yaml", pckContainer.DefaultParameters)
	cmn.Dump(output, "default-context.yaml", pckContainer.DefaultContext)

	if err != nil {
		return err
	}
	return nil
}
