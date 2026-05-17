package manifest

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestUpsertAndByStatus(t *testing.T) {
	store := openTestStore(t)

	now := time.Now()
	if err := store.Upsert(File{
		Path:       "1/A001C001.new.01.mxf",
		Size:       1024,
		ModifiedAt: now,
		Status:     StatusDiscovered,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := store.ByStatus(StatusDiscovered)
	if err != nil {
		t.Fatalf("by status: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if got[0].Path != "1/A001C001.new.01.mxf" {
		t.Errorf("path: got %q", got[0].Path)
	}
	if got[0].Size != 1024 {
		t.Errorf("size: got %d", got[0].Size)
	}
	if got[0].Status != StatusDiscovered {
		t.Errorf("status: got %q", got[0].Status)
	}
}

func TestSetHash(t *testing.T) {
	store := openTestStore(t)

	original := time.Unix(1_700_000_000, 0)
	if err := store.Upsert(File{
		Path:       "1/clip.mxf",
		Size:       512,
		ModifiedAt: original,
		Status:     StatusDiscovered,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	newMtime := time.Unix(1_700_000_500, 0)
	if err := store.SetHash("1/clip.mxf", "deadbeefcafebabe", newMtime); err != nil {
		t.Fatalf("set hash: %v", err)
	}

	got, err := store.ByStatus(StatusDiscovered)
	if err != nil {
		t.Fatalf("by status: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if got[0].Hash != "deadbeefcafebabe" {
		t.Errorf("hash: got %q", got[0].Hash)
	}
	if !got[0].ModifiedAt.Equal(newMtime) {
		t.Errorf("modified_at: got %v want %v", got[0].ModifiedAt, newMtime)
	}
	if got[0].Status != StatusDiscovered {
		t.Errorf("SetHash não deve mexer no status: got %q", got[0].Status)
	}
}

func TestNeedsHash_ReturnsOnlyEmpty(t *testing.T) {
	store := openTestStore(t)
	now := time.Now()

	files := []File{
		{Path: "1/a.mxf", Hash: "aaaa", Size: 1, ModifiedAt: now, Status: StatusDiscovered},
		{Path: "1/b.mxf", Hash: "", Size: 2, ModifiedAt: now, Status: StatusDiscovered},
		{Path: "1/c.mxf", Hash: "cccc", Size: 3, ModifiedAt: now, Status: StatusStaged},
		{Path: "1/d.mxf", Hash: "", Size: 4, ModifiedAt: now, Status: StatusReceived},
	}
	for _, f := range files {
		if err := store.Upsert(f); err != nil {
			t.Fatalf("upsert %s: %v", f.Path, err)
		}
	}

	needs, err := store.NeedsHash()
	if err != nil {
		t.Fatalf("needs hash: %v", err)
	}
	if len(needs) != 2 {
		t.Fatalf("expected 2 rows needing hash, got %d", len(needs))
	}
	seen := map[string]bool{}
	for _, f := range needs {
		seen[f.Path] = true
		if f.Hash != "" {
			t.Errorf("%s returned by NeedsHash but already has hash %q", f.Path, f.Hash)
		}
	}
	if !seen["1/b.mxf"] || !seen["1/d.mxf"] {
		t.Errorf("expected b.mxf and d.mxf, got %v", seen)
	}
}
