package config_test

import (
	"path/filepath"
	"testing"

	"github.com/Lohan-Costa/mc-sinc/internal/config"
)

func TestLoadInexistente(t *testing.T) {
	path := filepath.Join(t.TempDir(), "naoexiste.json")
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load inexistente deveria ser ok (zerado); got %v", err)
	}
	if c.Root != "" {
		t.Errorf("config vazio deveria ter Root vazio; got %q", c.Root)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := config.Persistent{Root: "/Volumes/Avid/MXF"}

	if err := config.Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Root != want.Root {
		t.Errorf("Root: got %q, want %q", got.Root, want.Root)
	}
}

func TestSaveSobrescreve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	_ = config.Save(path, config.Persistent{Root: "/a"})
	_ = config.Save(path, config.Persistent{Root: "/b"})

	got, _ := config.Load(path)
	if got.Root != "/b" {
		t.Errorf("Save deveria sobrescrever; got %q", got.Root)
	}
}
