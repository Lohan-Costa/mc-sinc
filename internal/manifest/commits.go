package manifest

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Direction indica se um commit foi originado localmente ou recebido de um peer.
type Direction string

const (
	DirectionSent     Direction = "sent"     // commit feito pelo usuário local, anunciado aos peers
	DirectionReceived Direction = "received" // commit anunciado por um peer, aguardando decisão
)

// CommitStatus é o estado de um commit no manifest.
type CommitStatus string

const (
	CommitStatusAnnounced CommitStatus = "announced" // anunciado mas ainda não materializado
	CommitStatusPulling   CommitStatus = "pulling"   // pull em andamento
	CommitStatusPulled    CommitStatus = "pulled"    // arquivos baixados e validados
	CommitStatusFailed    CommitStatus = "failed"    // pull falhou (hash, rede, etc.)
)

// Commit representa uma linha da tabela `commits`.
type Commit struct {
	ID        string       `json:"id"`
	Author    string       `json:"author"`
	Message   string       `json:"message"`
	CreatedAt time.Time    `json:"created_at"`
	Direction Direction    `json:"direction"`
	PeerAddr  string       `json:"peer_addr"` // só relevante para Direction=received; host:port do sender
	Status    CommitStatus `json:"status"`
	Files     []CommitFile `json:"files"` // populado por GetCommit / ListCommits
}

// CommitFile é uma linha de `commit_files`.
type CommitFile struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

// ErrCommitNotFound é retornado quando GetCommit não encontra o id pedido.
var ErrCommitNotFound = errors.New("commit not found")

// SaveCommit persiste um Commit + seus arquivos numa transação.
// Reescreve completamente o conjunto de commit_files se o commit já existir.
func (s *Store) SaveCommit(c Commit) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`
		INSERT INTO commits (id, author, message, created_at, direction, peer_addr, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			author     = excluded.author,
			message    = excluded.message,
			created_at = excluded.created_at,
			direction  = excluded.direction,
			peer_addr  = excluded.peer_addr,
			status     = excluded.status
	`, c.ID, c.Author, c.Message, c.CreatedAt.Unix(), string(c.Direction), c.PeerAddr, string(c.Status)); err != nil {
		return fmt.Errorf("upsert commit: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM commit_files WHERE commit_id = ?`, c.ID); err != nil {
		return fmt.Errorf("delete old files: %w", err)
	}
	for _, f := range c.Files {
		if _, err := tx.Exec(`
			INSERT INTO commit_files (commit_id, path, hash, size)
			VALUES (?, ?, ?, ?)
		`, c.ID, f.Path, f.Hash, f.Size); err != nil {
			return fmt.Errorf("insert commit_file %s: %w", f.Path, err)
		}
	}

	return tx.Commit()
}

// GetCommit busca um commit pelo id, populando seus arquivos.
func (s *Store) GetCommit(id string) (Commit, error) {
	var (
		c         Commit
		createdAt int64
		direction string
		status    string
	)
	err := s.db.QueryRow(`
		SELECT id, author, message, created_at, direction, peer_addr, status
		FROM commits WHERE id = ?
	`, id).Scan(&c.ID, &c.Author, &c.Message, &createdAt, &direction, &c.PeerAddr, &status)
	if err == sql.ErrNoRows {
		return Commit{}, ErrCommitNotFound
	}
	if err != nil {
		return Commit{}, err
	}
	c.CreatedAt = time.Unix(createdAt, 0)
	c.Direction = Direction(direction)
	c.Status = CommitStatus(status)

	files, err := s.CommitFiles(id)
	if err != nil {
		return Commit{}, err
	}
	c.Files = files
	return c, nil
}

// CommitFiles devolve os arquivos de um commit.
func (s *Store) CommitFiles(commitID string) ([]CommitFile, error) {
	rows, err := s.db.Query(`
		SELECT path, hash, size FROM commit_files WHERE commit_id = ? ORDER BY path ASC
	`, commitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommitFile
	for rows.Next() {
		var f CommitFile
		if err := rows.Scan(&f.Path, &f.Hash, &f.Size); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListCommits filtra por direção. Se status != "", filtra também por status.
// Files são populados (N+1 query) — aceitável porque N tende a ser pequeno
// nesta ferramenta (dezenas, não milhares); permite ao consumer mostrar
// contagem de arquivos e size total sem segunda viagem.
func (s *Store) ListCommits(dir Direction, status CommitStatus) ([]Commit, error) {
	query := `
		SELECT id, author, message, created_at, direction, peer_addr, status
		FROM commits WHERE direction = ?
	`
	args := []any{string(dir)}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Commit
	for rows.Next() {
		var (
			c         Commit
			createdAt int64
			direction string
			status    string
		)
		if err := rows.Scan(&c.ID, &c.Author, &c.Message, &createdAt, &direction, &c.PeerAddr, &status); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(createdAt, 0)
		c.Direction = Direction(direction)
		c.Status = CommitStatus(status)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		files, err := s.CommitFiles(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Files = files
	}
	return out, nil
}

// UpdateCommitStatus muda o status de um commit (ex: announced → pulling → pulled).
func (s *Store) UpdateCommitStatus(id string, status CommitStatus) error {
	res, err := s.db.Exec(`UPDATE commits SET status = ? WHERE id = ?`, string(status), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrCommitNotFound
	}
	return nil
}
