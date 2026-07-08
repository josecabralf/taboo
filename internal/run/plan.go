package run

import (
	"context"
	"io"
	"time"

	"github.com/josecabralf/taboo/internal/agent"
	"github.com/josecabralf/taboo/internal/exec"
	"github.com/josecabralf/taboo/internal/workshop"
)

// PlanOverrides is the per-call override layer applied on top of the config when
// resolving a Plan. A field's zero value means "unset": fall through to the
// workflow, then the defaults layer. Numeric knobs gate on >0; strings gate on
// non-empty. Stdout/Stderr are output sinks (nil = discard), not part of the
// precedence chain.
type PlanOverrides struct {
	Agent            agent.AgentName
	Model            string
	Timeout          time.Duration
	MaxIterations    int
	CompletionSignal string
	// StopOnNoChange is enable-only, not part of the first-non-zero precedence
	// chain: the effective value ORs this field with the workflow/defaults layers,
	// so a false here cannot disable a config-level enable.
	StopOnNoChange bool
	Branch         string
	// BaseRef is threaded straight onto RunRequest.BaseRef; see that field.
	BaseRef            string
	From               string
	Prompt, PromptFile string
	Stdout, Stderr     io.Writer
}

// Plan is a resolved, inspectable description of one run. Building it is pure
// (modulo reading a prompt file); running it via Run is the sole side effect.
type Plan struct {
	Config   workshop.Config
	Request  OrchestratedRequest
	Workflow string
	// Model records what NewProfile was built with; it is informational, the
	// profile on Config.Agent is what the run uses.
	Model string
	// Placeholders are the sorted, deduped {{VAR}} names of the pre-substitution
	// prompt. Request.Prompt is the post-substitution text, so this is the only
	// record of which variables the template referenced. Empty when none.
	Placeholders []string
}

// Run executes the resolved Plan over cmd, driving the orchestrator loop. It is
// the sole side effect of a Plan.
func (p *Plan) Run(ctx context.Context, cmd exec.Commander) (OrchestratedResult, error) {
	return NewOrchestrator(New(p.Config, cmd)).Run(ctx, p.Request)
}
