package lan

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	logpkg "github.com/Lohan-Costa/mc-sinc/internal/logging"
	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
	"github.com/Lohan-Costa/mc-sinc/internal/transport"
)

// Routes constrói o subrouter HTTP exposto aos peers.
// O caller deve montá-lo sob `/peer`.
func (t *Transport) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/commits", t.handleAnnounce)
	r.Get("/files/{id}/*", t.handleFile)
	r.Get("/inventory", t.handleInventory)
	return r
}

// handleInventory devolve os arquivos .mxf que esse peer tem na pasta
// `1-<requesting_user>/`. Usado pelo sender (peer A) pra montar lista
// delta antes de mandar sync.
//
// Query: ?user=<requesting_user>. Sem user, devolve 400.
// Resposta: JSON array de InventoryItem.
func (t *Transport) handleInventory(w http.ResponseWriter, r *http.Request) {
	user := strings.TrimSpace(r.URL.Query().Get("user"))
	if user == "" {
		http.Error(w, "query 'user' required", http.StatusBadRequest)
		return
	}
	prefix := "1-" + user + "/"
	files, err := t.store.FilesUnderPrefix(prefix)
	if err != nil {
		slog.WarnContext(r.Context(), "inventory: falha lendo manifest",
			slog.String("module", logModule),
			slog.String("event_id", "INVENTORY_LIST_FAIL"),
			slog.String("prefix", prefix),
			slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items := make([]transport.InventoryItem, 0, len(files))
	for _, f := range files {
		// Inventário cobre só .mxf — .mdb/.pmr sempre são re-enviados
		// (Avid atualiza constantemente, comparar hash não economiza).
		if !strings.EqualFold(filepath.Ext(f.Path), ".mxf") {
			continue
		}
		if f.Hash == "" {
			continue // arquivo ainda não hashado — peer A não consegue comparar
		}
		items = append(items, transport.InventoryItem{
			Path: strings.ReplaceAll(f.Path, `\`, "/"),
			Hash: f.Hash,
			Size: f.Size,
		})
	}
	slog.InfoContext(r.Context(), "inventory respondido",
		slog.String("module", logModule),
		slog.String("event_id", "INVENTORY_OK"),
		slog.String("requesting_user", user),
		slog.Int("count", len(items)))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

// handleAnnounce recebe um anúncio de commit de outro peer e persiste como
// direction=received, status=announced. O receiver decide depois se faz pull.
func (t *Transport) handleAnnounce(w http.ResponseWriter, r *http.Request) {
	// Pega o op_id do header pra correlacionar com o sender no log.
	ctx := r.Context()
	if op := r.Header.Get(opHeader); op != "" {
		ctx = logpkg.WithOp(ctx, op)
	}

	slog.InfoContext(ctx, "POST /peer/commits recebido",
		slog.String("module", logModule),
		slog.String("event_id", "ANNOUNCE_RECEIVED"),
		slog.String("from", r.RemoteAddr),
		slog.String("sender_user", r.Header.Get(userHeader)))

	var c commit.Commit
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		slog.WarnContext(ctx, "decode do announce falhou",
			slog.String("module", logModule),
			slog.String("event_id", "ANNOUNCE_DECODE_FAIL"),
			slog.String("error", err.Error()))
		http.Error(w, "bad announce body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if c.ID == "" || c.Author == "" || len(c.Files) == 0 {
		slog.WarnContext(ctx, "announce rejeitado por validação vazia",
			slog.String("module", logModule),
			slog.String("event_id", "ANNOUNCE_REJECTED_EMPTY"),
			slog.String("commit_id", c.ID),
			slog.String("author", c.Author),
			slog.Int("files", len(c.Files)))
		http.Error(w, "announce missing id, author or files", http.StatusBadRequest)
		return
	}
	if c.Author == t.user {
		// Echo do próprio anúncio — ignora silenciosamente.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	files := make([]manifest.CommitFile, 0, len(c.Files))
	for _, f := range c.Files {
		if f.Hash == "" || f.Path == "" {
			slog.WarnContext(ctx, "announce rejeitado por arquivo sem hash/path",
				slog.String("module", logModule),
				slog.String("event_id", "ANNOUNCE_FILE_INVALID"))
			http.Error(w, "file missing hash/path", http.StatusBadRequest)
			return
		}
		// Defensivo: senders novos normalizam pra forward slash em commit.go,
		// mas se algum sender antigo (ou outro transport futuro) mandar com
		// backslash, normaliza aqui antes de persistir.
		normalized := strings.ReplaceAll(f.Path, `\`, "/")
		files = append(files, manifest.CommitFile{Path: normalized, Hash: f.Hash, Size: f.Size})
	}

	mc := manifest.Commit{
		ID:        c.ID,
		Author:    c.Author,
		Message:   c.Message,
		CreatedAt: c.CreatedAt,
		Direction: manifest.DirectionReceived,
		PeerAddr:  remoteHost(r),
		Status:    manifest.CommitStatusAnnounced,
		Files:     files,
	}
	if err := t.store.SaveCommit(mc); err != nil {
		slog.ErrorContext(ctx, "falha persistindo announce",
			slog.String("module", logModule),
			slog.String("event_id", "ANNOUNCE_PERSIST_FAIL"),
			slog.String("commit_id", c.ID),
			slog.String("error", err.Error()))
		http.Error(w, "persist: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.InfoContext(ctx, "announce persistido como recebido",
		slog.String("module", logModule),
		slog.String("event_id", "ANNOUNCE_OK"),
		slog.String("commit_id", c.ID),
		slog.String("author", c.Author),
		slog.Int("files", len(c.Files)))
	w.WriteHeader(http.StatusAccepted)
}

// handleFile streama o conteúdo de um arquivo committed para um peer que
// está fazendo pull. Só serve arquivos que pertencem a um commit nosso
// (direction=sent) — bloqueia leitura arbitrária do disco.
func (t *Transport) handleFile(w http.ResponseWriter, r *http.Request) {
	// Correlaciona com o op_id do peer que solicitou — mesmo pattern do
	// handleAnnounce.
	ctx := r.Context()
	if op := r.Header.Get(opHeader); op != "" {
		ctx = logpkg.WithOp(ctx, op)
	}

	id := chi.URLParam(r, "id")
	rel := chi.URLParam(r, "*")
	if id == "" || rel == "" {
		slog.WarnContext(ctx, "/peer/files chamado sem id/path",
			slog.String("module", logModule),
			slog.String("event_id", "FILE_REQUEST_INVALID"),
			slog.String("from", r.RemoteAddr))
		http.Error(w, "missing id or path", http.StatusBadRequest)
		return
	}

	c, err := t.store.GetCommit(id)
	if errors.Is(err, manifest.ErrCommitNotFound) {
		slog.WarnContext(ctx, "/peer/files com commit_id desconhecido",
			slog.String("module", logModule),
			slog.String("event_id", "FILE_COMMIT_NOT_FOUND"),
			slog.String("commit_id", id),
			slog.String("from", r.RemoteAddr))
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "falha consultando commit pra servir arquivo",
			slog.String("module", logModule),
			slog.String("event_id", "FILE_LOOKUP_FAIL"),
			slog.String("commit_id", id),
			slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if c.Direction != manifest.DirectionSent {
		// Não servimos arquivos de commits que recebemos — só os nossos.
		// Pode ser peer mal configurado ou tentativa de leitura indevida.
		slog.WarnContext(ctx, "/peer/files pediu commit que nao e nosso (direction!=sent)",
			slog.String("module", logModule),
			slog.String("event_id", "FILE_NOT_OUR_COMMIT"),
			slog.String("commit_id", id),
			slog.String("from", r.RemoteAddr))
		http.NotFound(w, r)
		return
	}

	// Normaliza pra forward slash + sem prefixo "/" antes da comparação.
	// Defensivo: cobre commits velhos no manifest (sender pré-fix de
	// commit.go ainda tem \) e receivers que enviem URL com / extra.
	relNorm := strings.TrimPrefix(strings.ReplaceAll(rel, `\`, "/"), "/")

	allowed := false
	for _, f := range c.Files {
		if strings.ReplaceAll(f.Path, `\`, "/") == relNorm {
			allowed = true
			break
		}
	}
	if !allowed {
		// Path fora da lista do commit — bloqueio de leitura arbitrária.
		// Inclui manifest_paths no log pra facilitar debug futuro: se for
		// mismatch de separador, fica óbvio comparando rel com a lista.
		manifestPaths := make([]string, 0, len(c.Files))
		for _, f := range c.Files {
			manifestPaths = append(manifestPaths, f.Path)
		}
		slog.WarnContext(ctx, "/peer/files pediu path fora do manifesto do commit",
			slog.String("module", logModule),
			slog.String("event_id", "FILE_NOT_ALLOWED"),
			slog.String("commit_id", id),
			slog.String("path", rel),
			slog.String("path_normalized", relNorm),
			slog.String("manifest_paths", strings.Join(manifestPaths, ",")),
			slog.String("from", r.RemoteAddr))
		http.NotFound(w, r)
		return
	}

	// filepath.Join no Windows aceita /, mas explicitar FromSlash é menos
	// frágil que confiar em comportamento implícito do stdlib.
	full := filepath.Join(t.getRoot(), filepath.FromSlash(relNorm))
	f, err := os.Open(full)
	if err != nil {
		slog.ErrorContext(ctx, "falha abrindo arquivo pra servir",
			slog.String("module", logModule),
			slog.String("event_id", "FILE_OPEN_FAIL"),
			slog.String("commit_id", id),
			slog.String("path", rel),
			slog.String("error", err.Error()))
		http.Error(w, "open: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, _ := f.Stat()

	w.Header().Set("Content-Type", "application/octet-stream")
	if info != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	slog.InfoContext(ctx, "servindo arquivo pro peer",
		slog.String("module", logModule),
		slog.String("event_id", "FILE_SERVED"),
		slog.String("commit_id", id),
		slog.String("path", rel),
		slog.String("from", r.RemoteAddr))
	http.ServeContent(w, r, filepath.Base(rel), info.ModTime(), f)
}

// remoteHost extrai um identificador legível do peer (X-MC-Sinc-User se
// presente, senão r.RemoteAddr).
func remoteHost(r *http.Request) string {
	if u := strings.TrimSpace(r.Header.Get("X-MC-Sinc-User")); u != "" {
		return u + "@" + r.RemoteAddr
	}
	return r.RemoteAddr
}

