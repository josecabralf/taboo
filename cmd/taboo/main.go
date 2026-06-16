// Command taboo is the taboo CLI: it drives host-readiness checks and (later)
// run orchestration through the pkg/taboo library boundary.
package main

import (
	"context"
	"io"
	"os"

	"github.com/spf13/cobra"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

// Env carries the injected dependencies the CLI commands need so command
// behavior is testable without spawning processes or a TTY.
type Env struct {
	// Cmd is the pkg/taboo exec boundary every external probe runs through.
	Cmd taboo.Commander
	// Stdin is the command's input stream.
	Stdin io.Reader
	// Stdout is where reports are written.
	Stdout io.Writer
	// Stderr is where diagnostics are written.
	Stderr io.Writer
	// LookupEnv resolves environment variables; defaults to os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// Getwd reports the working directory; defaults to os.Getwd.
	Getwd func() (string, error)
}

// newRootCmd builds the taboo root command, wires the injected env into its
// streams, and registers every subcommand. SilenceErrors/SilenceUsage keep a
// failed check from dumping cobra usage/error noise; main maps the returned
// error to the exit code.
func newRootCmd(env Env) *cobra.Command {
	root := &cobra.Command{
		Use:           "taboo",
		Short:         "taboo orchestrates agent runs inside workshop environments",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(env.Stdin)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	root.AddCommand(newDoctorCmd(env))
	root.AddCommand(newInitCmd(env))
	root.AddCommand(newValidateCmd(env))
	root.AddCommand(newRunCmd(env))
	return root
}

func main() {
	env := Env{
		Cmd:       taboo.NewExecCommander(),
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		LookupEnv: os.LookupEnv,
		Getwd:     os.Getwd,
	}
	if err := newRootCmd(env).ExecuteContext(context.Background()); err != nil {
		os.Exit(1)
	}
}
