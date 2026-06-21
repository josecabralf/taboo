package taboo

// Shared agent test fixture for the package-taboo white-box bridge tests
// (plan_test, via testConfig). The concrete agent constructors (OpenCode/
// ClaudeCode/Copilot) moved into internal/agent and are no longer on the public
// facade (NewProfile is the supported entry point), so these tests build
// profiles through mustProfile. The model string mirrors the canonical fixture
// the internal/agent tests use; it is duplicated here because it lives in a
// different package now.
const openCodeModel = "openrouter/qwen/qwen3-coder-plus"

// mustProfile resolves an agent by name through the facade NewProfile and panics
// on error. It is the test stand-in for the de-exported named constructors: an
// unknown name is a test bug, never an expected condition, so a panic is apt.
func mustProfile(name AgentName, model string) AgentProfile {
	p, err := NewProfile(name, model)
	if err != nil {
		panic(err)
	}
	return p
}
