package commit_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
)

// O Service.Commit deve normalizar paths pra forward slash no
// FileSpec — caso contrário, peers Unix recebendo de Windows
// gravariam arquivos com `1\cena01.mxf` no nome.
func TestCommitNormalizesWindowsPathToSlash(t *testing.T) {
	store, err := manifest.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Simula um manifest no Windows: paths com backslash.
	if err := store.Upsert(manifest.File{
		Path:       `1\cena01.mxf`,
		Hash:       "deadbeef00000001",
		Size:       1024,
		ModifiedAt: time.Now(),
		Status:     manifest.StatusStaged,
	}); err != nil {
		t.Fatal(err)
	}

	svc := commit.New(store, "ilha02")
	c, err := svc.Commit(context.Background(), "smoke")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(c.Files) != 1 {
		t.Fatalf("Files=%v, esperava 1", c.Files)
	}
	if c.Files[0].Path != "1/cena01.mxf" {
		t.Errorf("Path=%q, esperava %q (forward slash)", c.Files[0].Path, "1/cena01.mxf")
	}
}

// Sanity: paths que já vêm com / (Mac, Linux) saem inalterados.
func TestCommitPreservaForwardSlash(t *testing.T) {
	store, err := manifest.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Upsert(manifest.File{
		Path:       "1/cena01.mxf",
		Hash:       "deadbeef00000001",
		Size:       1024,
		ModifiedAt: time.Now(),
		Status:     manifest.StatusStaged,
	}); err != nil {
		t.Fatal(err)
	}

	svc := commit.New(store, "mac")
	c, err := svc.Commit(context.Background(), "smoke")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if c.Files[0].Path != "1/cena01.mxf" {
		t.Errorf("Path=%q, esperava inalterado", c.Files[0].Path)
	}
}
