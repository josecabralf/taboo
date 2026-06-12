package taboo

import (
	"strings"
	"testing"
)

func TestSignalScanner_DetectsInSingleWrite(t *testing.T) {
	var dst strings.Builder
	s := NewSignalScanner("DONE", &dst)

	if _, err := s.Write([]byte("work finished: DONE here")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if !s.Found() {
		t.Error("Found() = false, want true (sentinel present in write)")
	}
	if dst.String() != "work finished: DONE here" {
		t.Errorf("passthrough = %q, want full input echoed", dst.String())
	}
}

func TestSignalScanner_DetectsAcrossWrites(t *testing.T) {
	var dst strings.Builder
	s := NewSignalScanner("DONE", &dst)

	// Sentinel is split across three writes; each write alone contains no match.
	for _, chunk := range []string{"...DO", "N", "E..."} {
		if _, err := s.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write(%q): %v", chunk, err)
		}
	}

	if !s.Found() {
		t.Error("Found() = false, want true (sentinel split across writes)")
	}
	if dst.String() != "...DONE..." {
		t.Errorf("passthrough = %q, want %q", dst.String(), "...DONE...")
	}
}

func TestSignalScanner_AbsentNotDetected(t *testing.T) {
	var dst strings.Builder
	s := NewSignalScanner("DONE", &dst)

	for _, chunk := range []string{"all ", "good ", "here, no signal"} {
		if _, err := s.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	if s.Found() {
		t.Error("Found() = true, want false (sentinel never written)")
	}
	if dst.String() != "all good here, no signal" {
		t.Errorf("passthrough = %q, want full input echoed", dst.String())
	}
}

func TestSignalScanner_WriteReturnsFullLength(t *testing.T) {
	s := NewSignalScanner("DONE", nil) // nil dst must be safe (discards)
	p := []byte("DONE and more")

	n, err := s.Write(p)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(p) {
		t.Errorf("n = %d, want %d (must report all bytes consumed)", n, len(p))
	}
}

func TestSignalScanner_EmptySignalNeverDetected(t *testing.T) {
	var dst strings.Builder
	s := NewSignalScanner("", &dst)

	if _, err := s.Write([]byte("anything at all")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if s.Found() {
		t.Error("Found() = true, want false (empty signal is disabled)")
	}
	if dst.String() != "anything at all" {
		t.Errorf("passthrough = %q, want input echoed", dst.String())
	}
}
