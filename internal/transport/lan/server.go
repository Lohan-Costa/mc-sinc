package lan

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
)

// Routes constrói o subrouter HTTP exposto aos peers.
// O caller deve montá-lo sob `/peer`.
func (t *Transport) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/commits", t.handleAnnounce)
	r.Get("/files/{id}/*", t.handleFile)
	return r
}

// handleAnnounce recebe um anúncio de commit de outro peer e persiste como
// direction=received, status=announced. O receiver decide depois se faz pull.
func (t *Transport) handleAnnounce(w http.ResponseWriter, r *http.Request) {
	log.Printf("lan: received POST /peer/commits from %s (X-MC-Sinc-User=%q)",
		r.RemoteAddr, r.Header.Get(userHeader))
	var c commit.Commit
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		log.Printf("lan: announce decode failed: %v", err)
		http.Error(w, "bad announce body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if c.ID == "" || c.Author == "" || len(c.Files) == 0 {
		log.Printf("lan: REJEITADO announce vazio (id=%q author=%q files=%d) "+
			"- provavel UI do sender sem stage", c.ID, c.Author, len(c.Files))
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
			http.Error(w, "file missing hash/path", http.StatusBadRequest)
			return
		}
		files = append(files, manifest.CommitFile{Path: f.Path, Hash: f.Hash, Size: f.Size})
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
		http.Error(w, "persist: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("lan: received announce %s from %s (%d files)", c.ID, c.Author, len(c.Files))
	w.WriteHeader(http.StatusAccepted)
}

// handleFile streama o conteúdo de um arquivo committed para um peer que
// está fazendo pull. Só serve arquivos que pertencem a um commit nosso
// (direction=sent) — bloqueia leitura arbitrária do disco.
func (t *Transport) handleFile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rel := chi.URLParam(r, "*")
	if id == "" || rel == "" {
		http.Error(w, "missing id or path", http.StatusBadRequest)
		return
	}

	c, err := t.store.GetCommit(id)
	if errors.Is(err, manifest.ErrCommitNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if c.Direction != manifest.DirectionSent {
		// Não servimos arquivos de commits que recebemos — só os nossos.
		http.NotFound(w, r)
		return
	}

	allowed := false
	for _, f := range c.Files {
		if f.Path == rel {
			allowed = true
			break
		}
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}

	full := filepath.Join(t.root, rel)
	f, err := os.Open(full)
	if err != nil {
		http.Error(w, "open: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, _ := f.Stat()

	w.Header().Set("Content-Type", "application/octet-stream")
	if info != nil {
		w.Header().Set("Content-Length", itoa64(info.Size()))
	}
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

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
