package taboo

import (
	"context"
	"fmt"
	"path/filepath"
)

// locateAndPlan resolves a *Plan from disk: it discovers the nearest config
// ascending from startDir, loads it, and plans the named workflow with the given
// vars and overrides. A missing config is a distinct error naming startDir. This
// is the shared front half of RunWorkflow and RunWorkflowAs.
func locateAndPlan(startDir, workflow string, vars map[string]string, ov PlanOverrides) (*Plan, error) {
	path, found := FindConfig(startDir)
	if !found {
		return nil, fmt.Errorf("taboo: no taboo.yaml found from %s", startDir)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return cfg.Plan(filepath.Dir(path), workflow, vars, ov)
}

// RunWorkflow is the one-call bridge: it locates the config above startDir, loads
// it, resolves the named workflow into a Plan, and runs it over cmd.
func RunWorkflow(ctx context.Context, startDir, workflow string, vars map[string]string, ov PlanOverrides, cmd Commander) (OrchestratedResult, error) {
	plan, err := locateAndPlan(startDir, workflow, vars, ov)
	if err != nil {
		return OrchestratedResult{}, err
	}
	return plan.Run(ctx, cmd)
}

// RunWorkflowAs is the typed one-call bridge. Like RunWorkflow it locates, loads,
// plans and runs the named workflow, but it threads a JSONResult[T] extractor
// into the plan so the agent's structured output is decoded in-loop and returned
// as a statically typed T, with no caller assertion needed. The generic lives
// only on this free function; a Plan.RunAs[T] method would be illegal Go.
//
// On a locate/load/plan failure it short-circuits before Run, returning the zero
// T and a zero OrchestratedResult. On an extraction failure (ErrNoResult /
// ErrInvalidResult) the Run already happened: the error surfaces with the zero T
// alongside the populated OrchestratedResult.
func RunWorkflowAs[T any](ctx context.Context, startDir, workflow string, vars map[string]string, ov PlanOverrides, cmd Commander) (T, OrchestratedResult, error) {
	var zero T
	plan, err := locateAndPlan(startDir, workflow, vars, ov)
	if err != nil {
		return zero, OrchestratedResult{}, err
	}
	plan.Request.ResultExtractor = JSONResult[T]()
	res, err := plan.Run(ctx, cmd)
	if err != nil {
		return zero, res, err
	}
	typed, _ := res.Result.(T)
	return typed, res, nil
}
