// Package manifest persiste o estado dos arquivos .mxf locais num SQLite.
//
// O manifest é a "verdade local": quais arquivos existem, seus hashes,
// e em qual status estão (descoberto, em staging, committed, recebido de peer).
//
// Schema (tabela `files`):
//
//	path        TEXT PRIMARY KEY  -- caminho relativo à pasta raiz MXF
//	hash        TEXT              -- xxhash64 do conteúdo, hex
//	size        INTEGER           -- tamanho em bytes
//	modified_at INTEGER           -- unix timestamp
//	status      TEXT              -- discovered | staged | committed | received
//	updated_at  INTEGER           -- unix timestamp da última atualização do registro
package manifest

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Status representa o estado de um arquivo no manifest.
type Status string

const (
	StatusDiscovered Status = "discovered" // watcher viu o arquivo, mas o usuário ainda não decidiu o que fazer
	StatusStaged     Status = "staged"     // usuário marcou para incluir no próximo commit
	StatusCommitted  Status = "committed"  // já enviado/anunciado aos peers
	StatusReceived   Status = "received"   // veio de um peer, vive numa subpasta `1-<user>`
)

// File é a representação de uma linha da tabela `files`.
type File struct {
	Path       string
	Hash       string
	Size       int64
	ModifiedAt time.Time
	Status     Status
	UpdatedAt  time.Time
}

// Store é a camada de persistência sobre o SQLite.
type Store struct {
	db *sql.DB
}

// Open abre (ou cria) o manifest no caminho dado.
//
// DSN configurado pra coexistência saudável com handlers HTTP concorrentes:
//   - _journal=WAL:           Write-Ahead Log permite leitura paralela com
//                             escrita; sem ele, qualquer concorrência levanta
//                             SQLITE_BUSY imediatamente.
//   - _busy_timeout=5000:     se mesmo com WAL houver contenção, espera até
//                             5s antes de devolver erro — handlers paralelos
//                             se atravessam sem falhar.
//   - _foreign_keys=on:       saúde de schema (impacto zero hoje porque não
//                             declaramos FK, mas é bom hábito).
func Open(path string) (*Store, error) {
	dsn := path + "?_journal=WAL&_busy_timeout=5000&_foreign_keys=on"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close encerra a conexão.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS files (
			path        TEXT PRIMARY KEY,
			hash        TEXT NOT NULL DEFAULT '',
			size        INTEGER NOT NULL DEFAULT 0,
			modified_at INTEGER NOT NULL DEFAULT 0,
			status      TEXT NOT NULL DEFAULT 'discovered',
			updated_at  INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_files_status ON files(status);

		CREATE TABLE IF NOT EXISTS commits (
			id          TEXT PRIMARY KEY,
			author      TEXT NOT NULL,
			message     TEXT NOT NULL,
			created_at  INTEGER NOT NULL,
			direction   TEXT NOT NULL,
			peer_addr   TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'announced'
		);
		CREATE INDEX IF NOT EXISTS idx_commits_direction ON commits(direction);
		CREATE INDEX IF NOT EXISTS idx_commits_status ON commits(status);

		CREATE TABLE IF NOT EXISTS commit_files (
			commit_id TEXT NOT NULL,
			path      TEXT NOT NULL,
			hash      TEXT NOT NULL,
			size      INTEGER NOT NULL,
			PRIMARY KEY (commit_id, path)
		);
	`)
	return err
}

// Upsert insere ou atualiza um arquivo no manifest.
func (s *Store) Upsert(f File) error {
	_, err := s.db.Exec(`
		INSERT INTO files (path, hash, size, modified_at, status, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			hash        = excluded.hash,
			size        = excluded.size,
			modified_at = excluded.modified_at,
			status      = excluded.status,
			updated_at  = excluded.updated_at
	`, f.Path, f.Hash, f.Size, f.ModifiedAt.Unix(), string(f.Status), time.Now().Unix())
	return err
}

// UpsertObserved é o caminho do drainer do watcher: registra que um arquivo
// foi visto no disco, com tamanho e mtime. Preserva o hash existente quando
// o mtime é o mesmo (arquivo não mudou, hasher não precisa recomputar),
// e **invalida** o hash quando o mtime difere (arquivo foi reescrito —
// caso típico do msmMMOB.mdb que o Avid atualiza repetidamente).
//
// Status segue a regra: novos arquivos viram `discovered`; arquivos já
// existentes mantêm o status atual (não regride staged/committed pra
// discovered só porque o watcher emitiu evento de novo).
func (s *Store) UpsertObserved(path string, size int64, modifiedAt time.Time, defaultStatus Status) error {
	_, err := s.db.Exec(`
		INSERT INTO files (path, hash, size, modified_at, status, updated_at)
		VALUES (?, '', ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			size        = excluded.size,
			modified_at = excluded.modified_at,
			updated_at  = excluded.updated_at,
			hash = CASE
				WHEN files.modified_at = excluded.modified_at THEN files.hash
				ELSE ''
			END
	`, path, size, modifiedAt.Unix(), string(defaultStatus), time.Now().Unix())
	return err
}

// ByStatus lista os arquivos num determinado status.
func (s *Store) ByStatus(st Status) ([]File, error) {
	rows, err := s.db.Query(`
		SELECT path, hash, size, modified_at, status, updated_at
		FROM files
		WHERE status = ?
		ORDER BY updated_at DESC
	`, string(st))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		var (
			f          File
			modifiedAt int64
			updatedAt  int64
			status     string
		)
		if err := rows.Scan(&f.Path, &f.Hash, &f.Size, &modifiedAt, &status, &updatedAt); err != nil {
			return nil, err
		}
		f.ModifiedAt = time.Unix(modifiedAt, 0)
		f.UpdatedAt = time.Unix(updatedAt, 0)
		f.Status = Status(status)
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetStatus muda o status de um arquivo (ex: discovered → staged).
func (s *Store) SetStatus(path string, st Status) error {
	_, err := s.db.Exec(`
		UPDATE files SET status = ?, updated_at = ? WHERE path = ?
	`, string(st), time.Now().Unix(), path)
	return err
}

// SetHash grava o hash e o modified_at observado no momento do hashing.
// Não toca em status — hash é metadado paralelo ao ciclo discovered/staged/committed.
func (s *Store) SetHash(path, hash string, modifiedAt time.Time) error {
	_, err := s.db.Exec(`
		UPDATE files
		SET hash = ?, modified_at = ?, updated_at = ?
		WHERE path = ?
	`, hash, modifiedAt.Unix(), time.Now().Unix(), path)
	return err
}

// NeedsHash devolve files que ainda não têm hash calculado.
// Re-hash por mudança de mtime fica a cargo do worker (próxima iteração).
func (s *Store) NeedsHash() ([]File, error) {
	rows, err := s.db.Query(`
		SELECT path, hash, size, modified_at, status, updated_at
		FROM files
		WHERE hash = ''
		ORDER BY updated_at ASC
		LIMIT 100
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		var (
			f          File
			modifiedAt int64
			updatedAt  int64
			status     string
		)
		if err := rows.Scan(&f.Path, &f.Hash, &f.Size, &modifiedAt, &status, &updatedAt); err != nil {
			return nil, err
		}
		f.ModifiedAt = time.Unix(modifiedAt, 0)
		f.UpdatedAt = time.Unix(updatedAt, 0)
		f.Status = Status(status)
		out = append(out, f)
	}
	return out, rows.Err()
}
