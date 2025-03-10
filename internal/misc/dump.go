package misc

import (
	"os"
	"path"
)

func DumpBytes(basePath string, name string, text []byte) {
	f := path.Join(basePath, name)
	err := os.WriteFile(f, []byte(text), 0644)
	if err != nil {
		panic(err) // Should not occurs, as we used safetmp package
	}
}

func DumpYaml(basePath string, name string, data interface{}) {
	DumpBytes(basePath, name, Map2Yaml(data))
}
