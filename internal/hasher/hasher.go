// Package hasher calcula xxhash64 dos arquivos .mxf locais em background
// e grava no manifest. É a base para verificação de integridade e dedup
// nas próximas iterações do transport.
//
// Estratégia: polling a cada DefaultInterval segundos. Pega files com
// hash vazio (NeedsHash), abre cada um, streama por um xxhash.Digest e
// chama SetHash. Erros (arquivo sumiu, locked) são logados; o próximo
// tick tenta de novo.
//
// Re-hash quando mtime muda é TODO consciente — para v1 só preenchemos
// hashes que faltam. Avid raramente reescreve um .mxf existente.
package hasher

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
)

const logModule = "hasher"

// DefaultInterval é a periodicidade do polling.
const DefaultInterval = 5 * time.Second

// Worker hasheia arquivos pendentes no manifest.
type Worker struct {
	store    *manifest.Store
	root     string
	interval time.Duration
}

// New cria um Worker ligado ao manifest e à raiz MXF do Avid.
// `root` é a pasta raiz (a mesma de --root), porque os caminhos no manifest
// são relativos a essa raiz.
func New(store *manifest.Store, root string) *Worker {
	return &Worker{
		store:    store,
		root:     root,
		interval: DefaultInterval,
	}
}

// Run executa o loop de hashing até `ctx` ser cancelado.
func (w *Worker) Run(ctx context.Context) error {
	tick := time.NewTicker(w.interval)
	defer tick.Stop()

	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			w.tick(ctx)
		}
	}
}

// tick faz uma passada: lista pendentes e hasheia cada um.
func (w *Worker) tick(ctx context.Context) {
	pending, err := w.store.NeedsHash()
	if err != nil {
		slog.WarnContext(ctx, "falha lendo arquivos pendentes de hash",
			slog.String("module", logModule),
			slog.String("event_id", "HASH_LIST_FAIL"),
			slog.String("error", err.Error()))
		return
	}
	for _, f := range pending {
		if ctx.Err() != nil {
			return
		}
		full := filepath.Join(w.root, f.Path)
		sum, mtime, err := hashFile(full)
		if err != nil {
			slog.WarnContext(ctx, "pulando arquivo no hash",
				slog.String("module", logModule),
				slog.String("event_id", "HASH_READ_ERROR"),
				slog.String("path", f.Path),
				slog.String("error", err.Error()))
			continue
		}
		if err := w.store.SetHash(f.Path, sum, mtime); err != nil {
			slog.WarnContext(ctx, "falha gravando hash no manifest",
				slog.String("module", logModule),
				slog.String("event_id", "HASH_PERSIST_FAIL"),
				slog.String("path", f.Path),
				slog.String("error", err.Error()))
			continue
		}
		slog.DebugContext(ctx, "arquivo hasheado",
			slog.String("module", logModule),
			slog.String("event_id", "HASH_OK"),
			slog.String("path", f.Path),
			slog.String("hash", sum))
	}
}

// hashFile streama o conteúdo do arquivo por um xxhash64 e devolve o
// hex (16 chars) junto com o mtime observado no momento da leitura.
func hashFile(path string) (string, time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", time.Time{}, err
	}

	h := xxhash.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", time.Time{}, err
	}
	return fmt.Sprintf("%016x", h.Sum64()), info.ModTime(), nil
}
