// Package commit empacota a ação manual de "commit" — o usuário decide
// que um conjunto de arquivos .mxf locais está pronto para ser anunciado
// aos peers da equipe.
//
// O fluxo é deliberadamente Git-like:
//
//  1. watcher detecta novos arquivos      → status: discovered
//  2. usuário marca o que quer enviar     → status: staged
//  3. usuário aperta "commit"             → status: committed (+ Commit row)
//  4. transport anuncia o commit aos peers
package commit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
)

// Commit é um snapshot anunciado: um conjunto de arquivos staged que viram
// uma unidade lógica de sincronização.
type Commit struct {
	ID        string    // identificador opaco, 16 bytes hex
	Author    string    // identificador do editor que fez o commit
	Message   string    // mensagem livre — "scenes 12-14 sound design"
	Files     []string  // paths relativos à pasta MXF
	CreatedAt time.Time
}

// Service orquestra staging e commits sobre o manifest local.
type Service struct {
	store *manifest.Store
	user  string
}

// New cria o serviço de commit ligado a um manifest e ao usuário corrente.
func New(store *manifest.Store, user string) *Service {
	return &Service{store: store, user: user}
}

// Stage move um arquivo do status `discovered` para `staged`.
func (s *Service) Stage(ctx context.Context, path string) error {
	return s.store.SetStatus(path, manifest.StatusStaged)
}

// Unstage faz o caminho contrário, devolvendo ao status `discovered`.
func (s *Service) Unstage(ctx context.Context, path string) error {
	return s.store.SetStatus(path, manifest.StatusDiscovered)
}

// Commit consome todos os arquivos em status `staged` e os marca como `committed`,
// devolvendo o Commit resultante. Não envia nada pela rede — isso é tarefa do transport.
func (s *Service) Commit(ctx context.Context, message string) (*Commit, error) {
	staged, err := s.store.ByStatus(manifest.StatusStaged)
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(staged))
	for _, f := range staged {
		if err := s.store.SetStatus(f.Path, manifest.StatusCommitted); err != nil {
			return nil, err
		}
		paths = append(paths, f.Path)
	}

	return &Commit{
		ID:        newID(),
		Author:    s.user,
		Message:   message,
		Files:     paths,
		CreatedAt: time.Now(),
	}, nil
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
