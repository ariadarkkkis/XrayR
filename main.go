package main

import (
	"os"

	_ "github.com/ariadarkkkis/XrayR/cmd"
	"github.com/xtls/xray-core/main/commands/base"
)

func main() {
	// Check if we have any arguments
	if len(os.Args) == 1 {
		// No arguments - run XrayR with default behavior
		base.RootCommand.Run(base.RootCommand, []string{})
		return
	}

	// Check if first argument starts with dash (flag) - run XrayR
	if len(os.Args) > 1 && os.Args[1][0] == '-' {
		// Parse flags manually and run XrayR
		base.RootCommand.Flag.Parse(os.Args[1:])
		base.RootCommand.Run(base.RootCommand, base.RootCommand.Flag.Args())
		return
	}

	// Otherwise use normal command execution
	base.Execute()
}
