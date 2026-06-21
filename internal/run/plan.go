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
// workflow, then the top-level/defaults layer. Numeric knobs gate on >0; strings
// gate on non-empty. Stdout/Stderr are output sinks (nil = discard), not part of
// the precedence chain.
type PlanOverrides struct {
	Agent            agent.AgentName
	Model            string
	Timeout          time.Duration
	MaxIterations    int
	CompletionSignal string
	Branch           string
	// BaseRef is threaded straight onto RunRequest.BaseRef (a per-call concern with
	// no config/workflow layer); see that field for the behavior. Empty = default.
	BaseRef            string
	From               string
	Prompt, PromptFile string
	Stdout, Stderr     io.Writer
}

// Plan is a resolved, inspectable description of one run: the runner Config, the
// looped Request, the originating workflow name ("" = ad-hoc), and the resolved
// model string (a record of what NewProfile was built with — the field is
// informational; the profile on Config.Agent is what the run actually uses).
// Building it is pure (modulo reading a prompt file); running it via Run is the
// sole side effect.
type Plan struct {
	Config   workshop.Config
	Request  OrchestratedRequest
	Workflow string
	Model    string
}

// Run executes the resolved Plan over cmd, driving the orchestrator loop. It is
// the sole side effect of a Plan: building one is pure (modulo reading a prompt
// file), running it dispatches the workshop/git commands.
func (p *Plan) Run(ctx context.Context, cmd exec.Commander) (OrchestratedResult, error) {
	return NewOrchestrator(New(p.Config, cmd)).Run(ctx, p.Request)
}
