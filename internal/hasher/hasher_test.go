package hasher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
)

func openTestStore(t *testing.T) *manifest.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := manifest.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("the avid never sleeps")
	path := filepath.Join(dir, "clip.mxf")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	want := fmt.Sprintf("%016x", xxhash.Sum64(payload))

	got, mtime, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	if got != want {
		t.Errorf("hash: got %q want %q", got, want)
	}
	if mtime.IsZero() {
		t.Error("mtime: got zero time")
	}
}

func TestHashFile_Missing(t *testing.T) {
	if _, _, err := hashFile(filepath.Join(t.TempDir(), "ghost.mxf")); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestWorkerTick_PopulatesHash(t *testing.T) {
	store := openTestStore(t)
	root := t.TempDir()

	// Cria a subpasta 1 e o arquivo físico.
	if err := os.MkdirAll(filepath.Join(root, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("scenes 12-14")
	rel := filepath.Join("1", "A001C001.new.01.mxf")
	if err := os.WriteFile(filepath.Join(root, rel), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	// Insere o file no manifest sem hash.
	if err := store.Upsert(manifest.File{
		Path:       rel,
		Size:       int64(len(payload)),
		ModifiedAt: time.Now(),
		Status:     manifest.StatusDiscovered,
	}); err != nil {
		t.Fatal(err)
	}

	w := New(store, root)
	w.tick(context.Background())

	got, err := store.ByStatus(manifest.StatusDiscovered)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	want := fmt.Sprintf("%016x", xxhash.Sum64(payload))
	if got[0].Hash != want {
		t.Errorf("hash: got %q want %q", got[0].Hash, want)
	}
	if got[0].Status != manifest.StatusDiscovered {
		t.Errorf("status mudou indevidamente: got %q", got[0].Status)
	}
}

func TestWorkerTick_SkipsAlreadyHashed(t *testing.T) {
	store := openTestStore(t)
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("1", "B.mxf")
	if err := os.WriteFile(filepath.Join(root, rel), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Já tem hash — NeedsHash não deve devolvê-lo.
	if err := store.Upsert(manifest.File{
		Path:       rel,
		Hash:       "preexisting0000",
		Size:       1,
		ModifiedAt: time.Now(),
		Status:     manifest.StatusDiscovered,
	}); err != nil {
		t.Fatal(err)
	}

	w := New(store, root)
	w.tick(context.Background())

	got, _ := store.ByStatus(manifest.StatusDiscovered)
	if len(got) != 1 || got[0].Hash != "preexisting0000" {
		t.Errorf("hash existente foi sobrescrito: %+v", got)
	}
}

func TestWorkerTick_MissingFileDoesNotPanic(t *testing.T) {
	store := openTestStore(t)
	root := t.TempDir() // root existe mas o file não

	if err := store.Upsert(manifest.File{
		Path:       "1/ghost.mxf",
		Size:       1,
		ModifiedAt: time.Now(),
		Status:     manifest.StatusDiscovered,
	}); err != nil {
		t.Fatal(err)
	}

	w := New(store, root)
	w.tick(context.Background()) // não pode entrar em pânico

	got, _ := store.ByStatus(manifest.StatusDiscovered)
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if got[0].Hash != "" {
		t.Errorf("file inexistente não deve receber hash: got %q", got[0].Hash)
	}
}
