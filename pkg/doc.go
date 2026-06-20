// Package taboo runs coding agents in isolated, disposable workshops: each run
// gets a fresh git worktree mounted into a workshop, the agent execs against it
// and commits in place, and the result comes back as a typed value. It is the
// public face of the library — everything under internal/ is implementation.
//
// There are three entry patterns, from highest-level to lowest:
//
// One call from a taboo.yaml. RunWorkflow locates the nearest taboo.yaml above a
// directory, resolves the named workflow into a run, and executes it over a
// Commander. RunWorkflowAs[T] does the same and decodes the agent's structured
// output into a typed T, with no caller assertion. This is the bridge most
// adopters want.
//
//	res, err := taboo.RunWorkflow(ctx, ".", "implement", vars, taboo.PlanOverrides{}, taboo.NewExecCommander())
//
// Inspect, then run. LoadConfig parses a taboo.yaml into a ProjectConfig;
// (*ProjectConfig).Plan resolves a workflow plus per-call PlanOverrides into a
// Plan — a pure, inspectable description of one run; (*Plan).Run executes it.
// Reach for this when you want to examine or adjust the resolved run before it
// happens.
//
// Fan out. NewPool builds a Pool from a Config that runs many RunRequests
// concurrently across a bounded set of workshops, returning one RunResult per
// request in input order. The Config for a Pool comes from a resolved Plan.
//
// The Commander interface is the single side-effecting seam: NewExecCommander
// returns the real one, and tests substitute a fake. Agent selection goes
// through NewProfile and the registry helpers (AgentNames, MatchModelFormat).
// Errors are matched with errors.Is against the package's sentinels.
package taboo
