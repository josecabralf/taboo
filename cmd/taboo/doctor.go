package main

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

// errChecksFailed is the sentinel doctor returns when any check is an error. The
// report is fully printed before it is returned; main maps it to a non-zero
// exit. SilenceErrors/SilenceUsage on the root keep cobra from echoing it.
var errChecksFailed = errors.New("doctor: one or more checks failed")

// statFileExists is the real existence probe used to discover taboo.yaml.
func statFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// newDoctorCmd builds the `doctor` subcommand. It gathers the always-on host
// checks plus any config-aware checks, prints a human or --json report to
// env.Stdout, and returns errChecksFailed when any check is an error so the
// process exits non-zero.
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

// renderReport writes the report in the requested format to env.Stdout under
// title and surfaces only an encoding error; the failure verdict is signaled
// separately by the caller via its own sentinel. The JSON document is generic
// (no title), so title applies only to the human form.
func renderReport(env Env, asJSON bool, title string, checks []check) error {
	if asJSON {
		return writeJSON(env.Stdout, checks)
	}
	writeHuman(env.Stdout, title, checks)
	return nil
}
