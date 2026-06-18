package taboo_test

import (
	"context"
	"errors"
	"fmt"
	"os"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

// ExampleRunner_Run drives one agent run end to end: Setup a fresh worktree on
// the request's branch, then Exec the agent once in it. The agent commits in
// place through the bind-mount, so res.Commit is the host branch's HEAD after
// the run.
//
// There is no // Output: line, so go test compiles this example but does not
// execute it: a real run launches an LXD-backed workshop, which is not
// available under unit tests.
func ExampleRunner_Run() {
	cfg := taboo.Config{
		Workshop:   "demo",
		Base:       "ubuntu@24.04",
		Agent:      taboo.OpenCode("openrouter/qwen/qwen3-coder-plus"),
		RepoPath:   "/home/me/project",
		ProjectDir: "/home/me/project/.taboo",
	}

	runner := taboo.New(cfg, taboo.NewExecCommander())

	res, err := runner.Run(context.Background(), taboo.RunRequest{
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

	cfg := taboo.Config{
		Workshop:   "demo",
		Base:       "ubuntu@24.04",
		Agent:      taboo.OpenCode("openrouter/qwen/qwen3-coder-plus"),
		RepoPath:   "/home/me/project",
		ProjectDir: "/home/me/project/.taboo",
	}

	orch := taboo.NewOrchestrator(taboo.New(cfg, taboo.NewExecCommander()))

	res, err := orch.Run(context.Background(), taboo.OrchestratedRequest{
		RunRequest: taboo.RunRequest{
			Branch: "taboo/refactor",
			Prompt: "Refactor the package. Print DONE and a <result> block when finished.",
			Stdout: os.Stderr,
			Stderr: os.Stderr,
		},
		MaxIterations:    5,
		CompletionSignal: "DONE",
		ResultExtractor:  taboo.JSONResult[review](),
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

// ExamplePool_Run fans several runs out across a bounded set of workshops.
// Results come back in input order; a per-run failure is recorded on
// results[i].Err and does not abort the batch.
//
// There is no // Output: line, so this example is compiled but not executed.
func ExamplePool_Run() {
	cfg := taboo.Config{
		Workshop:   "demo",
		Base:       "ubuntu@24.04",
		Agent:      taboo.OpenCode("openrouter/qwen/qwen3-coder-plus"),
		RepoPath:   "/home/me/project",
		ProjectDir: "/home/me/project/.taboo",
	}

	pool := taboo.NewPool(cfg, 4, taboo.NewExecCommander())

	reqs := []taboo.RunRequest{
		{Branch: "taboo/task-a", Prompt: "Implement task A."},
		{Branch: "taboo/task-b", Prompt: "Implement task B."},
	}

	results, err := pool.Run(context.Background(), reqs)
	if err != nil {
		fmt.Println("batch failed:", err)
		return
	}

	for _, res := range results {
		if res.Err != nil {
			fmt.Printf("%s: failed: %v\n", res.Branch, res.Err)
			continue
		}
		fmt.Printf("%s: %s\n", res.Branch, res.Commit)
	}
}

// ExampleJSONResult decodes the last <result>...</result> block in an agent's
// captured output into a caller struct. JSONResult is a pure function over the
// output string, so this example runs under go test.
func ExampleJSONResult() {
	type review struct {
		Summary string `json:"summary"`
		Passed  bool   `json:"passed"`
	}

	output := `working...
here is the verdict:
<result>{"summary":"all green","passed":true}</result>
`

	extractor := taboo.JSONResult[review]()
	v, err := extractor.Extract(output)
	if err != nil {
		fmt.Println("extract failed:", err)
		return
	}

	r := v.(review)
	fmt.Printf("%s (passed=%t)\n", r.Summary, r.Passed)
	// Output: all green (passed=true)
}

// ExampleJSONResult_validator shows the Validator path: when the decoded type's
// Validate method returns an error, Extract wraps ErrInvalidResult. This
// example runs under go test.
func ExampleJSONResult_validator() {
	output := `<result>{"score":150}</result>`

	extractor := taboo.JSONResult[score]()
	_, err := extractor.Extract(output)
	fmt.Println(errors.Is(err, taboo.ErrInvalidResult))
	// Output: true
}

// score is a result type whose Validate rejects out-of-range values. A type
// that implements taboo.Validator is checked after JSON decoding.
type score struct {
	Score int `json:"score"`
}

func (s score) Validate() error {
	if s.Score < 0 || s.Score > 100 {
		return fmt.Errorf("score %d out of range [0,100]", s.Score)
	}
	return nil
}
