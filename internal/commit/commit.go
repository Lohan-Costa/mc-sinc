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
	"strings"
	"time"

	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
)

// FileSpec descreve um arquivo do commit com a metadata mínima
// necessária pro receiver decidir pull e validar integridade depois.
//
// Path é SEMPRE em forward slash ("/"), independente do OS do sender —
// é um path de protocolo, não de filesystem. Senders em Windows
// normalizam via filepath.ToSlash() antes de expor o FileSpec.
// Receivers usam path.Base/path.Dir (não path/filepath) ao consumir
// e filepath.Join localmente pra montar caminhos de I/O.
type FileSpec struct {
	Path string `json:"path"` // caminho relativo à pasta MXF do sender, sempre forward slash
	Hash string `json:"hash"` // xxhash64 hex, 16 chars
	Size int64  `json:"size"` // bytes
}

// Commit é um snapshot anunciado: um conjunto de arquivos staged que viram
// uma unidade lógica de sincronização.
type Commit struct {
	ID        string     `json:"id"`         // identificador opaco, 16 bytes hex
	Author    string     `json:"author"`     // identificador do editor que fez o commit
	Message   string     `json:"message"`    // mensagem livre — "scenes 12-14 sound design"
	Files     []FileSpec `json:"files"`      // paths + hash + size, relativos à pasta MXF do sender
	CreatedAt time.Time  `json:"created_at"`
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

// Commit consome todos os arquivos com hash calculado (em status `discovered`
// ou `staged`) e os marca como `committed`, devolvendo o Commit resultante.
// Não envia nada pela rede — isso é tarefa do transport.
//
// Pasta é a unidade de envio. O Avid mantém .mdb/.pmr indexando todos os
// .mxf da pasta numérica — enviar parcial criaria índice apontando pra
// arquivos que não viajaram. Por isso pegamos tudo que tem hash.
//
// Arquivos sem hash são pulados (entram no próximo commit quando o hasher
// processar).
func (s *Service) Commit(ctx context.Context, message string) (*Commit, error) {
	discovered, err := s.store.ByStatus(manifest.StatusDiscovered)
	if err != nil {
		return nil, err
	}
	staged, err := s.store.ByStatus(manifest.StatusStaged)
	if err != nil {
		return nil, err
	}
	candidates := append(discovered, staged...)

	files := make([]FileSpec, 0, len(candidates))
	for _, f := range candidates {
		if f.Hash == "" {
			continue
		}
		if err := s.store.SetStatus(f.Path, manifest.StatusCommitted); err != nil {
			return nil, err
		}
		// Path no FileSpec é sempre forward slash — ver doc de FileSpec.
		// strings.ReplaceAll em vez de filepath.ToSlash porque este último
		// é runtime-dependent: no Unix, "\" não é Separator e ToSlash não
		// converteria. Aqui queremos converter incondicionalmente.
		files = append(files, FileSpec{Path: strings.ReplaceAll(f.Path, `\`, "/"), Hash: f.Hash, Size: f.Size})
	}

	return &Commit{
		ID:        newID(),
		Author:    s.user,
		Message:   message,
		Files:     files,
		CreatedAt: time.Now(),
	}, nil
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
