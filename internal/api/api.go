// Package api expõe o servidor HTTP local que a UI web consome.
//
// Endpoints UI-facing:
//
//	GET  /status              → estado geral do nó (user, root, peers, contadores)
//	GET  /pending             → arquivos detectados ou staged (UI marca staged via Status)
//	POST /stage               → marca um arquivo para entrar no próximo commit
//	POST /unstage             → desmarca um arquivo (volta para discovered)
//	POST /commit              → executa um commit dos arquivos staged
//	GET  /commits/sent        → histórico de commits anunciados aos peers
//	GET  /commits/received    → commits anunciados por peers (aguardando pull)
//	POST /commits/{id}/pull   → baixa arquivos do commit recebido para MXF/1-<sender>/
//	GET  /                    → serve a UI web (assets embutidos via internal/web)
//
// Endpoints peer-facing são montados em /peer/* via Transport.Routes().
package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Lohan-Costa/mc-sinc/internal/avid"
	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	"github.com/Lohan-Costa/mc-sinc/internal/discovery"
	logpkg "github.com/Lohan-Costa/mc-sinc/internal/logging"
	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
	"github.com/Lohan-Costa/mc-sinc/internal/transport"
)

const logModule = "api"

// Server é o handler HTTP raiz do nó.
type Server struct {
	user        string
	root        string
	version     string
	store       *manifest.Store
	commits     *commit.Service
	discovery   *discovery.Discovery
	transport   transport.Transport
	web         fs.FS
	avidProcess string

	// lifecycle é o ctx que os handlers usam pra spawnar goroutines de
	// background (fan-out de Send, Pull). É cancelado pelo caller no
	// shutdown — as goroutines em curso abortam graciosamente em vez de
	// virar zumbi. wg permite o caller esperar todas terminarem antes
	// de fechar o store.
	lifecycle context.Context
	wg        sync.WaitGroup
}

// Config agrupa as dependências necessárias para construir o Server.
type Config struct {
	User        string
	Root        string
	Version     string
	Store       *manifest.Store
	Commits     *commit.Service
	Discovery   *discovery.Discovery
	Transport   transport.Transport
	Web         fs.FS // sistema de arquivos com a UI (`web/`)
	AvidProcess string // nome do processo do Avid pra detecção (ex: "Avid Media Composer")

	// Lifecycle: ctx que controla as goroutines de fan-out. Quando
	// cancelado, Send/Pull em curso abortam. Opcional — default é
	// context.Background() (goroutines rodam até completar).
	Lifecycle context.Context
}

// New monta o servidor.
func New(cfg Config) *Server {
	lifecycle := cfg.Lifecycle
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	return &Server{
		user:        cfg.User,
		root:        cfg.Root,
		version:     cfg.Version,
		store:       cfg.Store,
		commits:     cfg.Commits,
		discovery:   cfg.Discovery,
		transport:   cfg.Transport,
		web:         cfg.Web,
		avidProcess: cfg.AvidProcess,
		lifecycle:   lifecycle,
	}
}

// Wait bloqueia até todas as goroutines de fan-out terminarem ou o ctx
// passado expirar. Deve ser chamado pelo main durante o shutdown,
// depois de httpSrv.Shutdown — assim os requests em voo terminam,
// e as goroutines de background que eles dispararam têm chance de
// fechar (anúncios pendentes, pulls em curso).
func (s *Server) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Handler devolve o http.Handler com todas as rotas registradas.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(opIDMiddleware)
	r.Use(middleware.Recoverer)

	r.Get("/status", s.handleStatus)
	r.Get("/pending", s.handlePending)
	r.Post("/stage", s.handleStage)
	r.Post("/unstage", s.handleUnstage)
	r.Post("/commit", s.handleCommit)

	r.Get("/commits/sent", s.handleListCommits(manifest.DirectionSent))
	r.Get("/commits/received", s.handleListCommits(manifest.DirectionReceived))
	r.Post("/commits/{id}/pull", s.handlePull)

	if s.transport != nil {
		r.Mount("/peer", s.transport.Routes())
	}

	if s.web != nil {
		r.Handle("/*", http.FileServer(http.FS(s.web)))
	}
	return r
}

type statusResponse struct {
	User    string        `json:"user"`
	Root    string        `json:"root"`
	Version string        `json:"version"`
	Peers   []string      `json:"peers"`
	Avid    avid.Snapshot `json:"avid"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	peerIDs := []string{}
	if s.discovery != nil {
		for _, p := range s.discovery.Peers() {
			peerIDs = append(peerIDs, p.ID)
		}
	}

	// Detecta o estado do Avid. Erros são esperados em ambientes sem mídia
	// (ex: --root apontado pra pasta vazia) — o Snapshot ainda é parcial e
	// útil; logamos pra debug mas não falhamos a request.
	snap, err := avid.Detect(avid.Config{
		Root:        s.root,
		ProcessName: s.avidProcess,
	})
	// Vários erros de avid.Detect são "ruído normal" e não precisam aparecer
	// no log — basta snap.State na wire.
	//   - "no msmMMOB.mdb"        Avid sem mídia ainda na pasta
	//   - "no such file"          root inexistente (Unix)
	//   - "cannot find the path"  root inexistente (Windows)
	// Erros DESCONHECIDOS (permissão, etc.) continuam logados.
	if err != nil && !isExpectedAvidErr(err.Error()) {
		slog.WarnContext(r.Context(), "avid.Detect falhou",
			slog.String("module", logModule),
			slog.String("event_id", "AVID_DETECT_FAIL"),
			slog.String("error", err.Error()))
	}

	writeJSON(w, http.StatusOK, statusResponse{
		User:    s.user,
		Root:    s.root,
		Version: s.version,
		Peers:   peerIDs,
		Avid:    snap,
	})
}

func (s *Server) handlePending(w http.ResponseWriter, r *http.Request) {
	// "Pendentes" inclui tanto arquivos discovered (sem decisão) quanto
	// staged (já marcados pra próximo commit). A UI distingue pelo campo
	// Status pra renderizar o checkbox marcado/desmarcado.
	discovered, err := s.store.ByStatus(manifest.StatusDiscovered)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	staged, err := s.store.ByStatus(manifest.StatusStaged)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	files := append(discovered, staged...)
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

func (s *Server) handleUnstage(w http.ResponseWriter, r *http.Request) {
	var req stageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if err := s.commits.Unstage(r.Context(), req.Path); err != nil {
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

	// Rejeita commit vazio: confere se há pelo menos 1 file com hash
	// em discovered ou staged (commit.Service.Commit considera ambos).
	// Sem isso, criaríamos um commit fantasma de 0 arquivos.
	hasHashable := false
	for _, st := range []manifest.Status{manifest.StatusDiscovered, manifest.StatusStaged} {
		files, err := s.store.ByStatus(st)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, f := range files {
			if f.Hash != "" {
				hasHashable = true
				break
			}
		}
		if hasHashable {
			break
		}
	}
	if !hasHashable {
		http.Error(w, "nenhum arquivo com hash calculado pra enviar", http.StatusBadRequest)
		return
	}

	// Mensagem é opcional — se vazia, geramos uma default com timestamp
	// pra o usuário não precisar pensar num texto pra cada envio.
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		msg = "envio manual em " + time.Now().Format("02/01/2006 15:04")
	}

	c, err := s.commits.Commit(r.Context(), msg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Persiste como direction=sent — autoriza /peer/files a servi-lo.
	files := make([]manifest.CommitFile, 0, len(c.Files))
	for _, f := range c.Files {
		files = append(files, manifest.CommitFile{Path: f.Path, Hash: f.Hash, Size: f.Size})
	}
	if err := s.store.SaveCommit(manifest.Commit{
		ID:        c.ID,
		Author:    c.Author,
		Message:   c.Message,
		CreatedAt: c.CreatedAt,
		Direction: manifest.DirectionSent,
		Status:    manifest.CommitStatusAnnounced,
		Files:     files,
	}); err != nil {
		http.Error(w, "persist commit: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Fan-out aos peers em background — o commit local não espera a rede.
	// Usa s.lifecycle pra que o shutdown cancele anúncios pendentes;
	// s.wg permite o main esperar todas terminarem antes de fechar o store.
	// Propaga o op_id do request original pro background goroutine.
	if s.transport != nil {
		opID, _ := logpkg.OpFromContext(r.Context())
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			bgCtx := s.lifecycle
			if opID != "" {
				bgCtx = logpkg.WithOp(bgCtx, opID)
			}
			if err := s.transport.Send(bgCtx, c); err != nil {
				slog.WarnContext(bgCtx, "transport.Send falhou em background",
					slog.String("module", logModule),
					slog.String("event_id", "TRANSPORT_SEND_FAIL"),
					slog.String("commit_id", c.ID),
					slog.String("error", err.Error()))
			}
		}()
	}

	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleListCommits(dir manifest.Direction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := s.store.ListCommits(dir, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}
}

func (s *Server) handlePull(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if s.transport == nil {
		http.Error(w, "no transport configured", http.StatusServiceUnavailable)
		return
	}
	// Pull pode demorar — roda em background, devolve 202. Usa
	// s.lifecycle/s.wg como o handleCommit pra coordenação com shutdown.
	opID, _ := logpkg.OpFromContext(r.Context())
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		bgCtx := s.lifecycle
		if opID != "" {
			bgCtx = logpkg.WithOp(bgCtx, opID)
		}
		if err := s.transport.Pull(bgCtx, id); err != nil {
			slog.WarnContext(bgCtx, "transport.Pull falhou em background",
				slog.String("module", logModule),
				slog.String("event_id", "TRANSPORT_PULL_FAIL"),
				slog.String("commit_id", id),
				slog.String("error", err.Error()))
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// opIDMiddleware gera um op_id pra cada request e injeta no context, pra
// correlacionar logs do mesmo handler. Substitui o middleware.Logger
// default (que usa log.Printf não-estruturado).
func opIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := logpkg.NewOp(r.Context())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isExpectedAvidErr filtra mensagens conhecidas de avid.Detect que são
// ruído normal (não erros reais). Mantém em uma função pra ficar testável
// se precisar e centralizar a lista de substrings.
func isExpectedAvidErr(msg string) bool {
	for _, sub := range []string{
		"no msmMMOB.mdb",     // pasta sem .mdb ainda
		"no such file",       // root inexistente (Unix)
		"cannot find the path", // root inexistente (Windows)
	} {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}
