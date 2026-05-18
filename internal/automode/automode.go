// Package automode orquestra pulls automáticos quando o Avid está idle.
//
// O Run loop chama o detector do Avid em intervalo fixo; quando o State
// é StateIdle (processo fechado E .mdb parado há ≥ RecentWindow), lista
// commits received com status announced e dispara Pull serial pra cada um.
//
// Coexistência com pull manual via UI é segura: o status `pulling` no
// manifest é atualizado antes do fetch (em transport.Pull), funcionando
// como mutex implícito — se o usuário clica Baixar enquanto o automode
// tenta o mesmo commit, o segundo a chegar vai operar sobre um status
// que já não é `announced` e falha limpo.
package automode

import (
	"context"
	"log/slog"
	"time"

	"github.com/Lohan-Costa/mc-sinc/internal/avid"
	"github.com/Lohan-Costa/mc-sinc/internal/manifest"
)

const (
	logModule = "automode"

	// DefaultInterval entre detecções. avid.Detect é barato (pgrep/tasklist
	// + ReadDir + Stat); 30s é responsivo sem custo perceptível.
	DefaultInterval = 30 * time.Second
)

// DetectFunc é a forma do detector do Avid. Tipada como função (em vez de
// interface) pra simplificar a injeção em testes — `avid.Detect` direto
// não é uma interface, então o caller embrulha numa closure.
type DetectFunc func() (avid.Snapshot, error)

// Store é a fatia de manifest.Store que o automode usa. Interface mínima
// pra permitir fake em testes.
type Store interface {
	ListCommits(dir manifest.Direction, status manifest.CommitStatus) ([]manifest.Commit, error)
}

// Transport é a fatia de transport.Transport que o automode usa.
type Transport interface {
	Pull(ctx context.Context, commitID string) error
}

// Config agrupa as deps do Run.
type Config struct {
	Detect    DetectFunc
	Store     Store
	Transport Transport
	Interval  time.Duration // se zero, usa DefaultInterval
}

// Run loop principal. Bloqueia até ctx cancelar. Erros parciais (Detect
// falha, Pull falha) só são logados; não param o loop.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Interval == 0 {
		cfg.Interval = DefaultInterval
	}
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	slog.InfoContext(ctx, "automode iniciado",
		slog.String("module", logModule),
		slog.String("event_id", "AUTOMODE_START"),
		slog.Duration("interval", cfg.Interval))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			tick(ctx, cfg)
		}
	}
}

// isSafeForAuto decide se o estado atual permite operações automáticas.
//
//   - StateIdle: Avid fechado E .mdb parado >= RecentWindow. Caso canônico.
//   - StateUnknown + !ProcessRunning: nenhum .mdb encontrado no --root
//     (pasta de teste, pasta sem mídia Avid, etc) E o processo do Avid
//     não está rodando em lugar nenhum. Como não há .mdb pra ser
//     corrompido e nada está editando, é seguro.
//
// StateUnknown + ProcessRunning: Avid pode estar editando outra raiz —
// seguro pra ESTA raiz, mas conservador: bloqueia.
func isSafeForAuto(s avid.Snapshot) bool {
	if s.State == avid.StateIdle {
		return true
	}
	if s.State == avid.StateUnknown && !s.ProcessRunning {
		return true
	}
	return false
}

func tick(ctx context.Context, cfg Config) {
	snap, err := cfg.Detect()
	if err != nil {
		slog.WarnContext(ctx, "avid.Detect falhou no tick do automode",
			slog.String("module", logModule),
			slog.String("event_id", "AUTOMODE_DETECT_FAIL"),
			slog.String("error", err.Error()))
		return
	}
	if !isSafeForAuto(snap) {
		// Estado não é seguro pra auto-mode. Debug-level pra não poluir
		// info em tick periódico, mas observável quando precisa investigar
		// "por que auto-pull não dispara".
		slog.DebugContext(ctx, "automode skip — estado nao eh seguro",
			slog.String("module", logModule),
			slog.String("event_id", "AUTOMODE_SKIP"),
			slog.String("state", string(snap.State)),
			slog.Bool("process_running", snap.ProcessRunning))
		return
	}

	pending, err := cfg.Store.ListCommits(manifest.DirectionReceived, manifest.CommitStatusAnnounced)
	if err != nil {
		slog.WarnContext(ctx, "ListCommits falhou no automode",
			slog.String("module", logModule),
			slog.String("event_id", "AUTOMODE_LIST_FAIL"),
			slog.String("error", err.Error()))
		return
	}
	if len(pending) == 0 {
		return
	}

	ids := make([]string, 0, len(pending))
	for _, c := range pending {
		ids = append(ids, c.ID)
	}
	slog.InfoContext(ctx, "automode disparando pull em batch",
		slog.String("module", logModule),
		slog.String("event_id", "AUTOMODE_PULL_BATCH"),
		slog.Int("count", len(pending)),
		slog.Any("commit_ids", ids))

	for _, c := range pending {
		if ctx.Err() != nil {
			return
		}
		if err := cfg.Transport.Pull(ctx, c.ID); err != nil {
			slog.WarnContext(ctx, "auto-pull falhou pra commit",
				slog.String("module", logModule),
				slog.String("event_id", "AUTOMODE_PULL_FAIL"),
				slog.String("commit_id", c.ID),
				slog.String("error", err.Error()))
			// Status já vira `failed` dentro de transport.Pull; seguimos.
		}
	}
}
