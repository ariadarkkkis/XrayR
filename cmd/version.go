package cmd

import (
	"fmt"

	"github.com/xtls/xray-core/main/commands/base"
)

var (
	version  = "0.9.6-25.9.11"
	codename = "XrayR"
)

var cmdVersion = &base.Command{
	UsageLine: `{{.Exec}} version`,
	Short:     `Print current version of XrayR`,
	Long: `
Print current version of XrayR including codename and description.
`,
	Run: executeVersion,
}

func init() {
	cmdVersion.Run = executeVersion // break init loop
}

func executeVersion(cmd *base.Command, args []string) {
	showVersion()
}

func showVersion() {
	fmt.Printf("%s %s \n", codename, version)
}
