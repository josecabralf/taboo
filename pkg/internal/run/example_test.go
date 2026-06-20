package run_test

import (
	"context"
	"fmt"
	"os"

	"github.com/josecabralf/taboo/pkg/internal/agent"
	"github.com/josecabralf/taboo/pkg/internal/exec"
	"github.com/josecabralf/taboo/pkg/internal/result"
	"github.com/josecabralf/taboo/pkg/internal/run"
	"github.com/josecabralf/taboo/pkg/internal/workshop"
)

// ExampleRunner_Run drives one agent run end to end: Setup a fresh worktree on
// the request's branch, then Exec the agent once in it. The agent commits in
// place through the bind-mount, so res.Commit is the host branch's HEAD after
// the run. Runner is internal: library callers reach it through the public
// bridge (RunWorkflow / Plan.Run); this example documents the run package itself.
//
// There is no // Output: line, so go test compiles this example but does not
// execute it: a real run launches an LXD-backed workshop, which is not
// available under unit tests.
func ExampleRunner_Run() {
	profile, err := agent.NewProfile("opencode", "openrouter/qwen/qwen3-coder-plus")
	if err != nil {
		fmt.Println("unknown agent:", err)
		return
	}

	cfg := workshop.Config{
		Workshop:   "demo",
		Base:       "ubuntu@24.04",
		Agent:      profile,
		RepoPath:   "/home/me/project",
		ProjectDir: "/home/me/project/.taboo",
	}

	runner := run.New(cfg, exec.NewExecCommander())

	res, err := runner.Run(context.Background(), run.RunRequest{
		Branch: "taboo/fix-typos",
		Prompt: "Fix the typos in README.md and commit.",
		Stdout: os.Stderr,
		Stderr: os.Stderr,
	})
	if err != nil {
		fmt.Println("run failed:", err)
		return
	}

	fmt.Println("branch:", res.Branch)
	fmt.Println("commit:", res.Commit)
}

// ExampleOrchestrator_Run re-runs the agent in one worktree until it prints the
// completion signal or MaxIterations is reached, then decodes a typed result
// from the final output with a ResultExtractor.
//
// There is no // Output: line, so this example is compiled but not executed.
func ExampleOrchestrator_Run() {
	// review is the caller's result schema: the agent is asked to emit it as a
	// JSON payload inside a <result>...</result> block.
	type review struct {
		Summary string `json:"summary"`
		Passed  bool   `json:"passed"`
	}

	profile, err := agent.NewProfile("opencode", "openrouter/qwen/qwen3-coder-plus")
	if err != nil {
		fmt.Println("unknown agent:", err)
		return
	}

	cfg := workshop.Config{
		Workshop:   "demo",
		Base:       "ubuntu@24.04",
		Agent:      profile,
		RepoPath:   "/home/me/project",
		ProjectDir: "/home/me/project/.taboo",
	}

	orch := run.NewOrchestrator(run.New(cfg, exec.NewExecCommander()))

	res, err := orch.Run(context.Background(), run.OrchestratedRequest{
		RunRequest: run.RunRequest{
			Branch: "taboo/refactor",
			Prompt: "Refactor the package. Print DONE and a <result> block when finished.",
			Stdout: os.Stderr,
			Stderr: os.Stderr,
		},
		MaxIterations:    5,
		CompletionSignal: "DONE",
		ResultExtractor:  result.JSONResult[review](),
	})
	if err != nil {
		fmt.Println("run failed:", err)
		return
	}

	fmt.Println("iterations:", res.Iterations)
	fmt.Println("stop reason:", res.StopReason)
	if r, ok := res.Result.(review); ok {
		fmt.Println("summary:", r.Summary)
	}
}
