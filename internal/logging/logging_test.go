package logging

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit_CreatesFilesAndWritesSessionHeader(t *testing.T) {
	dir := t.TempDir()
	cleanup, err := Init(Config{
		Dir:        dir,
		Level:      slog.LevelDebug,
		AppVersion: "test-1.2.3",
	})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer cleanup()

	if SessionID() == "" {
		t.Error("SessionID vazio após Init")
	}

	textPath := filepath.Join(dir, "app.log")
	jsonPath := filepath.Join(dir, "app.jsonl")

	for _, p := range []string{textPath, jsonPath} {
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("arquivo %s não foi criado: %v", p, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("arquivo %s está vazio (session header faltando)", p)
		}
	}

	// Verifica que a primeira linha JSON é o session header parseável.
	jsonBytes, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(jsonBytes)), "\n")
	if len(lines) == 0 {
		t.Fatal("nenhuma linha no app.jsonl")
	}

	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("primeira linha não é JSON válido: %v", err)
	}

	checks := map[string]string{
		"event_id":   "SESSION_START",
		"module":     "session",
		"version":    "test-1.2.3",
		"session_id": SessionID(),
	}
	for k, want := range checks {
		got, _ := header[k].(string)
		if got != want {
			t.Errorf("header[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestOpCorrelationPropagatesViaContext(t *testing.T) {
	dir := t.TempDir()
	cleanup, err := Init(Config{Dir: dir, Level: slog.LevelDebug, AppVersion: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	ctx, opID := NewOp(context.Background())
	if opID == "" {
		t.Fatal("NewOp retornou opID vazio")
	}
	gotID, ok := OpFromContext(ctx)
	if !ok || gotID != opID {
		t.Errorf("OpFromContext: %q,%v want %q,true", gotID, ok, opID)
	}

	slog.InfoContext(ctx, "evento com op", slog.String("module", "test"))

	jsonBytes, _ := os.ReadFile(filepath.Join(dir, "app.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(jsonBytes)), "\n")

	var found bool
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			continue
		}
		if m["msg"] == "evento com op" {
			if m["op_id"] != opID {
				t.Errorf("op_id no log = %v, want %v", m["op_id"], opID)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("evento com op_id não foi encontrado no app.jsonl")
	}
}

func TestSanitizationAppliedAutomatically(t *testing.T) {
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("UserHomeDir vazio neste ambiente")
	}
	dir := t.TempDir()
	cleanup, err := Init(Config{Dir: dir, Level: slog.LevelDebug, AppVersion: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	sensitive := filepath.Join(home, "supersecret", "media.mxf")
	slog.Info("teste de sanitização", slog.String("path", sensitive))

	got, _ := os.ReadFile(filepath.Join(dir, "app.jsonl"))
	asStr := string(got)
	if strings.Contains(asStr, home) {
		t.Errorf("HOME (%q) não foi sanitizado no log:\n%s", home, asStr)
	}
	if !strings.Contains(asStr, "<USER>") {
		t.Errorf("expected <USER> token no log:\n%s", asStr)
	}
}

func TestLevelFiltersWorking(t *testing.T) {
	dir := t.TempDir()
	cleanup, err := Init(Config{Dir: dir, Level: slog.LevelWarn, AppVersion: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	slog.Debug("debug-deve-ser-filtrado")
	slog.Info("info-tambem")
	slog.Warn("warn-deve-aparecer")
	slog.Error("error-tambem")

	got, _ := os.ReadFile(filepath.Join(dir, "app.jsonl"))
	asStr := string(got)
	if strings.Contains(asStr, "debug-deve-ser-filtrado") {
		t.Error("debug emitido com Level=Warn")
	}
	if strings.Contains(asStr, "info-tambem") {
		t.Error("info emitido com Level=Warn")
	}
	if !strings.Contains(asStr, "warn-deve-aparecer") {
		t.Error("warn não foi emitido")
	}
	if !strings.Contains(asStr, "error-tambem") {
		t.Error("error não foi emitido")
	}
}
