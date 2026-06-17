package main

import (
	"errors"
	"strings"

	"github.com/charmbracelet/huh"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

// runWizard collects and confirms agent, model, base, and repo through an
// interactive huh form, prefilling each field from opts and writing the
// confirmed values back into it. It is wired to env.Stdin/env.Stdout and is the
// one part of init that needs a real TTY, so it stays thin and is exercised
// manually rather than in unit tests.
func runWizard(env Env, opts *initOptions) error {
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Agent").
				Options(huh.NewOptions(taboo.AgentNames()...)...).
				Value(&opts.agent),
			huh.NewInput().
				Title("Model").
				Validate(notEmpty("model")).
				Value(&opts.model),
			huh.NewInput().
				Title("Workshop base image").
				Value(&opts.base),
			huh.NewInput().
				Title("Repository path").
				Validate(notEmpty("repository path")).
				Value(&opts.repo),
			huh.NewSelect[string]().
				Title("Seed example workflows (fix, refactor)?").
				Options(huh.NewOption("Yes", ""), huh.NewOption("No", "none")).
				Value(&opts.workflows),
			huh.NewSelect[string]().
				Title("Scaffold a Go main.go?").
				Options(huh.NewOption("No", "none"), huh.NewOption("Single-run", "single"), huh.NewOption("Fan-out", "fanout")).
				Value(&opts.template),
		),
	).WithInput(env.Stdin).WithOutput(env.Stdout)
	if err := form.Run(); err != nil {
		// A deliberate Ctrl-C / Esc out of the form surfaces as a clean
		// "canceled" rather than huh's raw "user aborted".
		if errors.Is(err, huh.ErrUserAborted) {
			return errors.New("canceled")
		}
		return err
	}
	return nil
}

// notEmpty returns a huh validator that rejects a blank (whitespace-only) value,
// naming the field in the error.
func notEmpty(field string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New(field + " is required")
		}
		return nil
	}
}
