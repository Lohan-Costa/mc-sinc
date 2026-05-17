// Package avid detecta o estado do Avid Media Composer no host atual,
// combinando dois sinais:
//
//  1. Processo Avid está rodando? (heurística direta via `pgrep` em Unix,
//     `tasklist` no Windows).
//  2. Mtime do arquivo `msmMMOB.mdb` mais recente sob a raiz MXF — o Avid
//     atualiza esse arquivo logo após qualquer escrita de mídia.
//
// O resultado é um Snapshot com um campo State enum, consumido hoje pelo
// endpoint /status (puramente informativo). Quando, no futuro, MC Sinc
// ganhar modo "auto-pull / auto-commit", essa primitiva é o gate: só roda
// automaticamente quando State == StateIdle (Avid fechado há tempo bom).
//
// Esta versão NÃO bloqueia nenhuma operação manual — commit e pull seguem
// disponíveis independentemente do State. A intenção é instrumentar antes
// de restringir.
package avid

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State é o estado consolidado do Avid no host.
type State string

const (
	// StateBusy: Avid rodando E .mdb mexido nos últimos BusyWindow segundos.
	// Provavelmente capturando, importando ou exportando agora.
	StateBusy State = "busy"

	// StateOpenIdle: processo Avid rodando, mas .mdb quieto.
	// Projeto aberto sem escrita ativa.
	StateOpenIdle State = "open_idle"

	// StateRecentlyClosed: processo Avid não está rodando, mas o último .mdb
	// foi mexido dentro de RecentWindow. Risco do usuário ter fechado e
	// estar prestes a reabrir.
	StateRecentlyClosed State = "recently_closed"

	// StateIdle: processo Avid fora E .mdb quieto há mais de RecentWindow.
	// Janela segura pra operações automáticas, quando existirem.
	StateIdle State = "idle"

	// StateUnknown: não foi possível determinar (nenhum .mdb encontrado, etc).
	StateUnknown State = "unknown"
)

// Default windows (configuráveis via Detect).
const (
	DefaultBusyWindow   = 10 * time.Second
	DefaultRecentWindow = 5 * time.Minute

	// DefaultProcessName é o basename do binário principal do editor —
	// um único token, sem espaços, sem extensão. No bundle macOS está em
	// AvidMediaComposer.app/Contents/MacOS/AvidMediaComposer; no Windows
	// o executável é AvidMediaComposer.exe.
	//
	// Usar esse nome (e match exato no COMM no Unix, substring no Windows)
	// evita match indesejado em auxiliares que vivem no mesmo bundle
	// (avid-api-gateway, AvidBinIndexer, etc.).
	DefaultProcessName = "AvidMediaComposer"

	mdbFileName = "msmMMOB.mdb"
)

// Snapshot é o resultado de uma detecção.
type Snapshot struct {
	State          State     `json:"state"`
	ProcessRunning bool      `json:"process_running"`
	LastMDBChange  time.Time `json:"last_mdb_change,omitempty"`
	LastMDBPath    string    `json:"last_mdb_path,omitempty"` // path relativo à raiz MXF; útil pra debug
}

// Config agrupa as deps de Detect, permitindo override em testes
// (procRunning) e tuning de janelas.
type Config struct {
	Root         string
	ProcessName  string
	BusyWindow   time.Duration
	RecentWindow time.Duration

	// ProcRunning é injetado para testes; em produção usa isProcessRunning
	// (definido em process_unix.go / process_windows.go).
	ProcRunning func(name string) (bool, error)
}

// Detect lê o estado atual do Avid combinando processo + mtime do .mdb.
func Detect(cfg Config) (Snapshot, error) {
	if cfg.BusyWindow == 0 {
		cfg.BusyWindow = DefaultBusyWindow
	}
	if cfg.RecentWindow == 0 {
		cfg.RecentWindow = DefaultRecentWindow
	}
	if cfg.ProcessName == "" {
		cfg.ProcessName = DefaultProcessName
	}
	procCheck := cfg.ProcRunning
	if procCheck == nil {
		procCheck = isProcessRunning
	}

	running, procErr := procCheck(cfg.ProcessName)
	// procErr é não-fatal: degrada graciosamente pro modo só-mtime.

	mtime, mdbPath, mdbErr := mostRecentMDB(cfg.Root)

	snap := Snapshot{ProcessRunning: running}

	if mdbErr != nil {
		// Sem .mdb encontrado: estado inferível só pelo processo.
		// Mantemos snap parcial e devolvemos o erro pra que o caller
		// possa logar (caso seja problema de configuração, ex.: root errado).
		if running {
			snap.State = StateOpenIdle
		} else {
			snap.State = StateUnknown
		}
		if procErr != nil {
			return snap, fmt.Errorf("detect: process=%v mdb=%v", procErr, mdbErr)
		}
		return snap, mdbErr
	}

	snap.LastMDBChange = mtime
	snap.LastMDBPath = mdbPath
	since := time.Since(mtime)

	switch {
	case running && since < cfg.BusyWindow:
		snap.State = StateBusy
	case running:
		snap.State = StateOpenIdle
	case !running && since < cfg.RecentWindow:
		snap.State = StateRecentlyClosed
	default:
		snap.State = StateIdle
	}
	return snap, procErr
}

// mostRecentMDB varre MXF/*/msmMMOB.mdb (profundidade 1) e devolve o mtime
// mais recente. Retorna erro se não achou nenhum .mdb.
func mostRecentMDB(root string) (time.Time, string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("read root %s: %w", root, err)
	}

	var (
		bestTime time.Time
		bestRel  string
		found    bool
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(root, e.Name(), mdbFileName)
		info, err := os.Stat(candidate)
		if err != nil {
			continue // pasta sem .mdb ainda — Avid não escreveu lá
		}
		if !found || info.ModTime().After(bestTime) {
			bestTime = info.ModTime()
			bestRel = filepath.Join(e.Name(), mdbFileName)
			found = true
		}
	}
	if !found {
		return time.Time{}, "", fmt.Errorf("no %s found under %s", mdbFileName, root)
	}
	return bestTime, bestRel, nil
}
