// Package lan implementa Transport sobre a rede local.
//
// Anúncios e transferências de arquivos viajam por HTTP entre nós descobertos
// via mDNS (internal/discovery). O servidor HTTP é montado pelo caller sob
// /peer (ver Routes); o client outbound usa um http.Client compartilhado.
//
// Modelo: pull explícito. Send anuncia metadata; Pull baixa bytes sob demanda.
package lan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	logpkg "github.com/Lohan-Costa/mc-sinc/internal/logging"
	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
	"github.com/Lohan-Costa/mc-sinc/internal/transport"
)

const logModule = "lan"

// PeerSource é a dependência mínima que o transport precisa pra saber pra
// quem anunciar. Em produção é satisfeito por *discovery.Discovery; nos
// testes, por um stub.
type PeerSource interface {
	Peers() []transport.Peer
}

// Transport implementa transport.Transport sobre HTTP + mDNS na LAN.
type Transport struct {
	user    string
	port    int
	root    string
	store   *manifest.Store
	discov  PeerSource

	httpClient *http.Client
}

// New configura uma instância LAN.
// `root` é a raiz MXF do Avid (mesma de --root); usada pra resolver paths
// físicos quando o peer pede arquivos via /peer/files.
func New(user string, port int, root string, store *manifest.Store, disc PeerSource) *Transport {
	return &Transport{
		user:       user,
		port:       port,
		root:       root,
		store:      store,
		discov:     disc,
		httpClient: &http.Client{Timeout: 0}, // streaming; sem timeout global
	}
}

// Send anuncia um commit a todos os peers conhecidos. Fan-out em goroutines;
// falhas individuais só logam — o commit local não fracassa por causa de um
// peer offline.
func (t *Transport) Send(ctx context.Context, c *commit.Commit) error {
	peers := t.discov.Peers()
	if len(peers) == 0 {
		slog.WarnContext(ctx, "Send chamado sem peers descobertos — nada será enviado",
			slog.String("module", logModule),
			slog.String("event_id", "SEND_NO_PEERS"),
			slog.String("commit_id", c.ID))
		return nil
	}

	var wg sync.WaitGroup
	for _, p := range peers {
		if p.ID == t.user {
			continue // não anuncia pra si mesmo
		}
		wg.Add(1)
		go func(peer transport.Peer) {
			defer wg.Done()
			// Cria um op_id por peer pra permitir correlação cross-host.
			peerCtx, opID := logpkg.NewOp(ctx)
			slog.InfoContext(peerCtx, "iniciando announce para peer",
				slog.String("module", logModule),
				slog.String("event_id", "ANNOUNCE_START"),
				slog.String("commit_id", c.ID),
				slog.String("peer_id", peer.ID),
				slog.String("peer_addr", peer.Addr))
			if err := t.announce(peerCtx, peer, c, opID); err != nil {
				slog.ErrorContext(peerCtx, "announce falhou",
					slog.String("module", logModule),
					slog.String("event_id", "ANNOUNCE_FAIL"),
					slog.String("commit_id", c.ID),
					slog.String("peer_id", peer.ID),
					slog.String("error", err.Error()))
				return
			}
			slog.InfoContext(peerCtx, "announce concluído",
				slog.String("module", logModule),
				slog.String("event_id", "ANNOUNCE_OK"),
				slog.String("commit_id", c.ID),
				slog.String("peer_id", peer.ID))
		}(p)
	}
	wg.Wait()
	return nil
}

// Pull baixa os arquivos de um commit recebido. Para cada file:
//  1. abre stream do peer
//  2. escreve em arquivo temp dentro de MXF/1-<author>/ enquanto computa xxhash
//  3. compara com hash anunciado — match: rename pro destino final + upsert no manifest;
//     mismatch: deleta o temp, marca o file no manifest como falho, segue pro próximo
//
// Status do commit avança announced → pulling → pulled (ou failed se algum falhou).
func (t *Transport) Pull(ctx context.Context, commitID string) error {
	c, err := t.store.GetCommit(commitID)
	if err != nil {
		return fmt.Errorf("get commit: %w", err)
	}
	if c.Direction != manifest.DirectionReceived {
		return errors.New("pull: commit não é direction=received")
	}
	if c.PeerAddr == "" {
		return errors.New("pull: commit sem peer_addr — não sei de quem baixar")
	}

	peerHost := stripUserPrefix(c.PeerAddr)
	senderHostPort := peerHostPort(peerHost, t.port, c.Author, t.discov)

	if err := t.store.UpdateCommitStatus(commitID, manifest.CommitStatusPulling); err != nil {
		return fmt.Errorf("status pulling: %w", err)
	}

	destDir := filepath.Join(t.root, "1-"+c.Author)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", destDir, err)
	}

	anyFailed := false
	for _, f := range c.Files {
		if err := t.pullOne(ctx, senderHostPort, commitID, f, destDir); err != nil {
			slog.ErrorContext(ctx, "pull de arquivo falhou",
				slog.String("module", logModule),
				slog.String("event_id", "PULL_FILE_FAIL"),
				slog.String("commit_id", commitID),
				slog.String("path", f.Path),
				slog.String("error", err.Error()))
			anyFailed = true
			continue
		}
	}

	final := manifest.CommitStatusPulled
	if anyFailed {
		final = manifest.CommitStatusFailed
	}
	if err := t.store.UpdateCommitStatus(commitID, final); err != nil {
		return fmt.Errorf("status final: %w", err)
	}
	return nil
}

// pullOne baixa um único arquivo, valida hash, e grava em destDir/<basename>.
func (t *Transport) pullOne(ctx context.Context, peerAddr, commitID string, f manifest.CommitFile, destDir string) error {
	stream, err := t.fetch(ctx, peerAddr, commitID, f.Path)
	if err != nil {
		return err
	}
	defer stream.Close()

	// path.Base (não path/filepath.Base): f.Path é forward-slash de
	// protocolo. filepath.Base no Unix não reconheceria backslash, mas
	// FileSpec é normalizado no sender; usar path.Base remove a chance
	// de regressão se alguém esquecer a normalização.
	filename := path.Base(f.Path)
	finalPath := filepath.Join(destDir, filename)
	tmpPath := finalPath + ".part"

	out, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}

	h := xxhash.New()
	written, err := io.Copy(io.MultiWriter(out, h), stream)
	closeErr := out.Close()
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("copy: %w", err)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", closeErr)
	}

	gotHash := fmt.Sprintf("%016x", h.Sum64())
	if gotHash != f.Hash {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("hash mismatch: got %s, want %s", gotHash, f.Hash)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	// rel-path no manifest: 1-<author>/<filename>
	rel := filepath.Join(filepath.Base(destDir), filename)
	info, _ := os.Stat(finalPath)
	mtime := time.Now()
	if info != nil {
		mtime = info.ModTime()
	}
	if err := t.store.Upsert(manifest.File{
		Path:       rel,
		Hash:       gotHash,
		Size:       written,
		ModifiedAt: mtime,
		Status:     manifest.StatusReceived,
	}); err != nil {
		return fmt.Errorf("upsert manifest: %w", err)
	}
	slog.InfoContext(ctx, "arquivo baixado com sucesso",
		slog.String("module", logModule),
		slog.String("event_id", "PULL_FILE_OK"),
		slog.String("path", rel),
		slog.Int64("bytes", written),
		slog.String("hash", gotHash))
	return nil
}

// ListPeers devolve um snapshot dos peers descobertos.
func (t *Transport) ListPeers(ctx context.Context) ([]transport.Peer, error) {
	return t.discov.Peers(), nil
}

// Close não tem recursos próprios — o http.Server é do caller, mDNS é do discovery.
func (t *Transport) Close() error { return nil }

// stripUserPrefix tira o "<user>@" se presente (remoteHost no server.go anota assim).
func stripUserPrefix(s string) string {
	if i := strings.Index(s, "@"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// peerHostPort tenta resolver o host:port real do peer. O Addr armazenado em
// PeerAddr veio do r.RemoteAddr do POST /peer/commits — é o IP do sender mas
// com a porta efêmera do TCP de origem, não a porta HTTP do sender.
// Resolvemos consultando o cache de discovery pelo author do commit.
func peerHostPort(remoteAddr string, fallbackPort int, author string, disc PeerSource) string {
	for _, p := range disc.Peers() {
		if p.ID == author {
			return p.Addr
		}
	}
	// Fallback: junta o IP do remoteAddr com a porta padrão.
	host := remoteAddr
	if i := strings.LastIndex(remoteAddr, ":"); i >= 0 {
		host = remoteAddr[:i]
	}
	return fmt.Sprintf("%s:%d", host, fallbackPort)
}

// compile-time check de que satisfazemos a interface.
var _ transport.Transport = (*Transport)(nil)
