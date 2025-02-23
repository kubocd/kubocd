package misc

import (
	"fmt"
	"io"
	"os"
	"sigs.k8s.io/yaml"
)

func LoadYaml(fileName string, target interface{}) error {
	content, err := os.ReadFile(fileName)
	if err != nil {
		return err
	}
	cnt, err := ExpandEnv(string(content))
	if err != nil {
		return fmt.Errorf("error in '%s': %w", fileName, err)
	}
	return YamlDecode(cnt, target)
}

func YamlDecode(content string, target interface{}) error {
	err := yaml.UnmarshalStrict([]byte(content), target)
	if err != nil {
		if err != io.EOF { // EOF is not an error. Just an empty file (with or without comment)
			return err
		}
	}
	return nil
}
