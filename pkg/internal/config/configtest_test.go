package config

// Model fixtures for the package-config white-box tests. The canonical model
// strings mirror those the internal/agent tests use; they are duplicated here
// because config is a different package and these tests resolve profiles through
// the loader/resolver (which calls agent.NewProfile) rather than naming the
// internal agent fixtures directly.
const (
	openCodeModel   = "openrouter/qwen/qwen3-coder-plus"
	claudeCodeModel = "claude-sonnet-4-6"
	copilotModel    = "gpt-5.4"
)
