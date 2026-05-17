package lan

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/go-chi/chi/v5"

	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
	"github.com/Lohan-Costa/mc-sinc/internal/transport"
)

// stubPeers satisfaz PeerSource para testes.
type stubPeers struct{ list []transport.Peer }

func (s *stubPeers) Peers() []transport.Peer { return s.list }

// node embala uma instância LAN com seu manifest e httptest.Server.
type node struct {
	user   string
	root   string
	store  *manifest.Store
	tport  *Transport
	server *httptest.Server
}

func newNode(t *testing.T, user string) *node {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "1"), 0o755); err != nil {
		t.Fatal(err)
	}

	store, err := manifest.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tport := New(user, 0, root, store, &stubPeers{})
	r := chi.NewRouter()
	r.Mount("/peer", tport.Routes())
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	return &node{user: user, root: root, store: store, tport: tport, server: srv}
}

// addr extrai host:port da URL do httptest.
func (n *node) addr(t *testing.T) string {
	u, err := url.Parse(n.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// writeMXF cria um .mxf físico na pasta MXF/1 do node e devolve o
// FileSpec correspondente (xxhash já computado).
func (n *node) writeMXF(t *testing.T, name string, payload []byte) commit.FileSpec {
	t.Helper()
	rel := filepath.Join("1", name)
	full := filepath.Join(n.root, rel)
	if err := os.WriteFile(full, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	return commit.FileSpec{
		Path: rel,
		Hash: fmt.Sprintf("%016x", xxhash.Sum64(payload)),
		Size: int64(len(payload)),
	}
}

func TestSendAnnouncePersistsOnReceiver(t *testing.T) {
	alice := newNode(t, "alice")
	bob := newNode(t, "bob")

	spec := alice.writeMXF(t, "clip.mxf", []byte("scene 1"))

	// Marca o commit como sent no manifest do Alice (necessário p/ /peer/files validar dono).
	c := &commit.Commit{
		ID:        "deadbeefdeadbeef",
		Author:    "alice",
		Message:   "scenes 1-3",
		Files:     []commit.FileSpec{spec},
		CreatedAt: time.Now(),
	}
	saveSent(t, alice.store, c)

	// Alice "descobre" o Bob.
	alice.tport.discov = &stubPeers{list: []transport.Peer{
		{ID: "bob", Addr: bob.addr(t)},
	}}

	if err := alice.tport.Send(context.Background(), c); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Bob deve ter o anúncio gravado.
	got, err := bob.store.GetCommit(c.ID)
	if err != nil {
		t.Fatalf("bob get commit: %v", err)
	}
	if got.Author != "alice" || got.Direction != manifest.DirectionReceived {
		t.Errorf("commit estado: %+v", got)
	}
	if got.Status != manifest.CommitStatusAnnounced {
		t.Errorf("status: %q", got.Status)
	}
	if len(got.Files) != 1 || got.Files[0].Hash != spec.Hash {
		t.Errorf("files: %+v", got.Files)
	}
}

func TestPullDownloadsFilesWithHashCheck(t *testing.T) {
	alice := newNode(t, "alice")
	bob := newNode(t, "bob")

	payload := []byte("the avid never sleeps")
	spec := alice.writeMXF(t, "A001.mxf", payload)

	c := &commit.Commit{
		ID:        "feedbeefcafe0001",
		Author:    "alice",
		Message:   "first pull",
		Files:     []commit.FileSpec{spec},
		CreatedAt: time.Now(),
	}
	saveSent(t, alice.store, c)

	// Send para Bob.
	alice.tport.discov = &stubPeers{list: []transport.Peer{{ID: "bob", Addr: bob.addr(t)}}}
	if err := alice.tport.Send(context.Background(), c); err != nil {
		t.Fatal(err)
	}

	// Bob precisa "saber" o address do Alice via discovery quando for puxar.
	bob.tport.discov = &stubPeers{list: []transport.Peer{{ID: "alice", Addr: alice.addr(t)}}}

	if err := bob.tport.Pull(context.Background(), c.ID); err != nil {
		t.Fatalf("pull: %v", err)
	}

	// Arquivo aterrissou em MXF/1-alice/A001.mxf
	final := filepath.Join(bob.root, "1-alice", "A001.mxf")
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read pulled: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("conteúdo divergente")
	}

	// Manifest do Bob tem o arquivo como received.
	files, _ := bob.store.ByStatus(manifest.StatusReceived)
	if len(files) != 1 || files[0].Hash != spec.Hash {
		t.Errorf("manifest received: %+v", files)
	}

	// Status do commit no Bob: pulled.
	updated, _ := bob.store.GetCommit(c.ID)
	if updated.Status != manifest.CommitStatusPulled {
		t.Errorf("status: %q", updated.Status)
	}
}

func TestPullRejectsOnHashMismatch(t *testing.T) {
	alice := newNode(t, "alice")
	bob := newNode(t, "bob")

	payload := []byte("real content")
	rel := filepath.Join("1", "X.mxf")
	if err := os.WriteFile(filepath.Join(alice.root, rel), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	// Anuncia com hash mentiroso.
	c := &commit.Commit{
		ID:     "deadbeefdeadcafe",
		Author: "alice",
		Files: []commit.FileSpec{{
			Path: rel,
			Hash: "0000000000000000", // não bate
			Size: int64(len(payload)),
		}},
		CreatedAt: time.Now(),
	}
	saveSent(t, alice.store, c)

	alice.tport.discov = &stubPeers{list: []transport.Peer{{ID: "bob", Addr: bob.addr(t)}}}
	if err := alice.tport.Send(context.Background(), c); err != nil {
		t.Fatal(err)
	}

	bob.tport.discov = &stubPeers{list: []transport.Peer{{ID: "alice", Addr: alice.addr(t)}}}
	_ = bob.tport.Pull(context.Background(), c.ID) // não panica; deve só falhar internamente

	// Arquivo NÃO deve existir no destino.
	final := filepath.Join(bob.root, "1-alice", "X.mxf")
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("arquivo não deveria existir, mas existe: %v", err)
	}
	// Tampouco o temporário.
	if _, err := os.Stat(final + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part deveria ter sido limpo: %v", err)
	}

	// Status do commit no Bob: failed.
	updated, _ := bob.store.GetCommit(c.ID)
	if updated.Status != manifest.CommitStatusFailed {
		t.Errorf("status: %q want failed", updated.Status)
	}
}

func TestPeerFilesRefusesNonSentCommit(t *testing.T) {
	alice := newNode(t, "alice")
	// Salva um commit no manifest do Alice como direction=received (não-dono).
	c := manifest.Commit{
		ID:        "abcd1234abcd1234",
		Author:    "carol",
		Message:   "not ours",
		CreatedAt: time.Now(),
		Direction: manifest.DirectionReceived,
		PeerAddr:  "carol@10.0.0.99",
		Status:    manifest.CommitStatusAnnounced,
		Files:     []manifest.CommitFile{{Path: "1/foo.mxf", Hash: "deadbeef", Size: 10}},
	}
	if err := alice.store.SaveCommit(c); err != nil {
		t.Fatal(err)
	}

	// Pedir o arquivo deve retornar 404 (commit não é direction=sent).
	resp, err := alice.server.Client().Get(alice.server.URL + "/peer/files/abcd1234abcd1234/1/foo.mxf")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status: %d want 404", resp.StatusCode)
	}
}

// saveSent persiste um commit no manifest do sender com direction=sent
// (necessário pro /peer/files autorizar leitura).
func saveSent(t *testing.T, store *manifest.Store, c *commit.Commit) {
	t.Helper()
	files := make([]manifest.CommitFile, 0, len(c.Files))
	for _, f := range c.Files {
		files = append(files, manifest.CommitFile{Path: f.Path, Hash: f.Hash, Size: f.Size})
	}
	mc := manifest.Commit{
		ID:        c.ID,
		Author:    c.Author,
		Message:   c.Message,
		CreatedAt: c.CreatedAt,
		Direction: manifest.DirectionSent,
		Status:    manifest.CommitStatusAnnounced,
		Files:     files,
	}
	if err := store.SaveCommit(mc); err != nil {
		t.Fatal(err)
	}
}

// Garante que pathEscapeSegments mantém as `/` mas escapa caracteres dentro
// dos segmentos. Importante para nomes como "A001 (take 2).mxf".
func TestPathEscapeSegments(t *testing.T) {
	got := pathEscapeSegments("1/A001 (take 2).mxf")
	if !strings.Contains(got, "/") {
		t.Errorf("manteve / literal: %q", got)
	}
	if strings.Contains(got, " ") {
		t.Errorf("não escapou espaço: %q", got)
	}
}
