// Package lan implementa Transport sobre a rede local.
//
// Atualmente é um placeholder estrutural — a integração com mDNS
// vive em `internal/discovery`, e o handshake HTTP de transferência
// vai ser implementado nas próximas iterações.
package lan

import (
	"context"
	"errors"
	"io"

	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	"github.com/Lohan-Costa/mc-sinc/internal/transport"
)

// Transport implementa transport.Transport sobre HTTP + mDNS na LAN.
type Transport struct {
	user string
	port int
	// TODO: discovery client, http server, peer cache
}

// New cria uma instância LAN configurada com a identidade do usuário corrente
// e a porta HTTP local em que o nó escuta.
func New(user string, port int) *Transport {
	return &Transport{user: user, port: port}
}

// Send anuncia um commit para todos os peers conhecidos.
// TODO: implementar handshake HTTP e streaming dos arquivos.
func (t *Transport) Send(ctx context.Context, c *commit.Commit, open func(path string) (io.ReadCloser, error)) error {
	return errors.New("lan.Send: not implemented yet")
}

// Receive bloqueia esperando o anúncio de um commit de algum peer.
// TODO: subscrever a eventos de mDNS / endpoint HTTP de anúncio.
func (t *Transport) Receive(ctx context.Context) (*commit.Commit, func(path string) (io.ReadCloser, error), error) {
	return nil, nil, errors.New("lan.Receive: not implemented yet")
}

// ListPeers devolve os peers atualmente descobertos.
// TODO: ler do cache populado pelo discovery.
func (t *Transport) ListPeers(ctx context.Context) ([]transport.Peer, error) {
	return nil, nil
}

// Close libera recursos do transport.
func (t *Transport) Close() error {
	return nil
}

// compile-time check de que satisfazemos a interface.
var _ transport.Transport = (*Transport)(nil)
