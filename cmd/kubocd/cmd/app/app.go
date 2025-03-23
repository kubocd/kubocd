package app

import (
	"fmt"
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/internal/application"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"path"
	"path/filepath"
	"strings"
)

func Dump(arg string, workDir string, insecure bool, anonymous bool, charts bool, output string) error {
	apOriginal := &application.Application{}
	status := &application.Status{}
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

		archive, err := oci.GetContentFromOci("", op, global.ApplicationContentMediaType)
		if err != nil {
			return err
		}

		tarManifest := path.Join(workDir, "manifest.tar")
		if err = misc.SafeEnsureEmpty(tarManifest); err != nil {
			return err
		}
		err = cmn.ExtractAllFromTgz(archive, tarManifest)
		if err != nil {
			return err
		}
		//fmt.Printf("Fetched OCI image content: %s\n", archive)
		apGroomedOci := &application.Application{}
		if err = misc.LoadYaml(path.Join(tarManifest, "original.yaml"), apOriginal); err != nil {
			return err
		}
		if err = misc.LoadYaml(path.Join(tarManifest, "groomed.yaml"), apGroomedOci); err != nil {
			return err
		}
		if err = misc.LoadYaml(path.Join(tarManifest, "status.yaml"), status); err != nil {
			return err
		}
		if output != "" {
			output = filepath.Join(output, apOriginal.Metadata.Name)
			err := misc.SafeEnsureEmpty(output)
			if err != nil {
				return err
			}
		}
		cmn.Dump(output, "groomed-oci.yaml", apGroomedOci)
		if charts {
			chartsDir := path.Join(output, "charts")
			for _, chartRef := range status.ChartByModule {
				fmt.Printf("Expand chart %s\n", chartRef.Name)
				err := cmn.ExtractAllFromTgz(path.Join(tarManifest, fmt.Sprintf("%s-%s.tgz", chartRef.Name, chartRef.Version)), chartsDir)
				if err != nil {
					return err
				}
			}
		}
	} else {
		// The manifest is a local file
		appGroomed := &application.Application{}
		err := misc.LoadYaml(arg, apOriginal, appGroomed)
		if err != nil {
			fmt.Printf("Error loading manifest: %s\n", err)
			return err
		}
		err = appGroomed.Groom()
		if err != nil {
			return err
		}
		if output != "" {
			output = filepath.Join(output, apOriginal.Metadata.Name)
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
			_, status, err = cmn.FetchArchives("", appGroomed, tarManifest, workDir)
			if err != nil {
				return err
			}
			chartsDir := path.Join(output, "charts")
			for _, chartRef := range status.ChartByModule {
				fmt.Printf("Expand chart %s\n", chartRef.Name)
				err := cmn.ExtractAllFromTgz(path.Join(tarManifest, fmt.Sprintf("%s-%s.tgz", chartRef.Name, chartRef.Version)), chartsDir)
				if err != nil {
					return err
				}
			}
		}
	}
	cmn.Dump(output, "status.yaml", status)
	cmn.Dump(output, "original.yaml", apOriginal)

	appContainer := &application.AppContainer{}
	err := appContainer.SetApplication(apOriginal, nil, "0.0.0@sha256:0000000000000000000000000")
	// We dump even in case of error, to let user have a look.
	cmn.Dump(output, "groomed.yaml", appContainer.Application)
	cmn.Dump(output, "default-parameters.yaml", appContainer.DefaultParameters)
	cmn.Dump(output, "default-context.yaml", appContainer.DefaultContext)

	if err != nil {
		return err
	}
	return nil
}
