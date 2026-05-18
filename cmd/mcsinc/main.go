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
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Lohan-Costa/mc-sinc/internal/api"
	"github.com/Lohan-Costa/mc-sinc/internal/automode"
	"github.com/Lohan-Costa/mc-sinc/internal/avid"
	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	"github.com/Lohan-Costa/mc-sinc/internal/config"
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
	// Toda a lógica vive em run() para que defers (cleanupLog, store.Close,
	// cancel) rodem em qualquer caminho de erro. Erros já são slog-ados onde
	// acontecem; aqui só traduzimos para exit code.
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
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
		autoPull = flag.Bool("auto-pull", true,
			"baixar commits recebidos automaticamente quando Avid estiver idle (fechado ≥5min)")
		autoCommit = flag.Bool("auto-commit", true,
			"commitar e enviar mudancas automaticamente quando Avid estiver idle")
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
		fmt.Fprintln(os.Stderr, "logging.Init:", err)
		return err
	}
	defer cleanupLog()

	// Ordem de precedência do --root:
	//   1. flag CLI (--root=...) explícita
	//   2. config.json persistido (editável via UI)
	//   3. auto-discovery (varre volumes Avid conhecidos)
	if *root == "" {
		cfg, cerr := config.Load(defaultConfigPath())
		if cerr != nil {
			slog.Warn("config.Load falhou — seguindo sem config persistente",
				slog.String("module", "main"),
				slog.String("event_id", "CONFIG_LOAD_FAIL"),
				slog.String("error", cerr.Error()))
		} else if cfg.Root != "" {
			*root = cfg.Root
			slog.Info("root carregado do config.json",
				slog.String("module", "main"),
				slog.String("event_id", "CONFIG_ROOT_LOADED"),
				slog.String("root", cfg.Root))
		}
	}
	if *root == "" {
		chosen, err := autoDiscoverRoot()
		if err != nil {
			slog.Error("auto-discovery falhou",
				slog.String("module", "main"),
				slog.String("event_id", "STARTUP_NO_ROOT"),
				slog.String("error", err.Error()))
			return err
		}
		*root = chosen
	}

	// Fast-fail: --root precisa existir e ser diretório. Sem isso, watcher,
	// hasher, avid.Detect e transport batem em erro a cada operação.
	if info, statErr := os.Stat(*root); statErr != nil || !info.IsDir() {
		slog.Error("--root inválido: não existe ou não é diretório",
			slog.String("module", "main"),
			slog.String("event_id", "ROOT_INVALID"),
			slog.String("root", *root))
		fmt.Fprintf(os.Stderr,
			"\nMC Sinc: a pasta --root nao existe ou nao e diretorio:\n  %s\n\n"+
				"Crie a estrutura (ex: 'mkdir -p \"%s/1\"') ou aponte --root\n"+
				"pra uma pasta MXF existente do Avid.\n\n",
			*root, *root)
		return errors.New("--root inválido")
	}

	if err := os.MkdirAll(filepath.Dir(*dbP), 0o755); err != nil {
		slog.Error("não consegui criar diretório do db",
			slog.String("module", "main"), slog.String("error", err.Error()))
		return err
	}

	store, err := manifest.Open(*dbP)
	if err != nil {
		slog.Error("falha abrindo manifest", slog.String("module", "main"), slog.String("error", err.Error()))
		return err
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
		return err
	}

	h := hasher.New(store, *root)

	tport := lan.New(*user, *port, *root, store, disc)

	webRoot, err := web.FS()
	if err != nil {
		slog.Error("falha preparando UI", slog.String("module", "main"), slog.String("error", err.Error()))
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

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
		ConfigPath:  defaultConfigPath(),
		Lifecycle:   ctx, // cancela fan-outs de background no shutdown
	})

	httpSrv := &http.Server{
		Addr:              ":" + strconv.Itoa(*port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Canal sinaliza saída anormal do servidor HTTP (porta em uso, etc.).
	// Buffered pra não bloquear a goroutine se ninguém estiver lendo.
	httpErr := make(chan error, 1)
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
			httpErr <- err
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

	// Auto-pull: baixa commits recebidos quando Avid está idle. Pode ser
	// desativado com --auto-pull=false (útil em apresentação ou debug).
	// Auto-mode: combina auto-pull (--auto-pull) e auto-commit (--auto-commit).
	// Goroutine só sobe se PELO MENOS um deles estiver habilitado.
	if *autoPull || *autoCommit {
		go func() {
			cfg := automode.Config{
				Detect: func() (avid.Snapshot, error) {
					return avid.Detect(avid.Config{
						Root:        *root,
						ProcessName: *avidProcess,
					})
				},
				Store:      store,
				Transport:  tport,
				Commits:    commits,
				AutoPull:   *autoPull,
				AutoCommit: *autoCommit,
				Interval:   30 * time.Second,
			}
			if err := automode.Run(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("automode encerrou com erro",
					slog.String("module", "automode"),
					slog.String("event_id", "AUTOMODE_FAIL"),
					slog.String("error", err.Error()))
			}
		}()
		slog.Info("auto-mode ativo",
			slog.String("module", "main"),
			slog.String("event_id", "AUTOMODE_ENABLED"),
			slog.Bool("auto_pull", *autoPull),
			slog.Bool("auto_commit", *autoCommit))
	} else {
		slog.Info("auto-mode totalmente desativado por flags",
			slog.String("module", "main"),
			slog.String("event_id", "AUTOMODE_DISABLED"))
	}

	// Drena eventos do watcher e os registra no manifest via UpsertObserved.
	// Diferença pra Upsert genérico: preserva o hash existente quando o
	// mtime não mudou (evita re-hash desnecessário a cada startup), e
	// invalida o hash quando o mtime difere (.mdb/.pmr são reescritos
	// pelo Avid — precisam ser re-hashados pra refletir o conteúdo novo).
	go func() {
		for ev := range w.Events {
			rel, _ := filepath.Rel(*root, ev.Path)
			info, err := os.Stat(ev.Path)
			if err != nil {
				continue
			}
			_ = store.UpsertObserved(rel, info.Size(), info.ModTime(), manifest.StatusDiscovered)
			slog.Info("arquivo descoberto pelo watcher",
				slog.String("module", "watcher"),
				slog.String("event_id", "FILE_DISCOVERED"),
				slog.String("path", rel))
		}
	}()

	// Espera SIGINT/SIGTERM OU falha do servidor HTTP. Em ambos os casos,
	// fazemos shutdown ordenado abaixo (defers cuidam do resto).
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-httpErr:
		runErr = err
	}
	slog.Info("encerrando", slog.String("module", "main"), slog.String("event_id", "SHUTDOWN"))

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = httpSrv.Shutdown(shutdownCtx)

	// Espera as goroutines de fan-out (transport.Send/Pull em background)
	// terminarem antes do defer fechar o store. Se estourarem o timeout,
	// é warning — store.Close vai rodar de qualquer jeito.
	if err := srv.Wait(shutdownCtx); err != nil {
		slog.Warn("goroutines de fan-out nao terminaram dentro do timeout",
			slog.String("module", "main"),
			slog.String("event_id", "SHUTDOWN_TIMEOUT"),
			slog.String("error", err.Error()))
	}

	return runErr
}

func defaultUser() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "mcsinc-user"
}

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(home, ".mcsinc", "config.json")
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
