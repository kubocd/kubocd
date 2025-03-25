package cmn

import (
	"fmt"
	"kubocd/internal/misc"
	"os"
	"path"
)

func Dump(output string, fileName string, ap interface{}) {
	out := fmt.Sprintf("# ====================================  %s:\n---\n%s\n", fileName, misc.Any2Yaml(ap))
	if output != "" {
		target := path.Join(output, fileName)
		err := os.WriteFile(target, []byte(out), 0644)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Create %s\n", target)
	} else {
		fmt.Print(out)
	}
}
func DumpTxt(output string, fileName string, txt string) {
	out := fmt.Sprintf("# ====================================  %s:\n%s\n", fileName, txt)
	if output != "" {
		target := path.Join(output, fileName)
		err := os.WriteFile(target, []byte(out), 0644)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Create %s\n", target)
	} else {
		fmt.Print(out)
	}
}
