package automode_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Lohan-Costa/mc-sinc/internal/automode"
	"github.com/Lohan-Costa/mc-sinc/internal/avid"
	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
)

// fakeStore satisfaz automode.Store sem precisar de SQLite. Devolve uma
// lista fixa por chave (dir+status concatenados).
type fakeStore struct {
	mu sync.Mutex
	// commits["received|announced"] = []manifest.Commit
	commits map[string][]manifest.Commit
}

func newFakeStore() *fakeStore {
	return &fakeStore{commits: map[string][]manifest.Commit{}}
}

func (f *fakeStore) set(dir manifest.Direction, status manifest.CommitStatus, list []manifest.Commit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits[string(dir)+"|"+string(status)] = list
}

func (f *fakeStore) ListCommits(dir manifest.Direction, status manifest.CommitStatus) ([]manifest.Commit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commits[string(dir)+"|"+string(status)], nil
}

// fakeTransport satisfaz automode.Transport. Registra invocações de Pull
// num slice protegido por mutex.
type fakeTransport struct {
	mu      sync.Mutex
	pulled  []string
	pullErr error
}

func (f *fakeTransport) Pull(ctx context.Context, id string) error {
	f.mu.Lock()
	f.pulled = append(f.pulled, id)
	f.mu.Unlock()
	return f.pullErr
}

func (f *fakeTransport) pulledIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.pulled))
	copy(out, f.pulled)
	return out
}

// runUntil sobe automode.Run em goroutine e cancela após `d`. Útil pra
// observar o efeito de N ticks.
func runUntil(t *testing.T, cfg automode.Config, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = automode.Run(ctx, cfg)
		close(done)
	}()
	time.Sleep(d)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run nao retornou apos cancel")
	}
}

func TestAutoModeDisparaEmStateIdle(t *testing.T) {
	store := newFakeStore()
	store.set(manifest.DirectionReceived, manifest.CommitStatusAnnounced, []manifest.Commit{
		{ID: "c1"},
		{ID: "c2"},
	})
	tport := &fakeTransport{}

	cfg := automode.Config{
		Detect: func() (avid.Snapshot, error) {
			return avid.Snapshot{State: avid.StateIdle}, nil
		},
		Store:     store,
		Transport: tport,
		Interval:  10 * time.Millisecond,
	}
	runUntil(t, cfg, 50*time.Millisecond)

	pulled := tport.pulledIDs()
	if len(pulled) < 2 {
		t.Fatalf("esperava pelo menos 2 pulls, got %v", pulled)
	}
	// Os 2 primeiros devem ser c1 e c2 (ordem da lista).
	if pulled[0] != "c1" || pulled[1] != "c2" {
		t.Errorf("ordem dos pulls inesperada: %v", pulled)
	}
}

func TestAutoModeNaoDisparaEmStateBusy(t *testing.T) {
	store := newFakeStore()
	store.set(manifest.DirectionReceived, manifest.CommitStatusAnnounced, []manifest.Commit{
		{ID: "c1"},
	})
	tport := &fakeTransport{}

	cfg := automode.Config{
		Detect: func() (avid.Snapshot, error) {
			return avid.Snapshot{State: avid.StateBusy}, nil
		},
		Store:     store,
		Transport: tport,
		Interval:  10 * time.Millisecond,
	}
	runUntil(t, cfg, 50*time.Millisecond)

	if got := tport.pulledIDs(); len(got) != 0 {
		t.Errorf("Pull nao deveria ter sido chamado em StateBusy; got %v", got)
	}
}

// StateUnknown + !ProcessRunning eh tratado como SAFE: --root pode ser
// pasta de teste sem .mdb, mas se nada esta editando, eh seguro.
func TestAutoModeDisparaEmUnknownSemProcesso(t *testing.T) {
	store := newFakeStore()
	store.set(manifest.DirectionReceived, manifest.CommitStatusAnnounced, []manifest.Commit{
		{ID: "c1"},
	})
	tport := &fakeTransport{}

	cfg := automode.Config{
		Detect: func() (avid.Snapshot, error) {
			return avid.Snapshot{State: avid.StateUnknown, ProcessRunning: false}, nil
		},
		Store:     store,
		Transport: tport,
		Interval:  10 * time.Millisecond,
	}
	runUntil(t, cfg, 50*time.Millisecond)

	if got := tport.pulledIDs(); len(got) == 0 {
		t.Errorf("Pull deveria ter sido chamado em Unknown sem processo Avid")
	}
}

// StateUnknown + ProcessRunning bloqueia: Avid pode estar editando em
// outro --root, melhor nao mexer.
func TestAutoModeNaoDisparaEmUnknownComProcesso(t *testing.T) {
	store := newFakeStore()
	store.set(manifest.DirectionReceived, manifest.CommitStatusAnnounced, []manifest.Commit{
		{ID: "c1"},
	})
	tport := &fakeTransport{}

	cfg := automode.Config{
		Detect: func() (avid.Snapshot, error) {
			return avid.Snapshot{State: avid.StateUnknown, ProcessRunning: true}, nil
		},
		Store:     store,
		Transport: tport,
		Interval:  10 * time.Millisecond,
	}
	runUntil(t, cfg, 50*time.Millisecond)

	if got := tport.pulledIDs(); len(got) != 0 {
		t.Errorf("Pull nao deveria disparar em Unknown com processo Avid rodando; got %v", got)
	}
}

func TestAutoModeNaoDisparaEmRecentlyClosed(t *testing.T) {
	store := newFakeStore()
	store.set(manifest.DirectionReceived, manifest.CommitStatusAnnounced, []manifest.Commit{
		{ID: "c1"},
	})
	tport := &fakeTransport{}

	cfg := automode.Config{
		Detect: func() (avid.Snapshot, error) {
			return avid.Snapshot{State: avid.StateRecentlyClosed}, nil
		},
		Store:     store,
		Transport: tport,
		Interval:  10 * time.Millisecond,
	}
	runUntil(t, cfg, 50*time.Millisecond)

	if got := tport.pulledIDs(); len(got) != 0 {
		t.Errorf("Pull nao deveria disparar em RecentlyClosed; got %v", got)
	}
}

func TestAutoModeNaoDisparaSeNadaAnnounced(t *testing.T) {
	store := newFakeStore() // vazio
	tport := &fakeTransport{}

	cfg := automode.Config{
		Detect: func() (avid.Snapshot, error) {
			return avid.Snapshot{State: avid.StateIdle}, nil
		},
		Store:     store,
		Transport: tport,
		Interval:  10 * time.Millisecond,
	}
	runUntil(t, cfg, 50*time.Millisecond)

	if got := tport.pulledIDs(); len(got) != 0 {
		t.Errorf("Pull nao deveria ter sido chamado sem commits announced; got %v", got)
	}
}

func TestAutoModeRespeitaCancel(t *testing.T) {
	store := newFakeStore()
	store.set(manifest.DirectionReceived, manifest.CommitStatusAnnounced, []manifest.Commit{
		{ID: "c1"},
	})
	tport := &fakeTransport{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancela ANTES do Run rodar

	cfg := automode.Config{
		Detect: func() (avid.Snapshot, error) {
			return avid.Snapshot{State: avid.StateIdle}, nil
		},
		Store:     store,
		Transport: tport,
		Interval:  10 * time.Millisecond,
	}
	err := automode.Run(ctx, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run deveria retornar context.Canceled; got %v", err)
	}
}

// Se Detect falha, o loop não trava — continua tickando até cancel.
func TestAutoModeContinuaApesDetectFail(t *testing.T) {
	store := newFakeStore()
	store.set(manifest.DirectionReceived, manifest.CommitStatusAnnounced, []manifest.Commit{
		{ID: "c1"},
	})
	tport := &fakeTransport{}

	var detectCalls int32
	var mu sync.Mutex
	cfg := automode.Config{
		Detect: func() (avid.Snapshot, error) {
			mu.Lock()
			detectCalls++
			n := detectCalls
			mu.Unlock()
			if n < 3 {
				return avid.Snapshot{}, errors.New("simulated detect fail")
			}
			return avid.Snapshot{State: avid.StateIdle}, nil
		},
		Store:     store,
		Transport: tport,
		Interval:  10 * time.Millisecond,
	}
	runUntil(t, cfg, 80*time.Millisecond)

	// Eventualmente, Detect retorna idle e Pull dispara.
	if got := tport.pulledIDs(); len(got) == 0 {
		t.Errorf("Pull deveria ter sido chamado apos detect-fail transitorio")
	}
}
