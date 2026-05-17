// Package api expõe o servidor HTTP local que a UI web consome.
//
// Endpoints:
//
//	GET  /status   → estado geral do nó (user, root, peers, contadores)
//	GET  /pending  → arquivos detectados aguardando decisão do usuário
//	POST /stage    → marca um arquivo para entrar no próximo commit
//	POST /commit   → executa um commit dos arquivos staged
//	GET  /         → serve a UI web (web/index.html)
package api

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	"github.com/Lohan-Costa/mc-sinc/internal/discovery"
	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
)

// Server é o handler HTTP raiz do nó.
type Server struct {
	user      string
	root      string
	version   string
	store     *manifest.Store
	commits   *commit.Service
	discovery *discovery.Discovery
	web       fs.FS
}

// Config agrupa as dependências necessárias para construir o Server.
type Config struct {
	User      string
	Root      string
	Version   string
	Store     *manifest.Store
	Commits   *commit.Service
	Discovery *discovery.Discovery
	Web       fs.FS // sistema de arquivos com a UI (`web/`)
}

// New monta o servidor.
func New(cfg Config) *Server {
	return &Server{
		user:      cfg.User,
		root:      cfg.Root,
		version:   cfg.Version,
		store:     cfg.Store,
		commits:   cfg.Commits,
		discovery: cfg.Discovery,
		web:       cfg.Web,
	}
}

// Handler devolve o http.Handler com todas as rotas registradas.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/status", s.handleStatus)
	r.Get("/pending", s.handlePending)
	r.Post("/stage", s.handleStage)
	r.Post("/commit", s.handleCommit)

	if s.web != nil {
		r.Handle("/*", http.FileServer(http.FS(s.web)))
	}
	return r
}

type statusResponse struct {
	User    string   `json:"user"`
	Root    string   `json:"root"`
	Version string   `json:"version"`
	Peers   []string `json:"peers"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	peerIDs := []string{}
	if s.discovery != nil {
		for _, p := range s.discovery.Peers() {
			peerIDs = append(peerIDs, p.ID)
		}
	}
	writeJSON(w, http.StatusOK, statusResponse{
		User:    s.user,
		Root:    s.root,
		Version: s.version,
		Peers:   peerIDs,
	})
}

func (s *Server) handlePending(w http.ResponseWriter, r *http.Request) {
	files, err := s.store.ByStatus(manifest.StatusDiscovered)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

type stageRequest struct {
	Path string `json:"path"`
}

func (s *Server) handleStage(w http.ResponseWriter, r *http.Request) {
	var req stageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if err := s.commits.Stage(r.Context(), req.Path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type commitRequest struct {
	Message string `json:"message"`
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	var req commitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c, err := s.commits.Commit(r.Context(), req.Message)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
