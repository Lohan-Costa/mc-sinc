package lan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	"github.com/Lohan-Costa/mc-sinc/internal/transport"
)

const (
	announceTimeout = 10 * time.Second
	userHeader      = "X-MC-Sinc-User"
)

// announce envia um anúncio de commit para um peer específico.
func (t *Transport) announce(ctx context.Context, peer transport.Peer, c *commit.Commit) error {
	body, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal commit: %w", err)
	}
	endpoint := fmt.Sprintf("http://%s/peer/commits", peer.Addr)

	annCtx, cancel := context.WithTimeout(ctx, announceTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(annCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(userHeader, t.user)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
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
