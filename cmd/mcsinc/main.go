// Command mcsinc é o ponto de entrada do MC Sinc.
//
// Sobe, em um único binário:
//   - manifest local (SQLite)
//   - watcher da pasta MXF
//   - serviço de commit
//   - discovery mDNS
//   - servidor HTTP local (API + UI web)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Lohan-Costa/mc-sinc/internal/api"
	"github.com/Lohan-Costa/mc-sinc/internal/avid"
	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	"github.com/Lohan-Costa/mc-sinc/internal/discovery"
	"github.com/Lohan-Costa/mc-sinc/internal/hasher"
	logpkg "github.com/Lohan-Costa/mc-sinc/internal/logging"
	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
	"github.com/Lohan-Costa/mc-sinc/internal/transport/lan"
	"github.com/Lohan-Costa/mc-sinc/internal/watcher"
	"github.com/Lohan-Costa/mc-sinc/internal/web"
)

const version = "0.1.0-alpha"

func main() {
	var (
		root        = flag.String("root", "", "raiz da pasta MXF do Avid; auto-detectado se omitido")
		user        = flag.String("user", defaultUser(), "identificador deste editor na rede")
		port        = flag.Int("port", 7777, "porta do servidor HTTP local")
		dbP         = flag.String("db", defaultDBPath(), "caminho do SQLite local")
		avidProcess = flag.String("avid-process-name", avid.DefaultProcessName,
			"nome do processo do Avid usado pra detecção (ex: 'AvidMediaComposer.exe' no Windows)")
		logLevel = flag.String("log-level", "info",
			"verbosidade dos logs: trace, debug, info, warn, error")
		logDir = flag.String("log-dir", defaultLogDir(),
			"pasta onde app.log e app.jsonl moram")
	)
	flag.Parse()

	// Bootstrap do logger ANTES de qualquer outra coisa — assim erros de
	// startup (auto-discovery, etc.) já entram no log estruturado.
	cleanupLog, err := logpkg.Init(logpkg.Config{
		Dir:        *logDir,
		Level:      parseLevel(*logLevel),
		Stderr:     true,
		AppVersion: version,
		Root:       *root,
	})
	if err != nil {
		log.Fatalf("logging.Init: %v", err)
	}
	defer cleanupLog()

	if *root == "" {
		chosen, err := autoDiscoverRoot()
		if err != nil {
			slog.Error("auto-discovery falhou",
				slog.String("module", "main"),
				slog.String("event_id", "STARTUP_NO_ROOT"),
				slog.String("error", err.Error()))
			os.Exit(1)
		}
		*root = chosen
	}

	if err := os.MkdirAll(filepath.Dir(*dbP), 0o755); err != nil {
		slog.Error("não consegui criar diretório do db",
			slog.String("module", "main"), slog.String("error", err.Error()))
		os.Exit(1)
	}

	store, err := manifest.Open(*dbP)
	if err != nil {
		slog.Error("falha abrindo manifest", slog.String("module", "main"), slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer store.Close()

	commits := commit.New(store, *user)

	disc := discovery.New(*user, *port, version)

	// O watcher acompanha a subpasta do usuário corrente dentro de MXF.
	// Convencão: cada editor edita em MXF/<numero> — começamos por MXF/1
	// e expandimos isso quando suportarmos múltiplas pastas locais.
	localFolder := filepath.Join(*root, "1")

	w, err := watcher.New(localFolder)
	if err != nil {
		slog.Error("falha criando watcher", slog.String("module", "main"), slog.String("error", err.Error()))
		os.Exit(1)
	}

	h := hasher.New(store, *root)

	tport := lan.New(*user, *port, *root, store, disc)

	webRoot, err := web.FS()
	if err != nil {
		slog.Error("falha preparando UI", slog.String("module", "main"), slog.String("error", err.Error()))
		os.Exit(1)
	}

	srv := api.New(api.Config{
		User:        *user,
		Root:        *root,
		Version:     version,
		Store:       store,
		Commits:     commits,
		Discovery:   disc,
		Transport:   tport,
		Web:         webRoot,
		AvidProcess: *avidProcess,
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	httpSrv := &http.Server{
		Addr:              ":" + itoa(*port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("MC Sinc iniciado",
			slog.String("module", "main"),
			slog.String("event_id", "HTTP_LISTEN"),
			slog.String("version", version),
			slog.String("user", *user),
			slog.String("root", *root),
			slog.Int("port", *port))
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("falha no servidor HTTP",
				slog.String("module", "main"),
				slog.String("event_id", "HTTP_FAIL"),
				slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	go func() {
		if err := disc.Run(ctx); err != nil {
			slog.Warn("discovery encerrou com erro",
				slog.String("module", "discovery"),
				slog.String("event_id", "DISCOVERY_FAIL"),
				slog.String("error", err.Error()))
		}
	}()

	go func() {
		if err := w.Run(ctx); err != nil {
			slog.Warn("watcher encerrou com erro",
				slog.String("module", "watcher"),
				slog.String("event_id", "WATCHER_FAIL"),
				slog.String("error", err.Error()))
		}
	}()

	go func() {
		if err := h.Run(ctx); err != nil {
			slog.Warn("hasher encerrou com erro",
				slog.String("module", "hasher"),
				slog.String("event_id", "HASHER_FAIL"),
				slog.String("error", err.Error()))
		}
	}()

	// Drena eventos do watcher e os registra como `discovered` no manifest.
	go func() {
		for ev := range w.Events {
			rel, _ := filepath.Rel(*root, ev.Path)
			info, err := os.Stat(ev.Path)
			if err != nil {
				continue
			}
			_ = store.Upsert(manifest.File{
				Path:       rel,
				Size:       info.Size(),
				ModifiedAt: info.ModTime(),
				Status:     manifest.StatusDiscovered,
			})
			slog.Info("arquivo descoberto pelo watcher",
				slog.String("module", "watcher"),
				slog.String("event_id", "FILE_DISCOVERED"),
				slog.String("path", rel))
		}
	}()

	<-ctx.Done()
	slog.Info("encerrando", slog.String("module", "main"), slog.String("event_id", "SHUTDOWN"))

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = httpSrv.Shutdown(shutdownCtx)
}

func defaultUser() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "mcsinc-user"
}

func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "manifest.db"
	}
	return filepath.Join(home, ".mcsinc", "manifest.db")
}

func defaultLogDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".mcsinc-logs"
	}
	return filepath.Join(home, ".mcsinc", "logs")
}

func parseLevel(s string) slog.Level {
	switch s {
	case "trace":
		return logpkg.LevelTrace
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// autoDiscoverRoot escaneia volumes conectados procurando pela estrutura
// "Avid MediaFiles/MXF" na raiz de cada um. Devolve o path do candidato
// com .mdb mais recente. Os outros encontrados ficam só em log informativo.
func autoDiscoverRoot() (string, error) {
	cands, err := avid.DiscoverRoots(avid.Discovery{ListVolumes: avid.ListVolumesPlatform})
	if err != nil {
		return "", fmt.Errorf("auto-discovery: %v", err)
	}
	if len(cands) == 0 {
		return "", errors.New(
			"nenhum 'Avid MediaFiles' encontrado nos volumes conectados. " +
				"Conecte um disco com a estrutura, ou passe --root manual.")
	}
	best := cands[0]
	slog.Info("auto-discovery escolheu o volume mais recente",
		slog.String("module", "main"),
		slog.String("event_id", "AUTO_DISCOVERY_PICKED"),
		slog.String("path", best.Path),
		slog.String("volume", best.VolumeName),
		slog.String("last_mdb", lastMDBLabel(best.LastMDBChange)))
	for _, c := range cands[1:] {
		slog.Info("auto-discovery encontrou outro volume (não usado nesta sessão)",
			slog.String("module", "main"),
			slog.String("event_id", "AUTO_DISCOVERY_EXTRA"),
			slog.String("path", c.Path))
	}
	return best.Path, nil
}

func lastMDBLabel(t time.Time) string {
	if t.IsZero() {
		return "nunca usado"
	}
	return t.Format(time.RFC3339)
}

// itoa local para não precisar de strconv só pra isso.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [12]byte
	pos := len(b)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
