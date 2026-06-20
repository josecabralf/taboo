package taboo_test

import (
	"context"
	"errors"
	"fmt"

	taboo "github.com/josecabralf/taboo/pkg"
)

// ExamplePool_Run fans several runs out across a bounded set of workshops.
// Results come back in input order; a per-run failure is recorded on
// results[i].Err and does not abort the batch.
//
// There is no // Output: line, so this example is compiled but not executed.
func ExamplePool_Run() {
	agent, err := taboo.NewProfile("opencode", "openrouter/qwen/qwen3-coder-plus")
	if err != nil {
		fmt.Println("unknown agent:", err)
		return
	}

	cfg := taboo.Config{
		Workshop:   "demo",
		Base:       "ubuntu@24.04",
		Agent:      agent,
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
