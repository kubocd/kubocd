package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	"kubocd/internal/global"
)

var versionParams struct {
	extended bool
}

func init() {
	versionCmd.PersistentFlags().BoolVar(&versionParams.extended, "extended", false, "Add build number")
}

var versionCmd = cobra.Command{
	Use:   "version",
	Short: "display kcd client version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if versionParams.extended {
			fmt.Printf("%s.%s\n", global.Version, global.BuildTs)
		} else {
			fmt.Printf("%s\n", global.Version)
		}
	},
}
