// Package transport define como commits e arquivos são trocados entre peers.
//
// A interface é intencionalmente mínima. A primeira implementação (LAN, em
// `transport/lan`) usa mDNS + HTTP. Implementações futuras (WAN com relay,
// transfer assíncrono via S3, etc.) só precisam satisfazer essa interface.
package transport

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/Lohan-Costa/mc-sinc/internal/commit"
)

// Peer representa outro editor descoberto pela rede.
type Peer struct {
	ID      string // identificador estável (geralmente o --user)
	Name    string // hostname amigável
	Addr    string // host:port para HTTP direto
	Version string // versão do MC Sinc do peer
}

// InventoryItem descreve um arquivo que um peer tem em sua pasta
// `1-<requesting_user>/`. Devolvido por /peer/inventory.
type InventoryItem struct {
	Path string `json:"path"` // forward slash, relativo à raiz MXF do peer (ex: "1-alice/clip.mxf")
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

// Transport é o contrato comum entre LAN, WAN, etc.
//
// O modelo é pull explícito: Send anuncia metadata aos peers; bytes só
// viajam quando o receiver chama Pull. Receivers materializam anúncios no
// manifest local — não há método `Receive` bloqueante.
type Transport interface {
	// Send anuncia um commit (metadata + lista de FileSpec) aos peers conhecidos.
	// Falhas individuais por peer não fazem o commit local falhar.
	Send(ctx context.Context, c *commit.Commit) error

	// SendTo é como Send mas anuncia pra UM peer específico — usado pelo
	// "Sincronizar com X" da UI pra mandar um commit delta direcionado.
	SendTo(ctx context.Context, peer Peer, c *commit.Commit) error

	// Pull baixa os arquivos de um commit recebido. Itera commit_files,
	// faz fetch byte-streamed do sender, verifica xxhash64 e grava no disco
	// local sob `MXF/1-<author>/<filename>`.
	Pull(ctx context.Context, commitID string) error

	// Inventory pergunta a um peer o que ele tem na pasta
	// `1-<requestingUser>/`. Sender usa pra montar lista delta antes de
	// criar um sync commit.
	Inventory(ctx context.Context, peer Peer, requestingUser string) ([]InventoryItem, error)

	// ListPeers devolve os peers vistos no momento.
	ListPeers(ctx context.Context) ([]Peer, error)

	// Routes devolve o subrouter HTTP com os endpoints peer-facing
	// (POST /commits, GET /files/...). O caller monta sob o prefixo /peer.
	Routes() chi.Router

	// Close encerra qualquer recurso aberto (sockets, listeners, etc.).
	Close() error
}
