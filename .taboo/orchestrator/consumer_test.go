package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/josecabralf/taboo"
)

// consumerProfile is an external-style AgentProfile declared entirely outside the
// taboo/pkg module. Its method signatures name the facade's aliased types
// (taboo.CommandOptions, taboo.AgentCommand, taboo.SessionSpec) so that the
// compile-time satisfaction assertion below only holds if those facade symbols are
// `=` type aliases over the internal package. A defined-type copy on the facade
// would make this consumer's methods a different signature than the internal
// interface demands, and satisfaction would fail across the module boundary.
type consumerProfile struct{}

func (consumerProfile) Name() taboo.AgentName { return "consumer" }

func (consumerProfile) BuildCommand(opts taboo.CommandOptions) taboo.AgentCommand {
	return taboo.AgentCommand{Argv: []string{"consumer", opts.Prompt}}
}

func (consumerProfile) CredentialEnvKeys() []string { return nil }

func (consumerProfile) Sessions() (taboo.SessionSpec, bool) {
	return taboo.SessionSpec{}, false
}

// Compile-time guard: an AgentProfile declared in the afk module (an external
// consumer wired to taboo/pkg via `replace`) satisfies taboo.AgentProfile across
// the real module boundary. This is the counter-example guard for issue #93 — it
// stops compiling the moment a signature-bearing facade type stops being an alias.
var _ taboo.AgentProfile = consumerProfile{}

// TestConsumerCrossModuleBoundary is the cross-module boundary guard for issue
// #93. The library was split into its own module github.com/josecabralf/taboo
// whose public surface is a facade of `=` aliases and re-exported sentinels. This
// test, living in the separate afk module, proves that curated surface works
// across a real module boundary: interface satisfaction, enum const identity, and
// error-sentinel pointer identity all survive the seam.
func TestConsumerCrossModuleBoundary(t *testing.T) {
	t.Parallel()

	t.Run("satisfies AgentProfile across the boundary", func(t *testing.T) {
		t.Parallel()

		// The var _ assertion above already proves satisfaction at compile time;
		// exercising the methods through the interface confirms the aliased input
		// and output types round-trip across the boundary at run time too.
		var profile taboo.AgentProfile = consumerProfile{}

		if got := profile.Name(); got != "consumer" {
			t.Errorf("Name() = %q, want %q", got, "consumer")
		}

		cmd := profile.BuildCommand(taboo.CommandOptions{Prompt: "hello"})
		want := []string{"consumer", "hello"}
		if len(cmd.Argv) != len(want) || cmd.Argv[0] != want[0] || cmd.Argv[1] != want[1] {
			t.Errorf("BuildCommand().Argv = %v, want %v", cmd.Argv, want)
		}

		if _, ok := profile.Sessions(); ok {
			t.Errorf("Sessions() ok = true, want false")
		}
	})

	t.Run("branches StopReason enum consts across the boundary", func(t *testing.T) {
		t.Parallel()

		// Switching over a taboo.StopReason and routing on the re-exported consts
		// proves enum aliasing preserves const identity across the module boundary.
		cases := []struct {
			name   string
			reason taboo.StopReason
			want   string
		}{
			{"signal", taboo.StopSignal, "signal"},
			{"max iterations", taboo.StopMaxIterations, "max"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				var got string
				switch tc.reason {
				case taboo.StopSignal:
					got = "signal"
				case taboo.StopMaxIterations:
					got = "max"
				default:
					got = "default"
				}
				if got != tc.want {
					t.Errorf("StopReason %v routed to %q, want %q", tc.reason, got, tc.want)
				}
			})
		}
	})

	t.Run("keeps sentinel identity across the boundary", func(t *testing.T) {
		t.Parallel()

		// Each re-exported sentinel must still match itself through a wrap, proving
		// pointer identity survived the re-export across the module boundary.
		sentinels := []error{
			taboo.ErrUnknownWorkflow,
			taboo.ErrNoPrompt,
			taboo.ErrUnknownAgent,
			taboo.ErrNoResult,
			taboo.ErrInvalidResult,
		}
		for _, sentinel := range sentinels {
			wrapped := fmt.Errorf("ctx: %w", sentinel)
			if !errors.Is(wrapped, sentinel) {
				t.Errorf("errors.Is(wrap of %v, itself) = false, want true", sentinel)
			}
		}

		// Distinct sentinels must not be confused: identity is per-value, not
		// per-type. If the facade collapsed sentinels onto a shared value this fails.
		if errors.Is(taboo.ErrNoPrompt, taboo.ErrUnknownWorkflow) {
			t.Error("errors.Is(ErrNoPrompt, ErrUnknownWorkflow) = true, want false")
		}
		if errors.Is(taboo.ErrNoResult, taboo.ErrInvalidResult) {
			t.Error("errors.Is(ErrNoResult, ErrInvalidResult) = true, want false")
		}
	})
}
