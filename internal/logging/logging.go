// Package logging é a infra central de observabilidade do MC Sinc.
//
// Provê logging estruturado (slog) com dois sinks em paralelo:
//
//   - app.log   texto humanamente legível (PT-BR)
//   - app.jsonl JSON Lines parseável (preparado pra IA/automação futura)
//
// Cada log carrega session_id (UUID da sessão atual) e, quando disponível
// via context, operation_id pra correlação de fluxos cross-host.
//
// Rotação automática via lumberjack — 20MB por arquivo, até 10 backups,
// retenção de 30 dias.
//
// Niveis: TRACE / DEBUG / INFO / WARN / ERROR (TRACE é custom = -8).
//
// Uso típico:
//
//	cleanup, err := logging.Init(logging.Config{
//	    Dir:        "$HOME/.mcsinc/logs",
//	    Level:      slog.LevelInfo,
//	    Stderr:     true,
//	    AppVersion: "0.1.0-alpha",
//	})
//	defer cleanup()
//
//	slog.InfoContext(ctx, "Mensagem em PT-BR",
//	    slog.String("module", "sync"),
//	    slog.String("event_id", "PEER_DISCOVERED"))
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"

	"gopkg.in/natefinch/lumberjack.v2"
)

// LevelTrace é o nível mais verboso (abaixo de Debug).
const LevelTrace = slog.Level(-8)

// Config agrupa as opções de inicialização do logger global.
type Config struct {
	// Dir é a pasta onde app.log e app.jsonl moram.
	// Default: filepath.Join(os.UserHomeDir(), ".mcsinc/logs").
	Dir string

	// Level mínimo a ser emitido. Default: slog.LevelInfo.
	Level slog.Level

	// Stderr duplica os logs no stderr (modo dev/CLI).
	Stderr bool

	// MaxFileMB é o tamanho máximo por arquivo antes de rotacionar.
	// Default: 20.
	MaxFileMB int

	// MaxBackups é quantos arquivos rotacionados manter.
	// Default: 10.
	MaxBackups int

	// MaxAgeDays é a retenção máxima em dias dos backups.
	// Default: 30.
	MaxAgeDays int

	// AppVersion é injetado pelo cmd/mcsinc e registrado no header
	// de sessão.
	AppVersion string

	// Root opcional do projeto/repo — paths que comecem com ele são
	// substituídos por <ROOT> nos logs. Útil pra testes com pasta fake.
	Root string
}

// state guarda o estado global de logging entre chamadas a Init e o cleanup.
type state struct {
	sessionID string
	textW     *lumberjack.Logger
	jsonW     *lumberjack.Logger
}

var current *state

// Init configura o logger global. Deve ser chamado uma única vez no main,
// antes de qualquer outro código emitir log. Retorna uma função de cleanup
// que deve ser chamada (via defer) no shutdown.
func Init(cfg Config) (func(), error) {
	if cfg.Dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		cfg.Dir = filepath.Join(home, ".mcsinc", "logs")
	}
	if cfg.MaxFileMB == 0 {
		cfg.MaxFileMB = 20
	}
	if cfg.MaxBackups == 0 {
		cfg.MaxBackups = 10
	}
	if cfg.MaxAgeDays == 0 {
		cfg.MaxAgeDays = 30
	}

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir log dir: %w", err)
	}

	textW := &lumberjack.Logger{
		Filename:   filepath.Join(cfg.Dir, "app.log"),
		MaxSize:    cfg.MaxFileMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   true,
	}
	jsonW := &lumberjack.Logger{
		Filename:   filepath.Join(cfg.Dir, "app.jsonl"),
		MaxSize:    cfg.MaxFileMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   true,
	}

	textOut := io.Writer(textW)
	if cfg.Stderr {
		textOut = io.MultiWriter(textW, os.Stderr)
	}

	replacer := makeReplacer(cfg.Root)
	opts := &slog.HandlerOptions{
		Level:       cfg.Level,
		ReplaceAttr: replacer,
	}

	textH := slog.NewTextHandler(textOut, opts)
	jsonH := slog.NewJSONHandler(jsonW, opts)

	sessID := newSessionID()

	multi := &multiHandler{handlers: []slog.Handler{textH, jsonH}}
	logger := slog.New(multi).With(slog.String("session_id", sessID))
	slog.SetDefault(logger)

	current = &state{
		sessionID: sessID,
		textW:     textW,
		jsonW:     jsonW,
	}

	// Header da sessão como primeira linha (sempre INFO).
	logSessionHeader(cfg.AppVersion)

	cleanup := func() {
		// Loga "encerrando" antes de fechar os writers.
		slog.LogAttrs(context.Background(), slog.LevelInfo,
			"Sessão encerrada",
			slog.String("module", "session"),
			slog.String("event_id", "SESSION_END"),
		)
		_ = textW.Close()
		_ = jsonW.Close()
		current = nil
	}
	return cleanup, nil
}

// SessionID devolve o UUID da sessão atual (vazio se Init ainda não rodou).
func SessionID() string {
	if current == nil {
		return ""
	}
	return current.sessionID
}

// multiHandler delega cada Handle pros handlers internos.
// Injeta op_id do context quando presente.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	if op, ok := OpFromContext(ctx); ok {
		r.AddAttrs(slog.String("op_id", op))
	}
	var firstErr error
	for _, h := range m.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		nh[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: nh}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	nh := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		nh[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: nh}
}

// logSessionHeader é chamado no Init pra deixar registrada a metadata
// de boot (versão, OS, arch, build hash) — facilita triagem em logs
// arquivados quando alguém abrir o app.log meses depois.
func logSessionHeader(appVersion string) {
	buildHash := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				buildHash = s.Value
				break
			}
		}
	}
	slog.LogAttrs(context.Background(), slog.LevelInfo,
		"Sessão iniciada",
		slog.String("module", "session"),
		slog.String("event_id", "SESSION_START"),
		slog.String("version", appVersion),
		slog.String("os", runtime.GOOS),
		slog.String("arch", runtime.GOARCH),
		slog.String("build_hash", buildHash),
	)
}
