package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
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
	for _, header := range []string{"workshops:", "worktrees:", "branches:"} {
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
	if doc.Workshops == nil || doc.Worktrees == nil || doc.Branches == nil {
		t.Errorf("empty sections decoded to nil, want empty slices: %+v", doc)
	}
	if len(doc.Workshops) != 0 || len(doc.Worktrees) != 0 || len(doc.Branches) != 0 {
		t.Errorf("empty listing has non-empty sections: %+v", doc)
	}
	for _, want := range []string{`"workshops": []`, `"worktrees": []`, `"branches": []`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("raw JSON missing %s (must be [] not null):\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "null") {
		t.Errorf("raw JSON contains null; empty sections must serialize as []:\n%s", stdout)
	}
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
