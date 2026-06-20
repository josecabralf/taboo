package diffmap

import "testing"

func TestParseRecordsAddedAndContextLines(t *testing.T) {
	t.Parallel()

	// A single hunk that adds two lines after one context line. The new side runs
	// from line 10: context (10), '+' (11), '+' (12). All three are addressable.
	diff := `diff --git a/foo.go b/foo.go
index 111..222 100644
--- a/foo.go
+++ b/foo.go
@@ -10,1 +10,3 @@
 unchanged
+added one
+added two
`
	set := Parse(diff)

	for _, line := range []int{10, 11, 12} {
		if !set.Has("foo.go", line) {
			t.Errorf("Has(foo.go, %d) = false, want true", line)
		}
	}
}

func TestParseTracksEachFileSeparatelyInMultiFileDiff(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,1 +1,2 @@
 keep a
+new a
diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -5,1 +5,2 @@
 keep b
+new b
`
	set := Parse(diff)

	if !set.Has("a.go", 2) {
		t.Error("Has(a.go, 2) = false, want true (added line in first file)")
	}
	if !set.Has("b.go", 6) {
		t.Error("Has(b.go, 6) = false, want true (added line in second file)")
	}
	// The new-side counter must reset per hunk; b.go line 2 is not in b.go's hunk.
	if set.Has("b.go", 2) {
		t.Error("Has(b.go, 2) = true, want false (a.go's line number must not leak)")
	}
}

func TestParseExcludesDeletedAndOutOfDiffLines(t *testing.T) {
	t.Parallel()

	// A pure deletion: the new-side header is /dev/null, so nothing is addressable.
	diff := `diff --git a/gone.go b/gone.go
deleted file mode 100644
index 333..000
--- a/gone.go
+++ /dev/null
@@ -1,2 +0,0 @@
-was one
-was two
diff --git a/edit.go b/edit.go
--- a/edit.go
+++ b/edit.go
@@ -1,3 +1,2 @@
 keep
-removed
 stillhere
`
	set := Parse(diff)

	if set.Has("gone.go", 1) {
		t.Error("Has(gone.go, 1) = true, want false (deleted file has no new side)")
	}
	// edit.go: context 'keep' is new-side line 1, the '-removed' is old-side only,
	// and 'stillhere' is new-side line 2 (the deletion does not advance it).
	if !set.Has("edit.go", 1) {
		t.Error("Has(edit.go, 1) = false, want true (first context line)")
	}
	if !set.Has("edit.go", 2) {
		t.Error("Has(edit.go, 2) = false, want true (context after a deletion)")
	}
	// A line past the hunk was never rendered, so it is out of diff.
	if set.Has("edit.go", 99) {
		t.Error("Has(edit.go, 99) = true, want false (line outside any hunk)")
	}
}

func TestParsePureAdditionsNewFileIgnoresNoNewlineMarker(t *testing.T) {
	t.Parallel()

	// A brand-new file: old side is /dev/null, every body line is an addition. The
	// trailing "\ No newline at end of file" is metadata and must not advance the
	// counter or become an addressable position.
	diff := `diff --git a/new.txt b/new.txt
new file mode 100644
index 000..444
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+first
+second
\ No newline at end of file
`
	set := Parse(diff)

	if !set.Has("new.txt", 1) || !set.Has("new.txt", 2) {
		t.Error("new file additions at lines 1 and 2 must be addressable")
	}
	if set.Has("new.txt", 3) {
		t.Error("Has(new.txt, 3) = true, want false (the \\ No newline marker must not add a line)")
	}
}

func TestParseRecordsAddedLineWhoseContentLooksLikeAHeader(t *testing.T) {
	t.Parallel()

	// An added line whose *content* begins with "++ " renders in the diff as a
	// "+++ ..." line. Inside a hunk it must be classified by its single leading
	// '+' as an added line, not mistaken for a "+++ b/<path>" new-side file header.
	// Otherwise the line is dropped and, worse, the new-side counter desyncs for the
	// rest of the file, silently dropping every later comment there.
	diff := `diff --git a/f.go b/f.go
--- a/f.go
+++ b/f.go
@@ -1,1 +1,3 @@
 ctx
+++ b/x
+after
`
	set := Parse(diff)

	if !set.Has("f.go", 2) {
		t.Error("Has(f.go, 2) = false, want true (the '++ b/x' added line is addressable)")
	}
	if !set.Has("f.go", 3) {
		t.Error("Has(f.go, 3) = false, want true (counter must not desync after a header-like add)")
	}
	if set.Has("x", 2) {
		t.Error("Has(x, 2) = true, want false (a header-like added line must not register a phantom file)")
	}
}

func TestParseToleratesCRLFLineEndings(t *testing.T) {
	t.Parallel()

	// A diff carrying CRLF endings must still key positions by the bare path: a
	// trailing '\r' on the '+++ b/foo.go' header would otherwise make every comment
	// for that file miss the set.
	diff := "diff --git a/foo.go b/foo.go\r\n--- a/foo.go\r\n+++ b/foo.go\r\n@@ -1,1 +1,2 @@\r\n ctx\r\n+added\r\n"
	set := Parse(diff)

	if !set.Has("foo.go", 1) || !set.Has("foo.go", 2) {
		t.Error("CRLF-terminated diff: foo.go:1 and foo.go:2 must both be addressable")
	}
}

func TestParseHandlesPureRenameWithNoContent(t *testing.T) {
	t.Parallel()

	// A 100%-similarity rename carries no hunk, so it addresses no positions.
	diff := `diff --git a/old.go b/new.go
similarity index 100%
rename from old.go
rename to new.go
`
	set := Parse(diff)

	if set.Has("new.go", 1) {
		t.Error("Has(new.go, 1) = true, want false (rename with no content change)")
	}
}
