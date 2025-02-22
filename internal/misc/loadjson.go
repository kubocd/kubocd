package misc

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func LoadJson(fileName string, target interface{}) error {
	content, err := os.ReadFile(fileName)
	if err != nil {
		return err
	}
	cnt, err := ExpandEnv(string(content))
	if err != nil {
		return fmt.Errorf("error in '%s': %w", fileName, err)
	}
	return JsonDecode(cnt, target)
}

func JsonDecode(content string, target interface{}) error {
	err := json.Unmarshal([]byte(content), target)
	if err != nil {
		if err != io.EOF { // EOF is not an error. Just an empty file (with or without comment)
			return err
		}
	}
	return nil

}
