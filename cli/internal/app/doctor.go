package app

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/josecabralf/taboo"
)

// errChecksFailed is the sentinel doctor returns when any check is an error. The
// report is printed to stdout first; executeRoot maps this to a non-zero exit and
// the one trailing `Error:` line on stderr.
var errChecksFailed = errors.New("doctor: one or more checks failed")

// statFileExists is the real existence probe used to discover taboo.yaml.
func statFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// newDoctorCmd builds the `doctor` subcommand.
func newDoctorCmd(env Env) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check host readiness for running taboo",
		Long: "doctor verifies the host has the tooling taboo needs (workshop, LXD, git) " +
			"and, when run inside a taboo project, sanity-checks the resolved config.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			checks := hostChecks(ctx, env)
			checks = append(checks, configChecks(ctx, env, statFileExists, taboo.LoadConfig)...)
			if err := renderReport(env, asJSON, "taboo doctor — host readiness", checks); err != nil {
				return err
			}
			if anyError(checks) {
				return errChecksFailed
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the report as JSON")
	return cmd
}

// renderReport writes the report to env.Stdout under title and surfaces only an
// encoding error; the failure verdict is signaled separately by the caller's
// sentinel; title applies only to the human form.
func renderReport(env Env, asJSON bool, title string, checks []check) error {
	if asJSON {
		return writeJSON(env.Stdout, checks)
	}
	writeHuman(env.Stdout, title, checks)
	return nil
}
