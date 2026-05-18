package watcher_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/Lohan-Costa/mc-sinc/internal/watcher"
)

// Garante que arquivos Avid (.mxf, .mdb, .pmr) JÁ existentes na pasta
// viram Event quando o Run arranca. Sem isso, o cross-test.ps1 nunca
// veria a UI mostrar pendentes; e o .mdb/.pmr do Avid não viajariam
// pro peer.
func TestRunEmitsExistingAvidFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.mxf"), "x")
	mustWrite(t, filepath.Join(dir, "msmMMOB.mdb"), "i")
	mustWrite(t, filepath.Join(dir, "msmFMID.pmr"), "p")
	mustWrite(t, filepath.Join(dir, "README.txt"), "ignore")
	mustWrite(t, filepath.Join(dir, ".DS_Store"), "ignore")

	w, err := watcher.New(dir)
	if err != nil {
		t.Fatalf("watcher.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	got := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(got) < 3 {
		select {
		case ev := <-w.Events:
			got[filepath.Base(ev.Path)] = true
		case <-deadline:
			t.Fatalf("esperava 3 events Avid, recebi %v", got)
		}
	}

	for _, want := range []string{"a.mxf", "msmMMOB.mdb", "msmFMID.pmr"} {
		if !got[want] {
			t.Errorf("faltou %s no set: %v", want, got)
		}
	}
	for _, skip := range []string{"README.txt", ".DS_Store"} {
		if got[skip] {
			t.Errorf("emitiu evento pra arquivo nao-Avid %s: %v", skip, got)
		}
	}
}

// Apagar arquivo da pasta deve emitir Event com Op == Remove. Sem
// isso, o manifest fica com paths fantasmas.
func TestWatcherEmiteRemoveEmDelete(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "vai-sumir.mxf")
	mustWrite(t, target, "x")

	w, err := watcher.New(dir)
	if err != nil {
		t.Fatalf("watcher.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Drena o Create inicial do emitExisting.
	select {
	case <-w.Events:
	case <-time.After(2 * time.Second):
		t.Fatal("nao recebeu o Create inicial")
	}

	if err := os.Remove(target); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// fsnotify pode demorar uns ms.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-w.Events:
			if ev.Path == target && ev.Op&fsnotifyRemoveMask() != 0 {
				return
			}
			// outros eventos no caminho (ex: Write antes do Remove em
			// alguns OSs) — segue lendo.
		case <-deadline:
			t.Fatal("nao recebeu Event de Remove em 2s")
		}
	}
}

// fsnotifyRemoveMask devolve o bitmask de Remove na lib fsnotify, sem
// poluir o import principal do teste com o package fsnotify direto.
func fsnotifyRemoveMask() fsnotify.Op {
	return fsnotify.Remove | fsnotify.Rename
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}
