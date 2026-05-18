package discovery

import (
	"net"
	"testing"
	"time"

	"github.com/Lohan-Costa/mc-sinc/internal/transport"
)

func ip(s string) net.IP { return net.ParseIP(s) }

// Peers() deve filtrar peers stale (lastSeen > peerTTL atrás) E
// removê-los do map — prevenir acúmulo perpétuo de entradas mortas.
func TestPeersExpiraEDeleta(t *testing.T) {
	d := New("alice", 7777, "v")
	d.mu.Lock()
	d.peers["fresh"] = &peerState{
		peer:     transport.Peer{ID: "fresh"},
		lastSeen: time.Now(),
	}
	d.peers["stale"] = &peerState{
		peer:     transport.Peer{ID: "stale"},
		lastSeen: time.Now().Add(-2 * peerTTL),
	}
	d.mu.Unlock()

	got := d.Peers()
	if len(got) != 1 || got[0].ID != "fresh" {
		t.Errorf("Peers()=%v, esperava apenas fresh", got)
	}

	// stale deve ter sido removido do map.
	d.mu.RLock()
	_, stillThere := d.peers["stale"]
	d.mu.RUnlock()
	if stillThere {
		t.Errorf("peer stale continua no map apos Peers()")
	}
}

func TestPickReachableIP_PrefereMesmaSubnet(t *testing.T) {
	peer := []net.IP{
		ip("192.168.56.1"),  // VirtualBox - inacessível
		ip("192.168.1.7"),   // Wi-Fi real - alcançável
	}
	local := []net.IP{ip("192.168.1.12")}

	got := pickReachableIP(peer, local)
	if got == nil || got.String() != "192.168.1.7" {
		t.Errorf("got %v, want 192.168.1.7", got)
	}
}

func TestPickReachableIP_SemMatchRetornaNil(t *testing.T) {
	peer := []net.IP{ip("10.0.0.1"), ip("172.16.0.5")}
	local := []net.IP{ip("192.168.1.12")}

	got := pickReachableIP(peer, local)
	if got != nil {
		t.Errorf("got %v, want nil (caller usa fallback)", got)
	}
}

func TestPickReachableIP_PrimeiroMatchVence(t *testing.T) {
	peer := []net.IP{ip("192.168.1.7"), ip("192.168.1.8")}
	local := []net.IP{ip("192.168.1.12")}

	got := pickReachableIP(peer, local)
	if got == nil || got.String() != "192.168.1.7" {
		t.Errorf("got %v, want 192.168.1.7 (primeiro match)", got)
	}
}

func TestPickReachableIP_IgnoraIPv6(t *testing.T) {
	peer := []net.IP{ip("fe80::1"), ip("192.168.1.7")}
	local := []net.IP{ip("192.168.1.12")}

	got := pickReachableIP(peer, local)
	if got == nil || got.String() != "192.168.1.7" {
		t.Errorf("got %v, want 192.168.1.7 (IPv6 deve ser pulado)", got)
	}
}

func TestPickReachableIP_ListaVazia(t *testing.T) {
	if got := pickReachableIP(nil, []net.IP{ip("192.168.1.1")}); got != nil {
		t.Errorf("peer vazio: got %v, want nil", got)
	}
	if got := pickReachableIP([]net.IP{ip("192.168.1.1")}, nil); got != nil {
		t.Errorf("local vazio: got %v, want nil", got)
	}
}
