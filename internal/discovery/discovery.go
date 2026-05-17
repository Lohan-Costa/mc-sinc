// Package discovery anuncia e descobre nós MC Sinc na LAN via mDNS.
//
// Cada nó publica um serviço `_mcsinc._tcp` contendo:
//   - hostname
//   - identificador do usuário (--user)
//   - porta HTTP local
//   - versão do MC Sinc
//
// Outros nós escutam o mesmo serviço e populam um cache de Peers.
package discovery

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/mdns"

	"github.com/Lohan-Costa/mc-sinc/internal/transport"
)

const (
	serviceName = "_mcsinc._tcp"
	domain      = "local."
	browseEvery = 10 * time.Second
)

// Discovery cuida do anúncio + browse simultâneos do serviço mDNS.
type Discovery struct {
	user    string
	port    int
	version string

	server *mdns.Server

	mu    sync.RWMutex
	peers map[string]transport.Peer
}

// New configura mas não inicia o discovery.
func New(user string, port int, version string) *Discovery {
	return &Discovery{
		user:    user,
		port:    port,
		version: version,
		peers:   make(map[string]transport.Peer),
	}
}

// Run inicia anúncio + browse. Bloqueia até `ctx` ser cancelado.
func (d *Discovery) Run(ctx context.Context) error {
	host, _ := os.Hostname()

	svc, err := mdns.NewMDNSService(
		d.user,                                 // instance name
		serviceName,                            // service
		domain,                                 // domain
		host+".",                               // host
		d.port,                                 // port
		nil,                                    // IPs (auto)
		[]string{"user=" + d.user, "v=" + d.version}, // TXT records
	)
	if err != nil {
		return fmt.Errorf("mdns service: %w", err)
	}

	server, err := mdns.NewServer(&mdns.Config{Zone: svc})
	if err != nil {
		return fmt.Errorf("mdns server: %w", err)
	}
	d.server = server

	go d.browseLoop(ctx)

	<-ctx.Done()
	return d.server.Shutdown()
}

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

func (d *Discovery) browseOnce(ctx context.Context) {
	entries := make(chan *mdns.ServiceEntry, 16)
	params := mdns.DefaultParams(serviceName)
	params.Entries = entries
	params.Timeout = 3 * time.Second

	go func() {
		_ = mdns.Query(params)
		close(entries)
	}()

	for e := range entries {
		p := transport.Peer{
			ID:   e.Name,
			Name: e.Host,
			Addr: fmt.Sprintf("%s:%d", e.AddrV4, e.Port),
		}
		for _, txt := range e.InfoFields {
			if len(txt) > 5 && txt[:5] == "user=" {
				p.ID = txt[5:]
			}
			if len(txt) > 2 && txt[:2] == "v=" {
				p.Version = txt[2:]
			}
		}
		d.mu.Lock()
		d.peers[p.ID] = p
		d.mu.Unlock()
	}
}

// Peers devolve um snapshot dos peers descobertos.
func (d *Discovery) Peers() []transport.Peer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]transport.Peer, 0, len(d.peers))
	for _, p := range d.peers {
		out = append(out, p)
	}
	return out
}
