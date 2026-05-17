package avid

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeMDB cria MXF/<sub>/msmMMOB.mdb com mtime dado.
func writeMDB(t *testing.T, root, sub string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(root, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, mdbFileName)
	if err := os.WriteFile(path, []byte("fake mdb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func stubProc(running bool, err error) func(string) (bool, error) {
	return func(string) (bool, error) { return running, err }
}

func TestDetect_Busy(t *testing.T) {
	root := t.TempDir()
	writeMDB(t, root, "1", time.Now().Add(-2*time.Second))

	snap, err := Detect(Config{
		Root:        root,
		ProcRunning: stubProc(true, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != StateBusy {
		t.Errorf("state: got %q want %q", snap.State, StateBusy)
	}
	if !snap.ProcessRunning {
		t.Error("ProcessRunning deve ser true")
	}
}

func TestDetect_OpenIdle(t *testing.T) {
	root := t.TempDir()
	writeMDB(t, root, "1", time.Now().Add(-1*time.Minute))

	snap, _ := Detect(Config{Root: root, ProcRunning: stubProc(true, nil)})
	if snap.State != StateOpenIdle {
		t.Errorf("state: got %q want %q", snap.State, StateOpenIdle)
	}
}

func TestDetect_RecentlyClosed(t *testing.T) {
	root := t.TempDir()
	writeMDB(t, root, "1", time.Now().Add(-1*time.Minute))

	snap, _ := Detect(Config{Root: root, ProcRunning: stubProc(false, nil)})
	if snap.State != StateRecentlyClosed {
		t.Errorf("state: got %q want %q", snap.State, StateRecentlyClosed)
	}
}

func TestDetect_Idle(t *testing.T) {
	root := t.TempDir()
	writeMDB(t, root, "1", time.Now().Add(-1*time.Hour))

	snap, _ := Detect(Config{Root: root, ProcRunning: stubProc(false, nil)})
	if snap.State != StateIdle {
		t.Errorf("state: got %q want %q", snap.State, StateIdle)
	}
}

func TestDetect_NoMDB_ProcessRunning(t *testing.T) {
	root := t.TempDir()
	// Sem nenhum .mdb. Avid pode estar aberto com projeto sem mídia ainda.
	snap, _ := Detect(Config{Root: root, ProcRunning: stubProc(true, nil)})
	if snap.State != StateOpenIdle {
		t.Errorf("state: got %q want %q", snap.State, StateOpenIdle)
	}
}

func TestDetect_NoMDB_ProcessNotRunning(t *testing.T) {
	root := t.TempDir()
	snap, _ := Detect(Config{Root: root, ProcRunning: stubProc(false, nil)})
	if snap.State != StateUnknown {
		t.Errorf("state: got %q want %q", snap.State, StateUnknown)
	}
}

func TestDetect_PicksMostRecentMDB(t *testing.T) {
	root := t.TempDir()
	writeMDB(t, root, "1", time.Now().Add(-2*time.Hour))
	writeMDB(t, root, "2", time.Now().Add(-3*time.Second)) // mais recente
	writeMDB(t, root, "1000", time.Now().Add(-30*time.Minute))

	snap, _ := Detect(Config{Root: root, ProcRunning: stubProc(true, nil)})
	if snap.State != StateBusy {
		t.Errorf("state: got %q (esperado busy pelo mtime mais recente)", snap.State)
	}
	if filepath.ToSlash(snap.LastMDBPath) != "2/"+mdbFileName {
		t.Errorf("LastMDBPath: got %q", snap.LastMDBPath)
	}
}

func TestDetect_IgnoresFilesAtRoot(t *testing.T) {
	root := t.TempDir()
	// Arquivo na raiz, não numa subpasta — deve ser ignorado.
	if err := os.WriteFile(filepath.Join(root, mdbFileName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, _ := Detect(Config{Root: root, ProcRunning: stubProc(false, nil)})
	if snap.State != StateUnknown {
		t.Errorf("state: got %q want unknown (mdb na raiz não conta)", snap.State)
	}
}

func TestDetect_RootDoesNotExist(t *testing.T) {
	snap, err := Detect(Config{
		Root:        "/tmp/this-dir-should-not-exist-xyzzy",
		ProcRunning: stubProc(false, nil),
	})
	if err == nil {
		t.Error("esperado erro ao não conseguir ler root")
	}
	if snap.State != StateUnknown {
		t.Errorf("state: got %q want unknown", snap.State)
	}
}

func TestDetect_CustomWindows(t *testing.T) {
	root := t.TempDir()
	writeMDB(t, root, "1", time.Now().Add(-7*time.Second))

	// BusyWindow=15s, então 7s atrás = busy ainda.
	snap, _ := Detect(Config{
		Root:        root,
		BusyWindow:  15 * time.Second,
		ProcRunning: stubProc(true, nil),
	})
	if snap.State != StateBusy {
		t.Errorf("state: got %q want busy com BusyWindow=15s", snap.State)
	}

	// BusyWindow=2s, então 7s atrás = open_idle.
	snap2, _ := Detect(Config{
		Root:        root,
		BusyWindow:  2 * time.Second,
		ProcRunning: stubProc(true, nil),
	})
	if snap2.State != StateOpenIdle {
		t.Errorf("state com BusyWindow=2s: got %q want open_idle", snap2.State)
	}
}
