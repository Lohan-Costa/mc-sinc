// Package transport define como commits e arquivos são trocados entre peers.
//
// A interface é intencionalmente mínima. A primeira implementação (LAN, em
// `transport/lan`) usa mDNS + HTTP. Implementações futuras (WAN com relay,
// transfer assíncrono via S3, etc.) só precisam satisfazer essa interface.
package transport

import (
	"context"
	"io"

	"github.com/Lohan-Costa/mc-sinc/internal/commit"
)

// Peer representa outro editor descoberto pela rede.
type Peer struct {
	ID      string // identificador estável (geralmente o --user)
	Name    string // hostname amigável
	Addr    string // host:port para HTTP direto
	Version string // versão do MC Sinc do peer
}

// Transport é o contrato comum entre LAN, WAN, etc.
type Transport interface {
	// Send anuncia um commit + envia os bytes dos arquivos para todos os peers.
	// `open` é uma função que devolve um Reader para o conteúdo de cada path
	// listado no commit (permite streaming sem carregar tudo na memória).
	Send(ctx context.Context, c *commit.Commit, open func(path string) (io.ReadCloser, error)) error

	// Receive bloqueia até receber um commit de um peer.
	// Devolve o commit e uma função `pull` que o caller chama para baixar
	// efetivamente cada arquivo (assim o usuário pode escolher se quer ou não).
	Receive(ctx context.Context) (*commit.Commit, func(path string) (io.ReadCloser, error), error)

	// ListPeers devolve os peers vistos no momento.
	ListPeers(ctx context.Context) ([]Peer, error)

	// Close encerra qualquer recurso aberto (sockets, listeners, etc.).
	Close() error
}
