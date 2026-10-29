package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationCutoffPreservesPreviousDurablePointer(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "latest.json")
	if err := atomicWrite(path, []byte("previous")); err != nil {
		t.Fatal(err)
	}
	// Simulate a deadline reached after preparing/fsyncing the new pointer,
	// immediately before its publication transition. Restart must see the old
	// success, with no visible partial or late pointer.
	err := atomicWriteChecked(path, []byte("late"), func() error { return context.DeadlineExceeded })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "previous" {
		t.Fatal("late pointer became visible", string(b), err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatal("cancelled publication leaked staging files", err)
	}
}
