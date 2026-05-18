package fsbrowse_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lohan-Costa/mc-sinc/internal/fsbrowse"
)

func TestListSubdirs(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "Avid MediaFiles"))
	mustMkdir(t, filepath.Join(dir, "outra-pasta"))
	mustMkdir(t, filepath.Join(dir, ".escondida"))
	mustWrite(t, filepath.Join(dir, "arquivo.txt"), "x") // não é dir

	r, err := fsbrowse.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range r.Entries {
		names[e.Name] = true
	}
	if !names["Avid MediaFiles"] || !names["outra-pasta"] {
		t.Errorf("esperava Avid MediaFiles + outra-pasta; got %v", names)
	}
	if names[".escondida"] {
		t.Errorf("escondidas nao deveriam aparecer")
	}
	if names["arquivo.txt"] {
		t.Errorf("arquivos nao deveriam aparecer (so dirs)")
	}

	// Sinalização IsAvidMediaFiles
	for _, e := range r.Entries {
		if e.Name == "Avid MediaFiles" && !e.IsAvidMediaFiles {
			t.Errorf("Avid MediaFiles deveria ter IsAvidMediaFiles=true")
		}
	}
}

func TestValidateAvidRootSucesso(t *testing.T) {
	base := t.TempDir()
	amf := filepath.Join(base, "Avid MediaFiles")
	mxf := filepath.Join(amf, "MXF")
	mustMkdir(t, mxf)

	root, err := fsbrowse.ValidateAvidRoot(amf)
	if err != nil {
		t.Fatalf("ValidateAvidRoot: %v", err)
	}
	if root != mxf {
		t.Errorf("root=%q, esperava %q", root, mxf)
	}
}

func TestValidateAvidRootNomeErrado(t *testing.T) {
	base := t.TempDir()
	wrong := filepath.Join(base, "nao-eh-avid")
	mustMkdir(t, filepath.Join(wrong, "MXF"))

	_, err := fsbrowse.ValidateAvidRoot(wrong)
	if err == nil {
		t.Fatal("esperava erro pra pasta com nome errado")
	}
	if !strings.Contains(err.Error(), "Avid MediaFiles") {
		t.Errorf("mensagem deveria citar Avid MediaFiles; got %v", err)
	}
}

func TestValidateAvidRootSemMxf(t *testing.T) {
	base := t.TempDir()
	amf := filepath.Join(base, "Avid MediaFiles")
	mustMkdir(t, amf) // sem MXF dentro

	_, err := fsbrowse.ValidateAvidRoot(amf)
	if err == nil {
		t.Fatal("esperava erro sem subpasta MXF")
	}
	if !strings.Contains(err.Error(), "MXF") {
		t.Errorf("mensagem deveria citar MXF; got %v", err)
	}
}

func TestValidateAvidRootInexistente(t *testing.T) {
	_, err := fsbrowse.ValidateAvidRoot(filepath.Join(t.TempDir(), "nao-existe"))
	if err == nil {
		t.Fatal("esperava erro pra path inexistente")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
