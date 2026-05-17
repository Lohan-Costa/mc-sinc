package manifest

import (
	"errors"
	"testing"
	"time"
)

func sampleCommit(id string, dir Direction) Commit {
	return Commit{
		ID:        id,
		Author:    "alice",
		Message:   "scenes 1-3",
		CreatedAt: time.Unix(1_700_000_000, 0),
		Direction: dir,
		PeerAddr:  "10.0.0.5:7777",
		Status:    CommitStatusAnnounced,
		Files: []CommitFile{
			{Path: "1/a.mxf", Hash: "aaaa1111", Size: 1024},
			{Path: "1/b.mxf", Hash: "bbbb2222", Size: 2048},
		},
	}
}

func TestSaveAndGetCommit(t *testing.T) {
	store := openTestStore(t)
	c := sampleCommit("abc123", DirectionReceived)

	if err := store.SaveCommit(c); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.GetCommit("abc123")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != c.ID || got.Author != c.Author || got.Message != c.Message {
		t.Errorf("commit mismatch: %+v vs %+v", got, c)
	}
	if got.Direction != DirectionReceived {
		t.Errorf("direction: got %q want %q", got.Direction, DirectionReceived)
	}
	if got.Status != CommitStatusAnnounced {
		t.Errorf("status: got %q", got.Status)
	}
	if len(got.Files) != 2 {
		t.Fatalf("files: got %d want 2", len(got.Files))
	}
	if got.Files[0].Path != "1/a.mxf" || got.Files[1].Hash != "bbbb2222" {
		t.Errorf("files content: %+v", got.Files)
	}
}

func TestGetCommit_NotFound(t *testing.T) {
	store := openTestStore(t)
	_, err := store.GetCommit("missing")
	if !errors.Is(err, ErrCommitNotFound) {
		t.Errorf("got %v, want ErrCommitNotFound", err)
	}
}

func TestSaveCommit_RewritesFiles(t *testing.T) {
	store := openTestStore(t)
	c := sampleCommit("rewrite", DirectionSent)

	if err := store.SaveCommit(c); err != nil {
		t.Fatal(err)
	}
	// Resalvar com lista menor
	c.Files = []CommitFile{{Path: "1/c.mxf", Hash: "cccc", Size: 99}}
	if err := store.SaveCommit(c); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCommit("rewrite")
	if len(got.Files) != 1 || got.Files[0].Path != "1/c.mxf" {
		t.Errorf("expected single file 1/c.mxf, got %+v", got.Files)
	}
}

func TestListCommits_FiltersByDirectionAndStatus(t *testing.T) {
	store := openTestStore(t)

	a := sampleCommit("a", DirectionSent)
	b := sampleCommit("b", DirectionReceived)
	c := sampleCommit("c", DirectionReceived)
	c.Status = CommitStatusPulled

	for _, x := range []Commit{a, b, c} {
		if err := store.SaveCommit(x); err != nil {
			t.Fatal(err)
		}
	}

	sent, err := store.ListCommits(DirectionSent, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].ID != "a" {
		t.Errorf("sent: %+v", sent)
	}

	allRecv, _ := store.ListCommits(DirectionReceived, "")
	if len(allRecv) != 2 {
		t.Errorf("all received: %d", len(allRecv))
	}

	pulled, _ := store.ListCommits(DirectionReceived, CommitStatusPulled)
	if len(pulled) != 1 || pulled[0].ID != "c" {
		t.Errorf("pulled: %+v", pulled)
	}
}

func TestUpdateCommitStatus(t *testing.T) {
	store := openTestStore(t)
	c := sampleCommit("xyz", DirectionReceived)
	if err := store.SaveCommit(c); err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateCommitStatus("xyz", CommitStatusPulling); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCommit("xyz")
	if got.Status != CommitStatusPulling {
		t.Errorf("status: %q", got.Status)
	}

	if err := store.UpdateCommitStatus("ghost", CommitStatusPulled); !errors.Is(err, ErrCommitNotFound) {
		t.Errorf("expected ErrCommitNotFound, got %v", err)
	}
}
