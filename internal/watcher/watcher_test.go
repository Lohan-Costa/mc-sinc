package watcher_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Lohan-Costa/mc-sinc/internal/watcher"
)

// Garante que arquivos .mxf JÁ existentes na pasta viram Event quando o
// Run arranca. Sem isso, o cross-test.ps1 (que cria o fake antes de
// subir o mcsinc) nunca veria a UI mostrar pendentes.
func TestRunEmitsExistingFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.mxf"), "x")
	mustWrite(t, filepath.Join(dir, "b.mxf"), "y")
	mustWrite(t, filepath.Join(dir, "skip.txt"), "z")

	w, err := watcher.New(dir)
	if err != nil {
		t.Fatalf("watcher.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	got := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case ev := <-w.Events:
			got[filepath.Base(ev.Path)] = true
		case <-deadline:
			t.Fatalf("esperava 2 events de .mxf, recebi %v", got)
		}
	}

	if !got["a.mxf"] || !got["b.mxf"] {
		t.Errorf("faltou um .mxf no set: %v", got)
	}
	if got["skip.txt"] {
		t.Errorf("emitiu evento pra arquivo não-.mxf: %v", got)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}
