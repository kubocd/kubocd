package app

import (
	"fmt"
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/internal/application"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"sigs.k8s.io/yaml"
	"strings"
)

func UnmarshalDataFromTgz(tgzPath string, fileName string, data interface{}) error {
	ba, err := cmn.ExtractDataFromTgz(tgzPath, fileName)
	if err != nil {
		return err
	}
	return yaml.UnmarshalStrict(ba, data)
}

func Dump(arg string, workDir string, insecure bool, anonymous bool, output string) error {
	apOriginal := &application.Application{}
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
		fmt.Printf("Fetched OCI image content: %s\n", archive)
		apGroomedOci := &application.Application{}
		status := &application.Status{}
		err = UnmarshalDataFromTgz(archive, "original.yaml", &apOriginal)
		if err != nil {
			return err
		}
		err = UnmarshalDataFromTgz(archive, "groomed.yaml", &apGroomedOci)
		if err != nil {
			return err
		}
		err = UnmarshalDataFromTgz(archive, "status.yaml", &status)
		if err != nil {
			return err
		}
		cmn.Dump(output, "status.yaml", status)
		cmn.Dump(output, "groomed-oci.yaml", apGroomedOci)
	} else {
		// The manifest is a local file
		err := misc.LoadYaml(arg, apOriginal)
		if err != nil {
			return err
		}
	}
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
