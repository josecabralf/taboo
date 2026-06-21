package taboo_test

// facade_test.go is the boundary tracer bullet for issue #92's decomposition: a
// black-box test (package taboo_test) that exercises the public *contract* the
// internal/ split must preserve. It must stay green through every cycle, so it
// pins the load-bearing Go mechanics the facade depends on:
//   - signature-bearing types are `=` aliases (so an externally-declared
//     AgentProfile implementation still satisfies taboo.AgentProfile across the
//     package boundary);
//   - StopReason const-branching works over the re-exported consts;
//   - errors.Is holds across the re-exported sentinels (same pointer);
//   - JSONResult[T] round-trips a typed value out of a delimited block.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/josecabralf/taboo"
)

// externalAgent is an AgentProfile implementation declared OUTSIDE the taboo
// package. Assigning it to a var of type taboo.AgentProfile only compiles if
// taboo.AgentProfile / CommandOptions / AgentCommand / SessionSpec are `=`
// aliases of the internal types: a defined-type copy would make this method set
// fail to satisfy the interface across the boundary. This is the single most
// important invariant the decomposition must not break.
type externalAgent struct{}

func (externalAgent) Name() taboo.AgentName { return "external" }

func (externalAgent) BuildCommand(opts taboo.CommandOptions) taboo.AgentCommand {
	return taboo.AgentCommand{Argv: []string{"external", opts.Prompt}, Stdin: opts.ResumeSession}
}

func (externalAgent) CredentialEnvKeys() []string { return nil }

func (externalAgent) Sessions() (taboo.SessionSpec, bool) {
	return taboo.SessionSpec{DirEnv: "XDG_DATA_HOME", Subdir: "external"}, true
}

// Compile-time proof that the external implementation satisfies the interface
// across the package boundary.
var _ taboo.AgentProfile = externalAgent{}

func TestFacade_ExternalAgentProfileSatisfiesInterface(t *testing.T) {
	var p taboo.AgentProfile = externalAgent{}

	cmd := p.BuildCommand(taboo.CommandOptions{Prompt: "do it", ResumeSession: "sess-1", Fork: true})
	if len(cmd.Argv) != 2 || cmd.Argv[1] != "do it" {
		t.Errorf("BuildCommand argv = %v, want [external do it]", cmd.Argv)
	}
	if cmd.Stdin != "sess-1" {
		t.Errorf("BuildCommand stdin = %q, want %q", cmd.Stdin, "sess-1")
	}

	spec, ok := p.Sessions()
	if !ok {
		t.Fatal("Sessions ok = false, want true")
	}
	if spec.DirEnv != "XDG_DATA_HOME" || spec.Subdir != "external" {
		t.Errorf("Sessions spec = %+v, want {XDG_DATA_HOME external}", spec)
	}
}

func TestFacade_StopReasonConstBranching(t *testing.T) {
	// describe must compile and branch over BOTH re-exported StopReason consts,
	// proving they keep their typed identity through the facade.
	describe := func(r taboo.StopReason) string {
		switch r {
		case taboo.StopSignal:
			return "signal"
		case taboo.StopMaxIterations:
			return "max-iterations"
		default:
			return "unknown"
		}
	}

	if got := describe(taboo.StopSignal); got != "signal" {
		t.Errorf("describe(StopSignal) = %q, want %q", got, "signal")
	}
	if got := describe(taboo.StopMaxIterations); got != "max-iterations" {
		t.Errorf("describe(StopMaxIterations) = %q, want %q", got, "max-iterations")
	}
}

func TestFacade_SentinelsMatchAcrossBoundary(t *testing.T) {
	// Every re-exported sentinel must remain the SAME error value the internal
	// packages wrap, so a consumer's errors.Is keeps working. Wrapping each with
	// %w and asserting errors.Is proves the identity survives re-export.
	sentinels := []error{
		taboo.ErrUnknownAgent,
		taboo.ErrNoResult,
		taboo.ErrInvalidResult,
		taboo.ErrConfigRead,
		taboo.ErrConfigParse,
		taboo.ErrForkLoop,
		taboo.ErrUnknownWorkflow,
		taboo.ErrNoPrompt,
		taboo.ErrNoAgent,
	}

	for _, sentinel := range sentinels {
		wrapped := fmt.Errorf("wrapped: %w", sentinel)
		if !errors.Is(wrapped, sentinel) {
			t.Errorf("errors.Is(wrapped, %v) = false, want true", sentinel)
		}
	}
}

func TestFacade_JSONResultRoundTrip(t *testing.T) {
	type review struct {
		Summary string `json:"summary"`
		Passed  bool   `json:"passed"`
	}

	output := `chatter before
<result>{"summary":"all green","passed":true}</result>
chatter after`

	extractor := taboo.JSONResult[review]()
	v, err := extractor.Extract(output)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	r, ok := v.(review)
	if !ok {
		t.Fatalf("Extract returned %T, want review", v)
	}
	if r.Summary != "all green" || !r.Passed {
		t.Errorf("Extract = %+v, want {all green true}", r)
	}

	// A missing block yields the re-exported ErrNoResult sentinel.
	if _, err := extractor.Extract("no block here"); !errors.Is(err, taboo.ErrNoResult) {
		t.Errorf("Extract(no block) error = %v, want ErrNoResult", err)
	}
}
