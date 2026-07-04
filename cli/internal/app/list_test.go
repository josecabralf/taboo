package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josecabralf/taboo"
)

// listProjectBody is a minimal valid taboo.yaml the list tests build on. It
// names one agent (opencode) so a per-agent workshop is derivable — list
// enumerates "<workshop>-<agent>", here "demo-opencode". The body carries the
// workshop, base, the shared testRepoPath fixture (an absolute, non-/tmp repo
// path used verbatim), and a branch-prefix in the defaults block. Variant
// bodies are built by string-concatenation off this var, which TestMain assigns
// before any test runs — it must NOT be initialized at package scope because
// testRepoPath is empty until TestMain sets it.
var listProjectBody string

func buildListProjectBody(repo string) string {
	return "workshop: demo\nbase: ubuntu@24.04\nagent: opencode\nmodel: anthropic/claude\nrepo: " + repo + "\ndefaults:\n  branch-prefix: taboo/\n"
}

// listFakeStdout returns a stdout program for the fake commander, built against
// a per-test project root so the worktree-porcelain stdout places the managed
// worktree under <root>/.taboo/worktrees with no package-level mutable state.
// The program supplies the canned host stdout the list probes parse: a realistic
// `workshop info` YAML block (so the status parses to "ready") for any
// workshop-info probe, and empty otherwise. Because the probe arg is a derived
// per-agent name ("demo-opencode"), not the bare base, the match keys on the
// "info" verb alone; the YAML name field is irrelevant since list prints names
// from projectWorkshops and parses only the status. It also answers the git
// worktree-porcelain and for-each-ref probes the worktrees and branches sections
// issue.
func listFakeStdout(root string) func(taboo.Cmd) string {
	return func(c taboo.Cmd) string {
		if c.Name == "workshop" && elemsContain(c.Args, "info") {
			return "name:     demo\nbase:     ubuntu@24.04\nstatus:   ready\nnotes:    --\n"
		}
		if c.Name == "git" && elemsContain(c.Args, "worktree", "list", "--porcelain") {
			// Two entries: a taboo-managed worktree under <projectDir>/worktrees/
			// and the repo's own main checkout (which must be excluded).
			managed := filepath.Join(root, ".taboo", "worktrees", "taboo-fix-123")
			return "worktree " + managed + "\nHEAD abc123\nbranch refs/heads/taboo/fix-123\n\n" +
				"worktree " + testRepoPath + "\nHEAD def456\nbranch refs/heads/main\n\n"
		}
		if c.Name == "git" && elemsContain(c.Args, "for-each-ref") {
			// Short refnames: two taboo-prefixed run branches plus the user's own
			// branches (main, develop) which must be filtered out by the prefix.
			return "main\ntaboo/fix-123\ntaboo/refactor-456\ndevelop\n"
		}
		return ""
	}
}

// listCmd builds a list command with env, runs it with args, and returns the
// captured stdout/stderr buffers and the execute error. It mirrors runCmd.
func listCmd(t *testing.T, env Env, args ...string) (string, string, error) {
	t.Helper()
	cmd := newListCmd(env)
	cmd.SetArgs(args)
	err := cmd.Execute()
	out, _ := env.Stdout.(*bytes.Buffer)
	errBuf, _ := env.Stderr.(*bytes.Buffer)
	if out == nil || errBuf == nil {
		t.Fatal("listCmd: env.Stdout and env.Stderr must be *bytes.Buffer")
	}
	return out.String(), errBuf.String(), err
}

// TestList_WorkshopState asserts list discovers the project config, probes the
// configured workshop's state via `workshop --project <projectDir> info <name>`,
// and reports the workshop and its parsed status.
func TestList_WorkshopState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, listProjectBody)
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	if findInvocation(fake, "workshop", "--project", filepath.Join(root, ".taboo"), "info", "demo-opencode") == nil {
		t.Errorf("no workshop-info probe with the project dir; calls: %v", invocations(fake))
	}
	if !strings.Contains(stdout, "demo-opencode") {
		t.Errorf("stdout missing the derived workshop name:\n%s", stdout)
	}
	if !strings.Contains(stdout, "ready") {
		t.Errorf("stdout missing the workshop status:\n%s", stdout)
	}
}

// TestList_WorkshopNotProvisioned locks the existence-probe contract: when the
// `workshop info` probe errors (the workshop does not exist), that workshop is
// reported with a distinct "not provisioned" state rather than being omitted or
// crashing the listing. A missing workshop is not a command failure.
func TestList_WorkshopNotProvisioned(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, listProjectBody)
	fake := &fakeCommander{
		stdoutFn: listFakeStdout(root),
		errFn: func(c taboo.Cmd) error {
			if c.Name == "workshop" && elemsContain(c.Args, "info") {
				return errors.New("workshop does not exist")
			}
			return nil
		},
	}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil (a missing workshop is not a failure)", err)
	}
	if !strings.Contains(stdout, "demo-opencode") {
		t.Errorf("stdout missing the derived workshop name:\n%s", stdout)
	}
	if !strings.Contains(stdout, "not provisioned") {
		t.Errorf("stdout missing the not-provisioned indicator:\n%s", stdout)
	}
}

// TestList_DerivesPerAgentWorkshops locks the multi-agent contract: taboo
// provisions one workshop per distinct agent (named "<workshop>-<agent>", as
// run launches them), so list must enumerate one derived workshop for each
// distinct agent the config references — the top-level agent plus every workflow
// agent. Here opencode (top level) and claude-code (a workflow) yield
// "demo-opencode" and "demo-claude-code", both probed and both reported "ready".
func TestList_DerivesPerAgentWorkshops(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := listProjectBody + "workflows:\n  refactor:\n    agent: claude-code\n    prompt: refactor it\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	projectDir := filepath.Join(root, ".taboo")
	if findInvocation(fake, "workshop", "--project", projectDir, "info", "demo-claude-code") == nil {
		t.Errorf("no workshop-info probe for demo-claude-code; calls: %v", invocations(fake))
	}
	if findInvocation(fake, "workshop", "--project", projectDir, "info", "demo-opencode") == nil {
		t.Errorf("no workshop-info probe for demo-opencode; calls: %v", invocations(fake))
	}
	section := workshopsSection(stdout)
	if !strings.Contains(section, "demo-claude-code") || !strings.Contains(section, "demo-opencode") {
		t.Errorf("workshops section missing a derived per-agent workshop:\n%s", section)
	}
	if strings.Count(section, "ready") != 2 {
		t.Errorf("both derived workshops should be reported ready:\n%s", section)
	}
}

// TestList_Worktrees locks the worktrees section: list reads `git -C <repo>
// worktree list --porcelain` and reports each worktree taboo manages for this
// project (those under <projectDir>/worktrees/) with its branch and path, while
// excluding worktrees outside that dir such as the repo's main checkout.
func TestList_Worktrees(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, listProjectBody)
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	if findInvocation(fake, "git", "-C", testRepoPath, "worktree", "list", "--porcelain") == nil {
		t.Errorf("no worktree-list porcelain probe against the repo; calls: %v", invocations(fake))
	}
	managed := filepath.Join(root, ".taboo", "worktrees", "taboo-fix-123")
	if !strings.Contains(stdout, managed) {
		t.Errorf("stdout missing the managed worktree path %q:\n%s", managed, stdout)
	}
	if !strings.Contains(stdout, "taboo/fix-123") {
		t.Errorf("stdout missing the managed worktree branch:\n%s", stdout)
	}
	// The repo's main checkout lives outside <projectDir>/worktrees/, so it must
	// not appear in the worktrees section. Asserting on its branch avoids a
	// false positive on the repo path that appears in the probe argv.
	if strings.Contains(worktreesSection(stdout), "main") {
		t.Errorf("worktrees section should exclude the main checkout:\n%s", stdout)
	}
}

// TestList_Branches locks the branches section: list reads `git -C <repo>
// for-each-ref --format=%(refname:short) refs/heads/` and reports only the
// branches under the configured branch-prefix (taboo/), excluding the user's
// own branches such as "main" and "develop".
func TestList_Branches(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, listProjectBody)
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	if findInvocation(fake, "git", "-C", testRepoPath, "for-each-ref", "--format=%(refname:short)", "refs/heads/") == nil {
		t.Errorf("no for-each-ref probe against the repo; calls: %v", invocations(fake))
	}
	if !strings.Contains(stdout, "taboo/fix-123") {
		t.Errorf("stdout missing a taboo-prefixed branch:\n%s", stdout)
	}
	if !strings.Contains(stdout, "taboo/refactor-456") {
		t.Errorf("stdout missing a taboo-prefixed branch:\n%s", stdout)
	}
	// Branches outside the prefix belong to the user, not taboo, and must be
	// excluded. "develop" appears nowhere else, so asserting on full stdout is
	// safe.
	if strings.Contains(stdout, "develop") {
		t.Errorf("branches section should exclude non-prefixed branches:\n%s", stdout)
	}
}

// TestList_GitProbeErrorIsFatal locks the contrast with the workshop section: a
// missing workshop is reported as a "not provisioned" state (non-fatal), but a
// git probe failure means the repo cannot be enumerated, so list fails with a
// wrapped error rather than emitting an empty-but-healthy-looking listing. Each
// subtest fails one git probe and asserts the matching wrapper propagates.
func TestList_GitProbeErrorIsFatal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		failOn  []string
		wantMsg string
	}{
		{"worktree list fails", []string{"worktree", "list"}, "list worktrees in"},
		{"for-each-ref fails", []string{"for-each-ref"}, "list branches in"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTabooProject(t, root, listProjectBody)
			fake := &fakeCommander{
				stdoutFn: listFakeStdout(root),
				errFn: func(c taboo.Cmd) error {
					if c.Name == "git" && elemsContain(c.Args, tc.failOn...) {
						return errors.New("not a git repository")
					}
					return nil
				},
			}
			env := configEnv(t, fake, root, nil)

			_, _, err := listCmd(t, env)
			if err == nil {
				t.Fatalf("list error = nil, want a fatal git-probe error")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q missing wrapper %q", err, tc.wantMsg)
			}
		})
	}
}

// TestList_JSON locks the machine view: with --json, list emits a structured
// document to stdout carrying the same three sections it renders for humans —
// each workshop with its status, the taboo-managed worktrees with branch+path,
// and the prefix-filtered branches — so tooling can consume the listing.
func TestList_JSON(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, listProjectBody)
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env, "--json")
	if err != nil {
		t.Fatalf("list --json error = %v, want nil", err)
	}

	var doc struct {
		Workshops []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"workshops"`
		Worktrees []struct {
			Branch string `json:"branch"`
			Path   string `json:"path"`
		} `json:"worktrees"`
		Branches []string `json:"branches"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}

	if len(doc.Workshops) != 1 || doc.Workshops[0].Name != "demo-opencode" || doc.Workshops[0].Status != "ready" {
		t.Errorf("workshops = %+v, want one {demo-opencode ready}", doc.Workshops)
	}

	managed := filepath.Join(root, ".taboo", "worktrees", "taboo-fix-123")
	found := false
	for _, wt := range doc.Worktrees {
		if wt.Path == managed && wt.Branch == "taboo/fix-123" {
			found = true
		}
	}
	if !found {
		t.Errorf("worktrees missing managed entry {taboo/fix-123 %s}: %+v", managed, doc.Worktrees)
	}

	if !containsStr(doc.Branches, "taboo/fix-123") || !containsStr(doc.Branches, "taboo/refactor-456") {
		t.Errorf("branches missing taboo-prefixed entries: %v", doc.Branches)
	}
	if containsStr(doc.Branches, "develop") {
		t.Errorf("branches should exclude non-prefixed entries: %v", doc.Branches)
	}
}

// containsStr reports whether want is in xs.
func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestList_ReadOnly locks the core invariant: list only ever probes host state
// and never mutates it. Across the full set of probes, none of the recorded
// invocations is a mutating workshop or git verb.
func TestList_ReadOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, listProjectBody)
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	if _, _, err := listCmd(t, env); err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}

	mutating := [][]string{
		{"workshop", "launch"},
		{"workshop", "stop"},
		{"workshop", "start"},
		{"git", "worktree", "add"},
		{"git", "worktree", "remove"},
		{"git", "branch", "-D"},
		{"git", "commit"},
	}
	for _, verb := range mutating {
		if findInvocation(fake, verb...) != nil {
			t.Errorf("list issued a mutating command %v; calls: %v", verb, invocations(fake))
		}
	}
}

// TestList_WorktreeSiblingExcluded locks underDir's separator-boundary guard: a
// worktree whose path is a sibling of the managed root (sharing its prefix as a
// string but not nested under it, e.g. <root>/.taboo/worktrees-extra/foo) must
// be excluded, while the genuinely-managed <root>/.taboo/worktrees/taboo-fix-123
// is included. A naive strings.HasPrefix without the separator would wrongly
// admit the sibling.
func TestList_WorktreeSiblingExcluded(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, listProjectBody)
	managed := filepath.Join(root, ".taboo", "worktrees", "taboo-fix-123")
	sibling := filepath.Join(root, ".taboo", "worktrees-extra", "foo")
	fake := &fakeCommander{stdoutFn: func(c taboo.Cmd) string {
		if c.Name == "git" && elemsContain(c.Args, "worktree", "list", "--porcelain") {
			return "worktree " + managed + "\nHEAD abc123\nbranch refs/heads/taboo/fix-123\n\n" +
				"worktree " + sibling + "\nHEAD def456\nbranch refs/heads/taboo/sibling\n\n"
		}
		return listFakeStdout(root)(c)
	}}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	section := worktreesSection(stdout)
	if !strings.Contains(section, managed) {
		t.Errorf("worktrees section missing the managed worktree %q:\n%s", managed, section)
	}
	// The sibling shares the "worktrees" prefix but is not under it, so the
	// boundary guard must drop it. Assert on its distinct branch to avoid a false
	// positive on the shared path prefix.
	if strings.Contains(section, "taboo/sibling") {
		t.Errorf("worktrees section should exclude the worktrees-extra sibling:\n%s", section)
	}
}

// TestList_DetachedWorktree locks the detached-HEAD fallback: a porcelain entry
// under the managed root with a "detached" line and no "branch refs/heads/..."
// line is still listed, with branch "(detached)" standing in for the missing
// ref.
func TestList_DetachedWorktree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, listProjectBody)
	detached := filepath.Join(root, ".taboo", "worktrees", "taboo-detached")
	fake := &fakeCommander{stdoutFn: func(c taboo.Cmd) string {
		if c.Name == "git" && elemsContain(c.Args, "worktree", "list", "--porcelain") {
			return "worktree " + detached + "\nHEAD abc123\ndetached\n\n"
		}
		return listFakeStdout(root)(c)
	}}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	section := worktreesSection(stdout)
	if !strings.Contains(section, detached) {
		t.Errorf("worktrees section missing the detached worktree path %q:\n%s", detached, section)
	}
	if !strings.Contains(section, "(detached)") {
		t.Errorf("worktrees section missing the (detached) branch fallback:\n%s", section)
	}
}

// TestList_EmptyBranchPrefixReturnsAll locks gatherBranches' empty-prefix
// behavior: with no defaults block the branch-prefix is "", so taboo's run
// branches are indistinguishable from the user's and every branch is returned —
// including the non-prefixed main and develop.
func TestList_EmptyBranchPrefixReturnsAll(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// No defaults block, so cfg.Defaults is nil and the prefix is empty.
	body := "workshop: demo\nbase: ubuntu@24.04\nagent: opencode\nmodel: anthropic/claude\nrepo: " + testRepoPath + "\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	section := branchesSection(stdout)
	for _, want := range []string{"main", "develop", "taboo/fix-123", "taboo/refactor-456"} {
		if !strings.Contains(section, want) {
			t.Errorf("branches section missing %q (empty prefix returns every branch):\n%s", want, section)
		}
	}
}

// TestList_WorkshopStatusUnknown locks parseWorkshopStatus's fallback: when the
// `workshop info` probe succeeds (no error) but returns stdout with no parseable
// status, the workshop renders with status "unknown" rather than crashing or
// being omitted — distinct from the "not provisioned" state a probe error yields.
func TestList_WorkshopStatusUnknown(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, listProjectBody)
	fake := &fakeCommander{stdoutFn: func(c taboo.Cmd) string {
		if c.Name == "workshop" && elemsContain(c.Args, "info") {
			// Non-erroring but statusless output: no "status:" field to parse.
			return "name:     demo\nbase:     ubuntu@24.04\n"
		}
		return listFakeStdout(root)(c)
	}}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	section := workshopsSection(stdout)
	if !strings.Contains(section, "unknown") {
		t.Errorf("workshops section missing the unknown status fallback:\n%s", section)
	}
}

// emptyListingFake programs a fake whose every probe succeeds but returns empty
// stdout, so the worktrees and branches sections come up empty. Paired with a
// config that has no workshop (projectWorkshops returns nil) all three sections
// are empty.
func emptyListingFake() *fakeCommander {
	return &fakeCommander{stdoutFn: func(taboo.Cmd) string { return "" }}
}

// emptyListingBody is a minimal config with workshop "" — projectWorkshops
// returns nil for it, so the workshops section is empty. The repo path (the
// shared testRepoPath fixture) keeps the git probes well-formed. TestMain
// assigns it before any test runs; it must NOT be initialized at package scope
// because testRepoPath is empty until TestMain sets it.
var emptyListingBody string

func buildEmptyListingBody(repo string) string {
	return "workshop: \"\"\nbase: ubuntu@24.04\nagent: opencode\nmodel: anthropic/claude\nrepo: " + repo + "\n"
}

// TestList_EmptyListingHuman locks the human view's empty-section fallback: when
// all three sections are empty (no workshop, empty porcelain, empty
// for-each-ref) each header is followed by the "  (none)" line. Asserting per
// section (not on raw stdout) proves each header got its own fallback rather than
// one leaking across sections.
func TestList_EmptyListingHuman(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, emptyListingBody)
	fake := emptyListingFake()
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	for _, header := range []string{"workshops:", "worktrees:", "branches:", "workflows:"} {
		section := listSection(stdout, header)
		if !strings.Contains(section, "(none)") {
			t.Errorf("section %q missing the (none) fallback:\n%s", header, section)
		}
	}
}

// TestList_EmptyListingJSON locks the machine view's empty shape: with an empty
// listing and --json, each section serializes as the conventional [] rather than
// null, so tooling can index the fields unconditionally. It checks both the
// decoded document (fields present, length zero) and the raw JSON bytes (no
// "null").
func TestList_EmptyListingJSON(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, emptyListingBody)
	fake := emptyListingFake()
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env, "--json")
	if err != nil {
		t.Fatalf("list --json error = %v, want nil", err)
	}

	var doc jsonListResult
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if doc.Workshops == nil || doc.Worktrees == nil || doc.Branches == nil || doc.Workflows == nil {
		t.Errorf("empty sections decoded to nil, want empty slices: %+v", doc)
	}
	if len(doc.Workshops) != 0 || len(doc.Worktrees) != 0 || len(doc.Branches) != 0 || len(doc.Workflows) != 0 {
		t.Errorf("empty listing has non-empty sections: %+v", doc)
	}
	for _, want := range []string{`"workshops": []`, `"worktrees": []`, `"branches": []`, `"workflows": []`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("raw JSON missing %s (must be [] not null):\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "null") {
		t.Errorf("raw JSON contains null; empty sections must serialize as []:\n%s", stdout)
	}
}

// TestList_WorkflowsSection locks the workflows section's tracer path: a
// configured workflow renders one line under the "workflows:" header showing
// its name, its effective agent and model (here both falling back to the
// top-level values, matching referencedAgents/referencedModels precedence),
// and a one-line preview of its inline prompt. The section is pure config —
// no new host probes.
func TestList_WorkflowsSection(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := listProjectBody + "workflows:\n  fix:\n    prompt: fix the bug\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	section := workflowsSection(stdout)
	for _, want := range []string{"fix", "opencode", "anthropic/claude", "fix the bug"} {
		if !strings.Contains(section, want) {
			t.Errorf("workflows section missing %q:\n%s", want, section)
		}
	}
	// The workflows section is pure config: adding it must not add host probes.
	// With one distinct agent the listing issues exactly the pre-existing three
	// probes — workshop info, git worktree list, git for-each-ref.
	if got := len(invocations(fake)); got != 3 {
		t.Errorf("list issued %d probes, want the pre-existing 3 (workflows adds none): %v", got, invocations(fake))
	}
}

// TestList_WorkflowsSortedWithDefaultMarker locks the section's ordering and
// default contract: workflows render sorted by name (not map order), the one
// named by default-workflow carries a "(default)" marker, the others do not,
// and a workflow's own agent/model override the top level on its line.
func TestList_WorkflowsSortedWithDefaultMarker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := listProjectBody +
		"workflows:\n" +
		"  refactor:\n    agent: claude-code\n    model: claude-sonnet-4-5\n    prompt: refactor it\n" +
		"  fix:\n    prompt: fix the bug\n" +
		"default-workflow: fix\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	section := workflowsSection(stdout)
	fixIdx := strings.Index(section, "fix")
	refactorIdx := strings.Index(section, "refactor")
	if fixIdx < 0 || refactorIdx < 0 {
		t.Fatalf("workflows section missing a workflow:\n%s", section)
	}
	if fixIdx > refactorIdx {
		t.Errorf("workflows not sorted by name (fix must precede refactor):\n%s", section)
	}
	if !strings.Contains(section, "fix (default)") {
		t.Errorf("workflows section missing the (default) marker on fix:\n%s", section)
	}
	if strings.Contains(section, "refactor (default)") {
		t.Errorf("(default) marker leaked onto a non-default workflow:\n%s", section)
	}
	// refactor's own agent/model must override the top level on its line.
	refactorLine := lineContaining(section, "refactor")
	if !strings.Contains(refactorLine, "claude-code") || !strings.Contains(refactorLine, "claude-sonnet-4-5") {
		t.Errorf("refactor line missing its own agent/model override: %q", refactorLine)
	}
	if strings.Contains(refactorLine, "opencode") {
		t.Errorf("refactor line should not fall back to the top-level agent: %q", refactorLine)
	}
}

// TestList_WorkflowPromptFileAndPlaceholders locks the prompt-file path: a
// workflow whose prompt lives in a prompt-file (resolved relative to the config
// dir through the injected statFile) renders a one-line promptSummary preview —
// first line plus line count for a multi-line file — and the sorted {{VAR}}
// placeholder names the file references.
func TestList_WorkflowPromptFileAndPlaceholders(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := listProjectBody + "workflows:\n  fix:\n    prompt-file: prompts/fix.md\n"
	writeTabooProject(t, root, body)
	promptDir := filepath.Join(root, ".taboo", "prompts")
	if err := os.MkdirAll(promptDir, 0o750); err != nil {
		t.Fatal(err)
	}
	content := "Fix issue {{ISSUE}} in {{REPO}}\nthen reference {{ISSUE}} in the commit"
	if err := os.WriteFile(filepath.Join(promptDir, "fix.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	section := workflowsSection(stdout)
	if !strings.Contains(section, "Fix issue {{ISSUE}} in {{REPO}} (2 lines)") {
		t.Errorf("workflows section missing the promptSummary preview of the prompt-file:\n%s", section)
	}
	// Placeholders come sorted and deduped: ISSUE (referenced twice) then REPO.
	if !strings.Contains(section, "ISSUE, REPO") {
		t.Errorf("workflows section missing the sorted, deduped placeholders:\n%s", section)
	}
}

// TestList_WorkflowMissingPromptFileDegrades locks the degradation contract: a
// workflow whose prompt-file does not exist still lists — with "prompt:
// (unavailable)" and no placeholders — rather than failing the listing.
// Existence policing stays validate's job (promptFileChecks).
func TestList_WorkflowMissingPromptFileDegrades(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := listProjectBody + "workflows:\n  fix:\n    prompt-file: prompts/ghost.md\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil (a missing prompt-file must not fail the listing)", err)
	}
	section := workflowsSection(stdout)
	if !strings.Contains(section, "fix") {
		t.Errorf("workflows section missing the workflow with the absent prompt-file:\n%s", section)
	}
	if !strings.Contains(section, "prompt: (unavailable)") {
		t.Errorf("workflows section missing the (unavailable) prompt fallback:\n%s", section)
	}
}

// TestList_WorkflowDefaultsPromptFallback locks the defaults-layer rung of the
// list-side prompt contract: a bare workflow with no prompt/prompt-file of its
// own resolves its effective prompt from the config's `defaults: prompt:`
// layer (effectivePrompt's third rung), so its workflows-section line carries
// the defaults prompt preview and its {{VAR}} placeholders rather than the
// "(unavailable)" fallback.
func TestList_WorkflowDefaultsPromptFallback(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// listProjectBody ends inside its defaults block, so the appended prompt
	// line joins that block; the workflow itself is bare.
	body := listProjectBody + "  prompt: fix {{ISSUE}} by default\nworkflows:\n  fix: {}\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env)
	if err != nil {
		t.Fatalf("list error = %v, want nil", err)
	}
	section := workflowsSection(stdout)
	fixLine := lineContaining(section, "fix")
	if !strings.Contains(fixLine, "prompt: fix {{ISSUE}} by default") {
		t.Errorf("fix line missing the defaults-layer prompt preview: %q", fixLine)
	}
	if !strings.Contains(fixLine, "vars: ISSUE") {
		t.Errorf("fix line missing the defaults prompt's placeholder: %q", fixLine)
	}
	if strings.Contains(section, "(unavailable)") {
		t.Errorf("defaults-layer prompt should resolve, not degrade to (unavailable):\n%s", section)
	}
}

// TestList_WorkflowsJSON locks the machine view of the workflows section: with
// --json the document gains a "workflows" array — sorted by name, each entry
// carrying the name, the default flag, the effective agent/model (each field
// falling back to the top level independently, never block-wise), the prompt
// summary with its availability flag, and the placeholder names (empty-slice,
// never null) — while the pre-existing workshops/worktrees/branches sections
// keep the exact shape TestList_JSON locks.
func TestList_WorkflowsJSON(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := listProjectBody +
		"workflows:\n" +
		"  refactor:\n    agent: claude-code\n    model: claude-sonnet-4-5\n    prompt: refactor {{TARGET}}\n" +
		"  polish:\n    model: claude-haiku\n    prompt: polish it\n" +
		"  fix:\n    prompt-file: prompts/ghost.md\n" +
		"default-workflow: refactor\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: listFakeStdout(root)}
	env := configEnv(t, fake, root, nil)

	stdout, _, err := listCmd(t, env, "--json")
	if err != nil {
		t.Fatalf("list --json error = %v, want nil", err)
	}

	var doc jsonListResult
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}

	if len(doc.Workflows) != 3 {
		t.Fatalf("workflows = %+v, want three entries", doc.Workflows)
	}
	fix, polish, refactor := doc.Workflows[0], doc.Workflows[1], doc.Workflows[2]
	if fix.Name != "fix" || polish.Name != "polish" || refactor.Name != "refactor" {
		t.Fatalf("workflows not sorted by name: %+v", doc.Workflows)
	}
	if fix.Default || polish.Default || !refactor.Default {
		t.Errorf("default flags = fix:%v polish:%v refactor:%v, want the marker on refactor only",
			fix.Default, polish.Default, refactor.Default)
	}
	// fix falls back to the top level; its prompt-file is absent so the prompt
	// is unavailable and the placeholders are the empty (never nil) slice.
	if fix.Agent != "opencode" || fix.Model != "anthropic/claude" {
		t.Errorf("fix effective agent/model = %q/%q, want top-level fallback opencode/anthropic-claude", fix.Agent, fix.Model)
	}
	if fix.PromptAvailable || fix.Prompt != "" {
		t.Errorf("fix prompt = %+v, want unavailable with an empty summary", fix)
	}
	if fix.Placeholders == nil || len(fix.Placeholders) != 0 {
		t.Errorf("fix placeholders = %#v, want the empty slice", fix.Placeholders)
	}
	// polish sets only model: each field resolves independently, so the agent
	// falls back to the top level while the model stays its own — a block-wise
	// fallback (agent and model taken together) would leave the agent empty.
	if polish.Agent != "opencode" || polish.Model != "claude-haiku" {
		t.Errorf("polish effective agent/model = %q/%q, want per-field opencode/claude-haiku", polish.Agent, polish.Model)
	}
	// refactor carries its own agent/model, an available inline prompt, and its
	// placeholder.
	if refactor.Agent != "claude-code" || refactor.Model != "claude-sonnet-4-5" {
		t.Errorf("refactor effective agent/model = %q/%q, want its own values", refactor.Agent, refactor.Model)
	}
	if !refactor.PromptAvailable || refactor.Prompt != "refactor {{TARGET}}" {
		t.Errorf("refactor prompt = %+v, want the available inline prompt", refactor)
	}
	if len(refactor.Placeholders) != 1 || refactor.Placeholders[0] != "TARGET" {
		t.Errorf("refactor placeholders = %v, want [TARGET]", refactor.Placeholders)
	}

	// The pre-existing sections keep their shape: adding workflows must not
	// disturb them.
	if len(doc.Workshops) == 0 || len(doc.Worktrees) == 0 || len(doc.Branches) == 0 {
		t.Errorf("pre-existing sections went empty after adding workflows: %+v", doc)
	}
	for _, key := range []string{`"workshops"`, `"worktrees"`, `"branches"`, `"workflows"`} {
		if !strings.Contains(stdout, key) {
			t.Errorf("raw JSON missing the %s key:\n%s", key, stdout)
		}
	}
}

// TestWorkflowLines locks the pure line formatter: the "(default)" marker, the
// agent/model fields, the "(unavailable)" prompt fallback, and the vars suffix
// appearing only when the prompt references placeholders.
func TestWorkflowLines(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		wf   jsonWorkflow
		want string
	}{
		{
			name: "default with placeholders",
			wf: jsonWorkflow{Name: "fix", Default: true, Agent: "opencode", Model: "anthropic/claude",
				Prompt: "fix {{ISSUE}}", PromptAvailable: true, Placeholders: []string{"ISSUE"}},
			want: "fix (default)  agent: opencode  model: anthropic/claude  prompt: fix {{ISSUE}}  vars: ISSUE",
		},
		{
			name: "non-default without placeholders",
			wf: jsonWorkflow{Name: "refactor", Agent: "claude-code", Model: "claude-sonnet-4-5",
				Prompt: "refactor it", PromptAvailable: true, Placeholders: []string{}},
			want: "refactor  agent: claude-code  model: claude-sonnet-4-5  prompt: refactor it",
		},
		{
			name: "unavailable prompt",
			wf:   jsonWorkflow{Name: "fix", Agent: "opencode", Model: "anthropic/claude", Placeholders: []string{}},
			want: "fix  agent: opencode  model: anthropic/claude  prompt: (unavailable)",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := workflowLines([]jsonWorkflow{tc.wf})
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("workflowLines = %q, want [%q]", got, tc.want)
			}
		})
	}
}

// TestGatherWorkflows_InjectedStatFile locks gatherWorkflows' statFile seam
// directly: a prompt-file the injected statFile denies degrades that workflow
// to an unavailable prompt without touching the filesystem outcome for the
// others, and a config with no workflows yields the empty (never nil) slice.
func TestGatherWorkflows_InjectedStatFile(t *testing.T) {
	t.Parallel()
	cfg := &taboo.ProjectConfig{
		Agent: "opencode",
		Model: "anthropic/claude",
		Workflows: map[string]taboo.Workflow{
			"fix": {PromptFile: "prompts/fix.md"},
		},
	}
	denyAll := func(string) bool { return false }
	got := gatherWorkflows(cfg, t.TempDir(), denyAll)
	if len(got) != 1 {
		t.Fatalf("gatherWorkflows = %+v, want one entry", got)
	}
	if got[0].PromptAvailable || got[0].Prompt != "" {
		t.Errorf("denied prompt-file should be unavailable: %+v", got[0])
	}
	if got[0].Placeholders == nil {
		t.Errorf("placeholders must be the empty slice, not nil: %+v", got[0])
	}

	empty := gatherWorkflows(&taboo.ProjectConfig{}, t.TempDir(), denyAll)
	if empty == nil || len(empty) != 0 {
		t.Errorf("gatherWorkflows with no workflows = %#v, want the empty (non-nil) slice", empty)
	}
}

// TestGatherWorkflows_UnsetDefaultMarksNothing locks the guard on the default
// marker: with default-workflow unset, no workflow is flagged as the default —
// not even one with the pathological empty-string name, which would otherwise
// match the unset ("") value and advertise a default a bare `taboo run` would
// refuse to select.
func TestGatherWorkflows_UnsetDefaultMarksNothing(t *testing.T) {
	t.Parallel()
	cfg := &taboo.ProjectConfig{
		Agent: "opencode",
		Model: "anthropic/claude",
		Workflows: map[string]taboo.Workflow{
			"": {Prompt: "hi"},
		},
	}
	got := gatherWorkflows(cfg, t.TempDir(), func(string) bool { return false })
	if len(got) != 1 {
		t.Fatalf("gatherWorkflows = %+v, want one entry", got)
	}
	if got[0].Default {
		t.Errorf("empty-string-named workflow marked (default) with default-workflow unset: %+v", got[0])
	}
}

// lineContaining returns the first line of text containing substr.
func lineContaining(text, substr string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

// workflowsSection returns just the lines under the "workflows:" header, so
// assertions about the workflows listed there cannot false-positive on text
// from other sections (e.g. an agent name in a workshop line).
func workflowsSection(stdout string) string {
	return listSection(stdout, "workflows:")
}

// branchesSection returns just the lines under the "branches:" header, so
// assertions about the branches listed there cannot false-positive on text from
// other sections (e.g. a branch name that also appears in a worktree path).
func branchesSection(stdout string) string {
	return listSection(stdout, "branches:")
}

// worktreesSection returns just the lines under the "worktrees:" header (up to
// the next top-level section or end of output), so assertions about what is
// listed there cannot false-positive on text from other sections.
func worktreesSection(stdout string) string {
	return listSection(stdout, "worktrees:")
}

// workshopsSection returns just the lines under the "workshops:" header, so
// assertions about the workshops listed there cannot false-positive on text from
// other sections (e.g. a "ready" status elsewhere).
func workshopsSection(stdout string) string {
	return listSection(stdout, "workshops:")
}

// listSection returns the indented lines under header (up to the next top-level
// section or end of output).
func listSection(stdout, header string) string {
	lines := strings.Split(stdout, "\n")
	var out []string
	in := false
	for _, line := range lines {
		if line == header {
			in = true
			continue
		}
		if in {
			if line != "" && !strings.HasPrefix(line, "  ") {
				break
			}
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
