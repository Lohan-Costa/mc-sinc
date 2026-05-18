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
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/zeroconf/v2"

	"github.com/Lohan-Costa/mc-sinc/internal/transport"
)

const logModule = "discovery"

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

	mu       sync.RWMutex
	peers    map[string]*peerState
	localIPs []net.IP // populado no Run após usefulInterfaces; usado pra pickReachableIP
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
	ifaces, localIPs := usefulInterfaces(ctx)
	d.mu.Lock()
	d.localIPs = localIPs
	d.mu.Unlock()

	server, err := zeroconf.Register(
		d.user,                                         // instance name
		serviceName,                                    // service type
		domain,                                         // domain
		d.port,                                         // port HTTP
		[]string{"user=" + d.user, "v=" + d.version},   // TXT records
		ifaces,                                         // interfaces filtradas
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
			slog.WarnContext(ctx, "browse mDNS retornou erro",
				slog.String("module", logModule),
				slog.String("event_id", "BROWSE_FAIL"),
				slog.String("error", err.Error()))
		}
	}()

	d.mu.RLock()
	localIPs := d.localIPs
	d.mu.RUnlock()

	for e := range entries {
		p := transport.Peer{
			ID:   e.Instance,
			Name: e.HostName,
			Addr: addressFrom(e, localIPs),
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
		_, alreadyKnown := d.peers[p.ID]
		d.peers[p.ID] = &peerState{peer: p, lastSeen: time.Now()}
		d.mu.Unlock()
		if !alreadyKnown {
			slog.InfoContext(ctx, "peer descoberto na LAN",
				slog.String("module", logModule),
				slog.String("event_id", "PEER_DISCOVERED"),
				slog.String("peer_id", p.ID),
				slog.String("peer_addr", p.Addr),
				slog.String("peer_version", p.Version))
		}
	}
}

// addressFrom monta "host:port" priorizando IPv4 alcançável da nossa máquina
// (mesma subnet /24 que algum IP local). Fallback: primeiro IPv4, depois
// primeiro IPv6.
//
// Sender pode anunciar em várias interfaces (Wi-Fi + Ethernet + VirtualBox
// + Hyper-V + ...), todas viram entradas em AddrIPv4. Pegar a primeira às
// cegas roteava pra VirtualBox host-only adapter no Windows do Lohan
// (192.168.56.0/24) — Mac não tinha rota e timeout no POST.
func addressFrom(e *zeroconf.ServiceEntry, localIPs []net.IP) string {
	if ip := pickReachableIP(e.AddrIPv4, localIPs); ip != nil {
		return fmt.Sprintf("%s:%d", ip, e.Port)
	}
	if len(e.AddrIPv4) > 0 {
		return fmt.Sprintf("%s:%d", e.AddrIPv4[0], e.Port)
	}
	if len(e.AddrIPv6) > 0 {
		return fmt.Sprintf("[%s]:%d", e.AddrIPv6[0], e.Port)
	}
	return ""
}

// pickReachableIP escolhe o primeiro peerIP que está na mesma subnet /24
// que algum IP local — heurística simples mas eficaz pra rede doméstica.
// Devolve nil se nenhum casar (caller cai pro fallback).
func pickReachableIP(peerIPs []net.IP, localIPs []net.IP) net.IP {
	for _, peer := range peerIPs {
		peer4 := peer.To4()
		if peer4 == nil {
			continue
		}
		for _, local := range localIPs {
			local4 := local.To4()
			if local4 == nil {
				continue
			}
			// /24: primeiros 3 octetos iguais.
			if peer4[0] == local4[0] && peer4[1] == local4[1] && peer4[2] == local4[2] {
				return peer4
			}
		}
	}
	return nil
}

// usefulInterfaces escolhe quais interfaces de rede o zeroconf vai usar.
// Preferência: a interface "default" (que tem rota pra Internet) — única,
// é o caso comum em laptops/desktops domésticos. Se não conseguir identificar,
// devolve todas as interfaces up + IPv4 + não-loopback.
//
// Devolve também os IPv4 locais úteis pra pickReachableIP no browse.
func usefulInterfaces(ctx context.Context) ([]net.Interface, []net.IP) {
	var defaultIP net.IP
	// Truque: net.Dial UDP pra 8.8.8.8 não conecta de verdade (UDP é
	// connectionless), mas o kernel resolve qual interface seria usada
	// se essa conexão acontecesse. Funciona offline também (apenas usa
	// a tabela de roteamento).
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			defaultIP = addr.IP
		}
		_ = conn.Close()
	}

	all, err := net.Interfaces()
	if err != nil {
		slog.WarnContext(ctx, "falha listando interfaces de rede",
			slog.String("module", logModule),
			slog.String("event_id", "INTERFACES_LIST_FAIL"),
			slog.String("error", err.Error()))
		return nil, nil
	}

	var (
		picked  []net.Interface
		localIPs []net.IP
		all4    []net.Interface
		allIPs  []net.IP
	)
	for _, iface := range all {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		var v4 []net.IP
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLinkLocalUnicast() {
				continue
			}
			v4 = append(v4, ip4)
		}
		if len(v4) == 0 {
			continue
		}
		all4 = append(all4, iface)
		allIPs = append(allIPs, v4...)
		if defaultIP != nil {
			for _, ip := range v4 {
				if ip.Equal(defaultIP.To4()) {
					picked = []net.Interface{iface}
					localIPs = v4
					slog.InfoContext(ctx, "interface escolhida para mDNS",
						slog.String("module", logModule),
						slog.String("event_id", "INTERFACE_SELECTED"),
						slog.String("name", iface.Name),
						slog.String("ip", ip.String()))
					return picked, localIPs
				}
			}
		}
	}

	// Fallback: tudo que sobrou.
	slog.InfoContext(ctx, "interface default não detectada — usando todas as não-loopback",
		slog.String("module", logModule),
		slog.String("event_id", "INTERFACE_FALLBACK_ALL"),
		slog.Int("count", len(all4)))
	return all4, allIPs
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
