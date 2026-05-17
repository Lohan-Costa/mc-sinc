package avid

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeVolume cria volume_root/Avid MediaFiles/MXF/<sub>/msmMMOB.mdb com
// o mtime dado. Se mtime for zero, não cria o .mdb (só a pasta MXF/<sub>).
func fakeVolume(t *testing.T, volRoot, sub string, mdbMtime time.Time) {
	t.Helper()
	subDir := filepath.Join(volRoot, avidMediaFilesDirName, "MXF", sub)
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !mdbMtime.IsZero() {
		mdb := filepath.Join(subDir, mdbFileName)
		if err := os.WriteFile(mdb, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(mdb, mdbMtime, mdbMtime); err != nil {
			t.Fatal(err)
		}
	}
}

func stubVolumes(paths ...string) func() ([]string, error) {
	return func() ([]string, error) { return paths, nil }
}

func TestDiscoverRoots_PicksMostRecent(t *testing.T) {
	volA := t.TempDir()
	volB := t.TempDir()

	fakeVolume(t, volA, "1", time.Now().Add(-2*time.Hour))
	fakeVolume(t, volB, "1", time.Now().Add(-5*time.Minute))

	cands, err := DiscoverRoots(Discovery{ListVolumes: stubVolumes(volA, volB)})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(cands))
	}
	if cands[0].Path != filepath.Join(volB, avidMediaFilesDirName, "MXF") {
		t.Errorf("primeiro candidato deveria ser o mais recente (volB), got %q", cands[0].Path)
	}
	if cands[1].Path != filepath.Join(volA, avidMediaFilesDirName, "MXF") {
		t.Errorf("segundo candidato deveria ser volA, got %q", cands[1].Path)
	}
}

func TestDiscoverRoots_SkipsVolumesWithoutAvidMediaFiles(t *testing.T) {
	volA := t.TempDir() // sem estrutura
	volB := t.TempDir()
	fakeVolume(t, volB, "1", time.Now())

	cands, err := DiscoverRoots(Discovery{ListVolumes: stubVolumes(volA, volB)})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate (só volB), got %d", len(cands))
	}
	if cands[0].VolumeName != filepath.Base(volB) {
		t.Errorf("VolumeName: got %q", cands[0].VolumeName)
	}
}

func TestDiscoverRoots_HandlesNoMDBYet(t *testing.T) {
	vol := t.TempDir()
	// MXF/ existe mas nenhum .mdb (mtime zero = não cria arquivo).
	fakeVolume(t, vol, "1", time.Time{})

	cands, err := DiscoverRoots(Discovery{ListVolumes: stubVolumes(vol)})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("volume sem .mdb ainda é candidato válido, got %d", len(cands))
	}
	if !cands[0].LastMDBChange.IsZero() {
		t.Errorf("LastMDBChange deve ser zero quando não há .mdb, got %v", cands[0].LastMDBChange)
	}
}

func TestDiscoverRoots_NoMDBComesLastInOrdering(t *testing.T) {
	volA := t.TempDir()
	volB := t.TempDir()

	fakeVolume(t, volA, "1", time.Time{})          // sem .mdb
	fakeVolume(t, volB, "1", time.Now().Add(-1*time.Hour))

	cands, _ := DiscoverRoots(Discovery{ListVolumes: stubVolumes(volA, volB)})
	if len(cands) != 2 {
		t.Fatalf("expected 2, got %d", len(cands))
	}
	if cands[0].VolumeName != filepath.Base(volB) {
		t.Errorf("volume com .mdb deve vir antes do sem .mdb; got primeiro %q", cands[0].VolumeName)
	}
}

func TestDiscoverRoots_NoVolumes(t *testing.T) {
	cands, err := DiscoverRoots(Discovery{ListVolumes: stubVolumes()})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Errorf("expected empty, got %d", len(cands))
	}
}

func TestDiscoverRoots_ListVolumesError(t *testing.T) {
	_, err := DiscoverRoots(Discovery{
		ListVolumes: func() ([]string, error) { return nil, os.ErrPermission },
	})
	if err == nil {
		t.Error("esperado erro propagado do ListVolumes")
	}
}

func TestDiscoverRoots_NoListVolumesInjected(t *testing.T) {
	_, err := DiscoverRoots(Discovery{})
	if err == nil {
		t.Error("esperado erro quando ListVolumes é nil")
	}
}
