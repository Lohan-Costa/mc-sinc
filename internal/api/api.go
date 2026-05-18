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
//	POST /commits/{id}/reject → marca commit recebido como rejeitado (não baixa)
//	POST /commits/clear       → apaga commits recebidos já finalizados (pulled/rejected/failed)
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
	"github.com/Lohan-Costa/mc-sinc/internal/config"
	"github.com/Lohan-Costa/mc-sinc/internal/discovery"
	"github.com/Lohan-Costa/mc-sinc/internal/fsbrowse"
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
	configPath  string
	store       *manifest.Store
	commits     *commit.Service
	discovery   *discovery.Discovery
	transport   transport.Transport
	web              fs.FS
	avidProcess      string
	avidRecentWindow time.Duration

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
	Web              fs.FS         // sistema de arquivos com a UI (`web/`)
	AvidProcess      string        // nome do processo do Avid pra detecção (ex: "Avid Media Composer")
	AvidRecentWindow time.Duration // threshold pra Avid virar idle; se zero, usa default 5min

	// ConfigPath: arquivo de config persistente (ex: ~/.mcsinc/config.json).
	// Se vazio, GET/POST /config respondem 503 (não configurado).
	ConfigPath string

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
		configPath:  cfg.ConfigPath,
		store:       cfg.Store,
		commits:     cfg.Commits,
		discovery:   cfg.Discovery,
		transport:   cfg.Transport,
		web:              cfg.Web,
		avidProcess:      cfg.AvidProcess,
		avidRecentWindow: cfg.AvidRecentWindow,
		lifecycle:        lifecycle,
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
	r.Post("/commits/{id}/reject", s.handleReject)
	r.Post("/commits/clear", s.handleClearFinished)

	r.Get("/config", s.handleGetConfig)
	r.Post("/config", s.handlePostConfig)
	r.Get("/fs/browse", s.handleFsBrowse)
	r.Post("/config/select-avid", s.handleSelectAvid)

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
		Root:         s.root,
		ProcessName:  s.avidProcess,
		RecentWindow: s.avidRecentWindow,
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

// handleReject marca um commit recebido como rejected — usuário decidiu
// não baixar. Preserva o registro no manifest pra auditoria. UI esconde
// commits rejected por padrão.
func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if err := s.store.UpdateCommitStatus(id, manifest.CommitStatusRejected); err != nil {
		slog.WarnContext(r.Context(), "reject falhou",
			slog.String("module", logModule),
			slog.String("event_id", "COMMIT_REJECT_FAIL"),
			slog.String("commit_id", id),
			slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.InfoContext(r.Context(), "commit rejeitado",
		slog.String("module", logModule),
		slog.String("event_id", "COMMIT_REJECTED"),
		slog.String("commit_id", id))
	w.WriteHeader(http.StatusNoContent)
}

// handleClearFinished apaga commits received já finalizados (pulled,
// rejected, failed) — limpa a UI mantendo só o que ainda tem ação
// pendente (announced, pulling).
func (s *Server) handleClearFinished(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.DeleteFinishedReceived()
	if err != nil {
		slog.WarnContext(r.Context(), "clear finished falhou",
			slog.String("module", logModule),
			slog.String("event_id", "COMMIT_CLEAR_FAIL"),
			slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.InfoContext(r.Context(), "commits finalizados apagados",
		slog.String("module", logModule),
		slog.String("event_id", "COMMIT_CLEAR_OK"),
		slog.Int64("count", n))
	writeJSON(w, http.StatusOK, map[string]int64{"removed": n})
}

// handleGetConfig devolve o config persistente atual. Útil pra UI
// pré-preencher o campo "Pasta raiz" com o valor salvo (que pode
// diferir do *root em runtime quando o usuário acabou de editar mas
// ainda não reiniciou).
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" {
		http.Error(w, "config path nao configurado", http.StatusServiceUnavailable)
		return
	}
	cfg, err := config.Load(s.configPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// handlePostConfig salva alterações no config.json. NÃO reconfigura
// watcher/manifest em runtime — exige reinício do mcsinc. UI mostra
// aviso "reinicie pra aplicar".
func (s *Server) handlePostConfig(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" {
		http.Error(w, "config path nao configurado", http.StatusServiceUnavailable)
		return
	}
	var req config.Persistent
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := config.Save(s.configPath, req); err != nil {
		slog.WarnContext(r.Context(), "config.Save falhou",
			slog.String("module", logModule),
			slog.String("event_id", "CONFIG_SAVE_FAIL"),
			slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.InfoContext(r.Context(), "config persistido — aplicar requer restart",
		slog.String("module", logModule),
		slog.String("event_id", "CONFIG_SAVED"),
		slog.String("new_root", req.Root))
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "Config salvo. Reinicie o MC Sinc para aplicar.",
	})
}

// handleFsBrowse lista subdiretórios de um path (query ?path=...).
// Sem path → lista volumes/drives do sistema. Usado pelo modal de
// "Editar pasta raiz" na UI pra navegar sem file picker nativo.
func (s *Server) handleFsBrowse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	res, err := fsbrowse.List(path)
	if err != nil {
		slog.WarnContext(r.Context(), "fsbrowse.List falhou",
			slog.String("module", logModule),
			slog.String("event_id", "FS_BROWSE_FAIL"),
			slog.String("path", path),
			slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleSelectAvid recebe o path da pasta "Avid MediaFiles" escolhida
// na UI, valida (nome + presença de MXF/), e salva o root final
// (path/MXF) em config.json. Aplica em runtime ainda não — exige
// restart. PR seguinte fará reconfig dinâmico.
func (s *Server) handleSelectAvid(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" {
		http.Error(w, "config path nao configurado", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		AvidMediaFilesPath string `json:"avid_media_files_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	mxfRoot, err := fsbrowse.ValidateAvidRoot(req.AvidMediaFilesPath)
	if err != nil {
		slog.WarnContext(r.Context(), "select-avid rejeitado",
			slog.String("module", logModule),
			slog.String("event_id", "SELECT_AVID_INVALID"),
			slog.String("path", req.AvidMediaFilesPath),
			slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := config.Save(s.configPath, config.Persistent{Root: mxfRoot}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.InfoContext(r.Context(), "Avid MediaFiles selecionado pela UI",
		slog.String("module", logModule),
		slog.String("event_id", "SELECT_AVID_OK"),
		slog.String("avid_media_files", req.AvidMediaFilesPath),
		slog.String("mxf_root", mxfRoot))
	writeJSON(w, http.StatusOK, map[string]string{
		"root":    mxfRoot,
		"message": "Pasta Avid MediaFiles salva. Reinicie o MC Sinc para aplicar.",
	})
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
