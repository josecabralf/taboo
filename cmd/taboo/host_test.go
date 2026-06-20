package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	taboo "github.com/josecabralf/taboo/pkg"
)

// newHostEnv builds an Env wired to fake for host-check tests: no config is
// discoverable (Getwd points at an empty temp dir) and no env vars are set.
func newHostEnv(t *testing.T, fake *fakeCommander) Env {
	t.Helper()
	return Env{
		Cmd:       fake,
		Stdin:     strings.NewReader(""),
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
		LookupEnv: func(string) (string, bool) { return "", false },
		Getwd:     func() (string, error) { return t.TempDir(), nil },
	}
}

// failOn returns an errFn that fails any command whose Name is in names.
func failOn(names ...string) func(c taboo.Cmd) error {
	set := map[string]struct{}{}
	for _, n := range names {
		set[n] = struct{}{}
	}
	return func(c taboo.Cmd) error {
		if _, bad := set[c.Name]; bad {
			return errors.New(c.Name + " failed")
		}
		return nil
	}
}

// failOnArgs returns an errFn that fails a command matching both Name and a
// specific first arg (e.g. `lxc info` but not `lxc version`).
func failOnArgs(name, firstArg string) func(c taboo.Cmd) error {
	return func(c taboo.Cmd) error {
		if c.Name == name && len(c.Args) > 0 && c.Args[0] == firstArg {
			return errors.New(name + " " + firstArg + " failed")
		}
		return nil
	}
}

// TestHostChecks_Severities table-drives one failing host probe at a time and
// asserts the resulting status of every host check plus whether doctor exits
// non-zero.
func TestHostChecks_Severities(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		errFn      func(c taboo.Cmd) error
		stdoutFn   func(c taboo.Cmd) string
		want       map[string]string // check name -> status token
		wantNonNil bool              // expect errChecksFailed
	}{
		{
			name:     "all ok",
			stdoutFn: okHostStdout,
			want: map[string]string{
				"workshop": "ok", "lxd": "ok", "lxd-reachable": "ok", "git": "ok", "go": "ok",
			},
			wantNonNil: false,
		},
		{
			name: "workshop too old",
			stdoutFn: func(c taboo.Cmd) string {
				if c.Name == "workshop" {
					return "workshop version 0.9.0\n"
				}
				return okHostStdout(c)
			},
			want:       map[string]string{"workshop": "error"},
			wantNonNil: true,
		},
		{
			name: "workshop floor exact ok",
			stdoutFn: func(c taboo.Cmd) string {
				if c.Name == "workshop" {
					return "workshop version 0.9.1-dev-abc\n"
				}
				return okHostStdout(c)
			},
			want:       map[string]string{"workshop": "ok"},
			wantNonNil: false,
		},
		{
			name:       "lxc not installed cascades to reachable",
			stdoutFn:   okHostStdout,
			errFn:      failOn("lxc"),
			want:       map[string]string{"lxd": "error", "lxd-reachable": "error"},
			wantNonNil: true,
		},
		{
			name:       "lxd installed but not reachable",
			stdoutFn:   okHostStdout,
			errFn:      failOnArgs("lxc", "info"),
			want:       map[string]string{"lxd": "ok", "lxd-reachable": "error"},
			wantNonNil: true,
		},
		{
			name:       "git missing is error",
			stdoutFn:   okHostStdout,
			errFn:      failOn("git"),
			want:       map[string]string{"git": "error"},
			wantNonNil: true,
		},
		{
			name:       "go missing is only a warning",
			stdoutFn:   okHostStdout,
			errFn:      failOn("go"),
			want:       map[string]string{"go": "warn"},
			wantNonNil: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeCommander{stdoutFn: tt.stdoutFn, errFn: tt.errFn}
			env := newHostEnv(t, fake)
			out, err := runDoctor(t, env)
			if tt.wantNonNil && !errors.Is(err, errChecksFailed) {
				t.Errorf("doctor error = %v, want errChecksFailed\n%s", err, out)
			}
			if !tt.wantNonNil && err != nil {
				t.Errorf("doctor error = %v, want nil\n%s", err, out)
			}
			for name, wantStatus := range tt.want {
				if got := findStatus(out, name); got != wantStatus {
					t.Errorf("check %q status = %q, want %q\n%s", name, got, wantStatus, out)
				}
			}
		})
	}
}

// TestParseVersion covers the hand-rolled version parser's tolerance of
// prefixes, suffixes, and missing patch, and its rejection of garbage.
func TestParseVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    [3]int
		wantErr bool
	}{
		{in: "workshop version 1.8.0", want: [3]int{1, 8, 0}},
		{in: "1.8.0-abc-dev", want: [3]int{1, 8, 0}},
		{in: "v0.9.1", want: [3]int{0, 9, 1}},
		{in: "2.10", want: [3]int{2, 10, 0}},
		{in: "build 0.9.1 (linux)", want: [3]int{0, 9, 1}},
		{in: "no version here", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := parseVersion(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseVersion(%q) err = nil, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVersion(%q) err = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseVersion(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestVersionLess checks the ordering used to compare against the floor.
func TestVersionLess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b [3]int
		want bool
	}{
		{a: [3]int{0, 9, 0}, b: [3]int{0, 9, 1}, want: true},
		{a: [3]int{0, 9, 1}, b: [3]int{0, 9, 1}, want: false},
		{a: [3]int{1, 0, 0}, b: [3]int{0, 9, 1}, want: false},
		{a: [3]int{0, 8, 9}, b: [3]int{0, 9, 1}, want: true},
	}
	for _, tt := range tests {
		if got := versionLess(tt.a, tt.b); got != tt.want {
			t.Errorf("versionLess(%v,%v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
