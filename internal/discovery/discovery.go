// Package discovery anuncia e descobre nós MC Sinc na LAN via mDNS.
//
// Cada nó publica um serviço `_mcsinc._tcp` contendo:
//   - hostname
//   - identificador do usuário (--user)
//   - porta HTTP local
//   - versão do MC Sinc
//
// Outros nós escutam o mesmo serviço e populam um cache de Peers.
//
// Esta implementação usa `libp2p/zeroconf/v2` em vez de `hashicorp/mdns`
// (que tinha problema conhecido no macOS: o `mDNSResponder` do SO segura
// a porta UDP 5353, e o hashicorp/mdns não consegue receber respostas de
// browse — só o advertise saía. zeroconf coexiste corretamente).
package discovery

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/zeroconf/v2"

	"github.com/Lohan-Costa/mc-sinc/internal/transport"
)

const (
	serviceName = "_mcsinc._tcp"
	domain      = "local."
	browseEvery = 10 * time.Second
	browseRound = 3 * time.Second

	// peerTTL: peer sem ser visto por mais de peerTTL é considerado offline.
	// 30s = 3x browseEvery, tolera 1 round perdida sem sumir cedo demais.
	peerTTL = 30 * time.Second
)

// peerState carrega o último Peer visto + quando.
type peerState struct {
	peer     transport.Peer
	lastSeen time.Time
}

// Discovery cuida do anúncio + browse simultâneos do serviço mDNS.
type Discovery struct {
	user    string
	port    int
	version string

	server *zeroconf.Server

	mu    sync.RWMutex
	peers map[string]*peerState
}

// New configura mas não inicia o discovery.
func New(user string, port int, version string) *Discovery {
	return &Discovery{
		user:    user,
		port:    port,
		version: version,
		peers:   make(map[string]*peerState),
	}
}

// Run inicia anúncio + browse. Bloqueia até `ctx` ser cancelado.
func (d *Discovery) Run(ctx context.Context) error {
	server, err := zeroconf.Register(
		d.user,                                         // instance name
		serviceName,                                    // service type
		domain,                                         // domain
		d.port,                                         // port HTTP
		[]string{"user=" + d.user, "v=" + d.version},   // TXT records
		nil,                                            // ifaces — nil = todas
	)
	if err != nil {
		return fmt.Errorf("zeroconf register: %w", err)
	}
	d.server = server
	defer d.server.Shutdown()

	go d.browseLoop(ctx)

	<-ctx.Done()
	return nil
}

// browseLoop chama browseOnce a cada `browseEvery`.
func (d *Discovery) browseLoop(ctx context.Context) {
	tick := time.NewTicker(browseEvery)
	defer tick.Stop()

	d.browseOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			d.browseOnce(ctx)
		}
	}
}

// browseOnce roda uma janela de `browseRound` segundos consumindo entries.
// O context.WithTimeout encerra o Browse — a goroutine de produção do
// zeroconf fecha o canal e o for-range sai naturalmente.
func (d *Discovery) browseOnce(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, browseRound)
	defer cancel()

	entries := make(chan *zeroconf.ServiceEntry, 16)
	go func() {
		if err := zeroconf.Browse(ctx, serviceName, domain, entries); err != nil {
			log.Printf("discovery: browse: %v", err)
		}
	}()

	for e := range entries {
		p := transport.Peer{
			ID:   e.Instance,
			Name: e.HostName,
			Addr: addressFrom(e),
		}
		for _, txt := range e.Text {
			switch {
			case strings.HasPrefix(txt, "user="):
				p.ID = strings.TrimPrefix(txt, "user=")
			case strings.HasPrefix(txt, "v="):
				p.Version = strings.TrimPrefix(txt, "v=")
			}
		}
		// Ignora o próprio anúncio — o zeroconf devolve o self também.
		if p.ID == d.user {
			continue
		}
		d.mu.Lock()
		d.peers[p.ID] = &peerState{peer: p, lastSeen: time.Now()}
		d.mu.Unlock()
	}
}

// addressFrom monta "host:port" priorizando IPv4 (mais comum em LAN
// doméstica) com fallback IPv6.
func addressFrom(e *zeroconf.ServiceEntry) string {
	if len(e.AddrIPv4) > 0 {
		return fmt.Sprintf("%s:%d", e.AddrIPv4[0], e.Port)
	}
	if len(e.AddrIPv6) > 0 {
		return fmt.Sprintf("[%s]:%d", e.AddrIPv6[0], e.Port)
	}
	return ""
}

// Peers devolve um snapshot dos peers descobertos cujo lastSeen
// é mais recente que peerTTL. Peers stale são filtrados no read e
// continuam no map até a próxima atualização ou um GC futuro.
func (d *Discovery) Peers() []transport.Peer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	cutoff := time.Now().Add(-peerTTL)
	out := make([]transport.Peer, 0, len(d.peers))
	for _, st := range d.peers {
		if st.lastSeen.Before(cutoff) {
			continue
		}
		out = append(out, st.peer)
	}
	return out
}
