package lan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	"github.com/Lohan-Costa/mc-sinc/internal/transport"
)

const (
	announceTimeout = 10 * time.Second
	userHeader      = "X-MC-Sinc-User"
	opHeader        = "X-MC-Sinc-Op"
)

// announce envia um anúncio de commit para um peer específico.
// opID viaja no header X-MC-Sinc-Op pra permitir correlação cross-host
// nos logs estruturados.
func (t *Transport) announce(ctx context.Context, peer transport.Peer, c *commit.Commit, opID string) error {
	body, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal commit: %w", err)
	}
	if peer.Addr == "" {
		return fmt.Errorf("peer %s sem Addr — discovery não populou", peer.ID)
	}
	endpoint := fmt.Sprintf("http://%s/peer/commits", peer.Addr)
	slog.InfoContext(ctx, "POST anúncio para peer",
		slog.String("module", logModule),
		slog.String("event_id", "ANNOUNCE_HTTP_POST"),
		slog.String("endpoint", endpoint),
		slog.String("commit_id", c.ID),
		slog.Int("files", len(c.Files)),
		slog.Int("body_bytes", len(body)))

	annCtx, cancel := context.WithTimeout(ctx, announceTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(annCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(userHeader, t.user)
	if opID != "" {
		req.Header.Set(opHeader, opID)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("peer %s status %d: %s", peer.ID, resp.StatusCode, string(buf))
	}
	return nil
}

// fetch streama o conteúdo de um arquivo de um peer.
// O caller é responsável por fechar o ReadCloser retornado.
func (t *Transport) fetch(ctx context.Context, peerAddr, commitID, path string) (io.ReadCloser, error) {
	endpoint := fmt.Sprintf("http://%s/peer/files/%s/%s",
		peerAddr,
		url.PathEscape(commitID),
		pathEscapeSegments(path),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set(userHeader, t.user)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("peer status %d: %s", resp.StatusCode, string(buf))
	}
	return resp.Body, nil
}

// fetchInventory consulta GET /peer/inventory?user=<requestingUser>
// num peer remoto. Devolve a lista de .mxf que ele tem na pasta
// 1-<requestingUser>/ — usado pelo handleSyncWith pra montar delta.
func (t *Transport) fetchInventory(ctx context.Context, peerAddr, requestingUser string) ([]transport.InventoryItem, error) {
	endpoint := fmt.Sprintf("http://%s/peer/inventory?user=%s",
		peerAddr, url.QueryEscape(requestingUser))

	invCtx, cancel := context.WithTimeout(ctx, announceTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(invCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build inventory req: %w", err)
	}
	req.Header.Set(userHeader, t.user)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get inventory: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("peer status %d: %s", resp.StatusCode, body)
	}
	var items []transport.InventoryItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode inventory: %w", err)
	}
	return items, nil
}

// pathEscapeSegments encoda cada segmento entre `/` separadamente.
// PathEscape encoda `/` como %2F, que chi não desempacota para o
// curinga `{*}`; precisamos preservar as barras literais.
func pathEscapeSegments(p string) string {
	out := ""
	for i, seg := range splitOnSlash(p) {
		if i > 0 {
			out += "/"
		}
		out += url.PathEscape(seg)
	}
	return out
}

func splitOnSlash(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	out = append(out, p[start:])
	return out
}
