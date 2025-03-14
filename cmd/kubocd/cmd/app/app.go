package app

import (
	"fmt"
	"kubocd/cmd/kubocd/cmd/oci"
	"kubocd/cmd/kubocd/cmd/tgz"
	"kubocd/internal/application"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"os"
	"path"
	"sigs.k8s.io/yaml"
	"strings"
)

func UnmarshalDataFromTgz(tgzPath string, fileName string, data interface{}) error {
	ba, err := tgz.ExtractDataFromTgz(tgzPath, fileName)
	if err != nil {
		return err
	}
	return yaml.UnmarshalStrict(ba, data)
}

func Dump(arg string, workDir string, insecure bool, anonymous bool, output string) error {
	apOriginal := &application.Application{}
	if strings.HasPrefix(arg, "oci://") {
		imageRepo, imageTag, err := oci.DecodeImageUrl(arg)
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
		err = dump(output, "status.yaml", status)
		if err != nil {
			return err
		}
		err = dump(output, "groomed-oci.yaml", apGroomedOci)
		if err != nil {
			return err
		}
	} else {
		// The manifest is a local file
		err := misc.LoadYaml(arg, apOriginal)
		if err != nil {
			return err
		}
	}
	err := dump(output, "original.yaml", apOriginal)
	if err != nil {
		return err
	}
	appContainer := &application.AppContainer{}
	err = appContainer.SetApplication(apOriginal, nil, "0.0.0@sha256:0000000000000000000000000")
	if err != nil {
		return err
	}
	err = dump(output, "groomed.yaml", appContainer.Application)
	if err != nil {
		return err
	}
	err = dump(output, "default-parameters.yaml", appContainer.DefaultParameters)
	if err != nil {
		return err
	}
	err = dump(output, "default-context.yaml", appContainer.DefaultContext)
	if err != nil {
		return err
	}
	return nil
}

func dump(output string, fileName string, ap interface{}) error {
	out := fmt.Sprintf("# ====================================  %s:\n---\n%s\n", fileName, misc.Map2Yaml(ap))
	if output != "" {
		target := path.Join(output, fileName)
		err := os.WriteFile(target, []byte(out), 0644)
		if err != nil {
			return err
		}
		fmt.Printf("Create %s\n", target)
	} else {
		fmt.Print(out)
	}
	return nil
}
