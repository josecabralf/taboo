package app

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// readBuildInfo is the seam onto runtime/debug.ReadBuildInfo, so the version
// command's output can be driven in tests.
var readBuildInfo = debug.ReadBuildInfo

// newVersionCmd builds the `version` subcommand.
func newVersionCmd(env Env) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the taboo CLI version",
		Long: "version prints the build version of the taboo CLI, read from the binary's embedded " +
			"module info. A binary installed with `go install` reports its module version; a plain " +
			"local build reports \"(devel)\".",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintf(env.Stdout, "taboo %s\n", cliVersion())
			return nil
		},
	}
}

// cliVersion returns the CLI's display version from the binary's embedded build
// info, falling back to `unknown` when it is unavailable or carries no main
// version.
func cliVersion() string {
	info, ok := readBuildInfo()
	if !ok || info.Main.Version == "" {
		return "unknown"
	}
	return info.Main.Version
}
