package workshop

import "github.com/josecabralf/taboo/pkg/internal/agent"

// Shared agent test fixtures for the package-workshop white-box tests (derive,
// materialize). The concrete agent constructors live in internal/agent and are
// reached here through agent.NewProfile, so these tests build profiles through
// mustProfile. The model strings mirror the canonical fixtures the internal/agent
// tests use; they are duplicated here because they live in a different package.
const (
	openCodeModel = "openrouter/qwen/qwen3-coder-plus"
)

// mustProfile resolves an agent by name through agent.NewProfile and panics on
// error. An unknown name is a test bug, never an expected condition, so a panic
// is apt.
func mustProfile(name, model string) agent.AgentProfile {
	p, err := agent.NewProfile(name, model)
	if err != nil {
		panic(err)
	}
	return p
}

// stdinProfile is a minimal AgentProfile naming the opencode SDK but reporting no
// session store (Sessions() ok == false), used to pin the sessionless branch of
// derivation.
type stdinProfile struct{}

func (stdinProfile) Name() string { return "opencode" }
func (stdinProfile) BuildCommand(agent.CommandOptions) agent.AgentCommand {
	return agent.AgentCommand{}
}
func (stdinProfile) CredentialEnvKeys() []string         { return nil }
func (stdinProfile) Sessions() (agent.SessionSpec, bool) { return agent.SessionSpec{}, false }
