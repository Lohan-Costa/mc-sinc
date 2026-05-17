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
	)
	flag.Parse()

	if *root == "" {
		chosen, err := autoDiscoverRoot()
		if err != nil {
			log.Fatal(err)
		}
		*root = chosen
	}

	if err := os.MkdirAll(filepath.Dir(*dbP), 0o755); err != nil {
		log.Fatalf("criando diretório do db: %v", err)
	}

	store, err := manifest.Open(*dbP)
	if err != nil {
		log.Fatalf("abrindo manifest: %v", err)
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
		log.Fatalf("criando watcher: %v", err)
	}

	h := hasher.New(store, *root)

	tport := lan.New(*user, *port, *root, store, disc)

	webRoot, err := web.FS()
	if err != nil {
		log.Fatalf("preparando UI: %v", err)
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
		log.Printf("MC Sinc %s — user=%q root=%q http=:%d", version, *user, *root, *port)
		log.Printf("UI:    http://localhost:%d", *port)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	go func() {
		if err := disc.Run(ctx); err != nil {
			log.Printf("discovery: %v", err)
		}
	}()

	go func() {
		if err := w.Run(ctx); err != nil {
			log.Printf("watcher: %v", err)
		}
	}()

	go func() {
		if err := h.Run(ctx); err != nil {
			log.Printf("hasher: %v", err)
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
			log.Printf("discovered: %s", rel)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down…")

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
	log.Printf("auto-discovery: usando %q (volume %q, último .mdb %s)",
		best.Path, best.VolumeName, lastMDBLabel(best.LastMDBChange))
	for _, c := range cands[1:] {
		log.Printf("auto-discovery: também encontrado %q (não usado nesta sessão)", c.Path)
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
