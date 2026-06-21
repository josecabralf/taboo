package run

import "github.com/josecabralf/taboo/internal/agent"

// Shared agent test fixtures for the package-run white-box tests (runner,
// orchestrator, pool, hooks, integration). The concrete agent constructors live
// in internal/agent; these tests build profiles through mustProfile, the run-side
// stand-in. The model strings mirror the canonical fixtures the internal/agent
// tests use; they are duplicated here because they live in a different package.
const (
	openCodeModel   = "openrouter/qwen/qwen3-coder-plus"
	claudeCodeModel = "claude-sonnet-4-6"
	copilotModel    = "gpt-5.4"
)

// mustProfile resolves an agent by name through agent.NewProfile and panics on
// error. An unknown name is a test bug, never an expected condition, so a panic
// is apt.
func mustProfile(name, model string) agent.AgentProfile {
	p, err := agent.NewProfile(agent.AgentName(name), model)
	if err != nil {
		panic(err)
	}
	return p
}
