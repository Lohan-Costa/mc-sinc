package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Lohan-Costa/mc-sinc/internal/api"
	"github.com/Lohan-Costa/mc-sinc/internal/commit"
	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
	"github.com/Lohan-Costa/mc-sinc/internal/transport"
)

// stubTransport satisfaz transport.Transport e registra invocações pra
// asserts. Send pode bloquear via blockSend pra exercitar Wait().
type stubTransport struct {
	mu         sync.Mutex
	sentIDs    []string
	pulledIDs  []string
	blockSend  chan struct{} // se != nil, Send espera receber dele antes de retornar
	sendCalled chan struct{} // sinaliza que Send foi chamado
}

func (s *stubTransport) Send(ctx context.Context, c *commit.Commit) error {
	s.mu.Lock()
	s.sentIDs = append(s.sentIDs, c.ID)
	s.mu.Unlock()
	if s.sendCalled != nil {
		select {
		case s.sendCalled <- struct{}{}:
		default:
		}
	}
	if s.blockSend != nil {
		select {
		case <-s.blockSend:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *stubTransport) Pull(ctx context.Context, id string) error {
	s.mu.Lock()
	s.pulledIDs = append(s.pulledIDs, id)
	s.mu.Unlock()
	return nil
}

func (s *stubTransport) ListPeers(ctx context.Context) ([]transport.Peer, error) {
	return nil, nil
}

func (s *stubTransport) Routes() chi.Router { return chi.NewRouter() }
func (s *stubTransport) Close() error       { return nil }

func (s *stubTransport) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.sentIDs))
	copy(out, s.sentIDs)
	return out
}

func (s *stubTransport) pulled() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.pulledIDs))
	copy(out, s.pulledIDs)
	return out
}

// testEnv junta um Server + dependências pra cada teste.
type testEnv struct {
	srv   *api.Server
	store *manifest.Store
	tport *stubTransport
	mux   http.Handler
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	store, err := manifest.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("manifest.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tport := &stubTransport{}

	srv := api.New(api.Config{
		User:      "alice",
		Root:      t.TempDir(),
		Version:   "0.0.0-test",
		Store:     store,
		Commits:   commit.New(store, "alice"),
		Transport: tport,
		// Discovery, Web, AvidProcess deliberadamente nil/zero — handleStatus
		// faz nil-check em discovery, web só serve /*, AvidProcess vira string
		// vazia (avid.Detect tenta detectar com default).
	})

	return &testEnv{
		srv:   srv,
		store: store,
		tport: tport,
		mux:   srv.Handler(),
	}
}

func (e *testEnv) request(t *testing.T, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var bodyR *bytes.Reader
	if body != nil {
		bodyR = bytes.NewReader(body)
	} else {
		bodyR = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, bodyR)
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, req)
	return rr
}

func TestHandleStatusEmptyPeers(t *testing.T) {
	e := newTestEnv(t)
	rr := e.request(t, "GET", "/status", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status code: %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		User    string   `json:"user"`
		Version string   `json:"version"`
		Peers   []string `json:"peers"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.User != "alice" {
		t.Errorf("user=%q, esperava alice", resp.User)
	}
	if resp.Version != "0.0.0-test" {
		t.Errorf("version=%q", resp.Version)
	}
	if len(resp.Peers) != 0 {
		t.Errorf("peers=%v, esperava vazio", resp.Peers)
	}
}

func TestHandlePendingListaDiscoveredEStaged(t *testing.T) {
	e := newTestEnv(t)
	must(t, e.store.Upsert(manifest.File{
		Path: "1/a.mxf", Hash: "deadbeef", Size: 100,
		ModifiedAt: time.Now(), Status: manifest.StatusDiscovered,
	}))
	must(t, e.store.Upsert(manifest.File{
		Path: "1/b.mxf", Hash: "feedbeef", Size: 200,
		ModifiedAt: time.Now(), Status: manifest.StatusStaged,
	}))
	must(t, e.store.Upsert(manifest.File{
		Path: "1/c.mxf", Hash: "cafe", Size: 300,
		ModifiedAt: time.Now(), Status: manifest.StatusCommitted, // não deve aparecer
	}))

	rr := e.request(t, "GET", "/pending", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	var files []manifest.File
	if err := json.Unmarshal(rr.Body.Bytes(), &files); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	paths := map[string]manifest.Status{}
	for _, f := range files {
		paths[f.Path] = f.Status
	}
	if len(paths) != 2 || paths["1/a.mxf"] != manifest.StatusDiscovered || paths["1/b.mxf"] != manifest.StatusStaged {
		t.Errorf("pending=%v, esperava a=discovered e b=staged", paths)
	}
}

func TestHandleStageMudaStatus(t *testing.T) {
	e := newTestEnv(t)
	must(t, e.store.Upsert(manifest.File{
		Path: "1/a.mxf", Hash: "deadbeef", Size: 100,
		ModifiedAt: time.Now(), Status: manifest.StatusDiscovered,
	}))

	body, _ := json.Marshal(map[string]string{"path": "1/a.mxf"})
	rr := e.request(t, "POST", "/stage", body)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("code=%d, body=%s", rr.Code, rr.Body.String())
	}

	files, err := e.store.ByStatus(manifest.StatusStaged)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "1/a.mxf" {
		t.Errorf("staged=%v, esperava 1/a.mxf", files)
	}
}

func TestHandleStageSemPathDevolve400(t *testing.T) {
	e := newTestEnv(t)
	body, _ := json.Marshal(map[string]string{})
	rr := e.request(t, "POST", "/stage", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperava 400", rr.Code)
	}
}

func TestHandleCommitInclueStagedComHash(t *testing.T) {
	e := newTestEnv(t)
	must(t, e.store.Upsert(manifest.File{
		Path: "1/com_hash.mxf", Hash: "deadbeef", Size: 100,
		ModifiedAt: time.Now(), Status: manifest.StatusStaged,
	}))
	must(t, e.store.Upsert(manifest.File{
		Path: "1/sem_hash.mxf", Hash: "", Size: 200,
		ModifiedAt: time.Now(), Status: manifest.StatusStaged,
	}))
	must(t, e.store.Upsert(manifest.File{
		Path: "1/discovered.mxf", Hash: "feedbeef", Size: 300,
		ModifiedAt: time.Now(), Status: manifest.StatusDiscovered, // não staged: ignorado
	}))

	body, _ := json.Marshal(map[string]string{"message": "smoke"})
	rr := e.request(t, "POST", "/commit", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d, body=%s", rr.Code, rr.Body.String())
	}
	var c commit.Commit
	if err := json.Unmarshal(rr.Body.Bytes(), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(c.Files) != 1 || c.Files[0].Path != "1/com_hash.mxf" {
		t.Errorf("files=%v, esperava só com_hash.mxf", c.Files)
	}
	if c.Message != "smoke" {
		t.Errorf("message=%q", c.Message)
	}

	sent, err := e.store.ListCommits(manifest.DirectionSent, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].ID != c.ID {
		t.Errorf("persisted commits=%v", sent)
	}

	if err := e.srv.Wait(timeoutCtx(t, 2*time.Second)); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := e.tport.sent(); len(got) != 1 || got[0] != c.ID {
		t.Errorf("transport.Send chamou com %v, esperava [%s]", got, c.ID)
	}
}

func TestHandleCommitSemStagedDevolve400(t *testing.T) {
	e := newTestEnv(t)
	// manifest vazio: nada staged
	body, _ := json.Marshal(map[string]string{"message": "vazio"})
	rr := e.request(t, "POST", "/commit", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperava 400; body=%s", rr.Code, rr.Body.String())
	}
	if got := e.tport.sent(); len(got) != 0 {
		t.Errorf("transport.Send foi chamado: %v", got)
	}
}

func TestHandleCommitStagedSemHashDevolve400(t *testing.T) {
	e := newTestEnv(t)
	must(t, e.store.Upsert(manifest.File{
		Path: "1/a.mxf", Hash: "", Size: 100,
		ModifiedAt: time.Now(), Status: manifest.StatusStaged,
	}))
	body, _ := json.Marshal(map[string]string{"message": "msg"})
	rr := e.request(t, "POST", "/commit", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, esperava 400", rr.Code)
	}
}

func TestHandleUnstageVoltaParaDiscovered(t *testing.T) {
	e := newTestEnv(t)
	must(t, e.store.Upsert(manifest.File{
		Path: "1/a.mxf", Hash: "deadbeef", Size: 100,
		ModifiedAt: time.Now(), Status: manifest.StatusStaged,
	}))

	body, _ := json.Marshal(map[string]string{"path": "1/a.mxf"})
	rr := e.request(t, "POST", "/unstage", body)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("code=%d, body=%s", rr.Code, rr.Body.String())
	}
	files, err := e.store.ByStatus(manifest.StatusDiscovered)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "1/a.mxf" {
		t.Errorf("discovered=%v", files)
	}
}

func TestHandlePullDevolve202EchamaTransport(t *testing.T) {
	e := newTestEnv(t)
	must(t, e.store.SaveCommit(manifest.Commit{
		ID:        "c-pull",
		Author:    "bob",
		Direction: manifest.DirectionReceived,
		Status:    manifest.CommitStatusAnnounced,
		Files:     []manifest.CommitFile{{Path: "1/x.mxf", Hash: "h", Size: 1}},
	}))

	rr := e.request(t, "POST", "/commits/c-pull/pull", nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("code=%d, esperava 202", rr.Code)
	}

	if err := e.srv.Wait(timeoutCtx(t, 2*time.Second)); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := e.tport.pulled(); len(got) != 1 || got[0] != "c-pull" {
		t.Errorf("transport.Pull chamou com %v", got)
	}
}

func TestWaitEsperaSendTerminar(t *testing.T) {
	// Stub bloqueia o Send até liberarmos — Wait com timeout curto deve
	// expirar; com timeout maior + liberação deve retornar nil.
	store, err := manifest.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	must(t, store.Upsert(manifest.File{
		Path: "1/a.mxf", Hash: "deadbeef", Size: 100,
		ModifiedAt: time.Now(), Status: manifest.StatusStaged,
	}))

	tport := &stubTransport{
		blockSend:  make(chan struct{}),
		sendCalled: make(chan struct{}, 1),
	}
	srv := api.New(api.Config{
		User:      "alice",
		Root:      t.TempDir(),
		Version:   "v",
		Store:     store,
		Commits:   commit.New(store, "alice"),
		Transport: tport,
	})
	mux := srv.Handler()

	body, _ := json.Marshal(map[string]string{"message": "blocked"})
	req := httptest.NewRequest("POST", "/commit", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}

	// Espera o Send ter sido chamado pelo menos uma vez.
	select {
	case <-tport.sendCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("Send nao foi chamado em 2s")
	}

	// Wait com timeout curto deve estourar (Send ainda bloqueado).
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := srv.Wait(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Wait deveria estourar; got=%v", err)
	}

	// Libera Send; Wait com timeout maior agora deve retornar nil.
	close(tport.blockSend)
	if err := srv.Wait(timeoutCtx(t, 2*time.Second)); err != nil {
		t.Errorf("Wait apos liberar: %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func timeoutCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
