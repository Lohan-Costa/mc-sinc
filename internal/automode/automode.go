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
	"fmt"
	"log/slog"
	"time"

	"github.com/Lohan-Costa/mc-sinc/internal/avid"
	"github.com/Lohan-Costa/mc-sinc/internal/commit"
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
	SaveCommit(c manifest.Commit) error
}

// Transport é a fatia de transport.Transport que o automode usa.
type Transport interface {
	Pull(ctx context.Context, commitID string) error
	Send(ctx context.Context, c *commit.Commit) error
}

// Committer cria um commit a partir dos arquivos hashados no manifest.
// Em produção é satisfeito por *commit.Service.
type Committer interface {
	Commit(ctx context.Context, msg string) (*commit.Commit, error)
}

// Config agrupa as deps do Run.
type Config struct {
	Detect    DetectFunc
	Store     Store
	Transport Transport
	Commits   Committer // necessário se AutoCommit=true
	Interval  time.Duration

	// AutoPull: se true, baixa commits recebidos com status announced.
	// Default conservador é false; main.go passa true por padrão.
	AutoPull bool

	// AutoCommit: se true, quando Avid idle e .mdb mudou desde último
	// sent, automode commita + envia automaticamente.
	AutoCommit bool

	// AutoCommitMsg: gerador da mensagem default do auto-commit. Se nil,
	// usa "auto: YYYY-MM-DD HH:MM".
	AutoCommitMsg func() string
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

	// Auto-commit ANTES do auto-pull: se há mudança local, anuncia primeiro
	// pros peers; em seguida (no mesmo tick), baixa o que chegou de outros.
	if cfg.AutoCommit {
		autoCommit(ctx, cfg, snap)
	}

	if !cfg.AutoPull {
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

// autoCommit dispara commit + send se houver mudança local desde o último
// sent. Detecção de mudança via mtime do msmMMOB.mdb (Avid atualiza esse
// arquivo toda vez que mexe na pasta): se LastMDBChange > último
// sent.CreatedAt, há diff. Primeira execução (zero sent) também dispara,
// desde que haja .mdb conhecido OU files com hash.
//
// Quando não há .mdb (StateUnknown puro), não temos sinal confiável de
// mudança — pulamos auto-commit (auto-pull continua). Usuário pode
// commitar manualmente via UI.
func autoCommit(ctx context.Context, cfg Config, snap avid.Snapshot) {
	if cfg.Commits == nil || cfg.Store == nil || cfg.Transport == nil {
		return
	}

	sent, err := cfg.Store.ListCommits(manifest.DirectionSent, "")
	if err != nil {
		slog.WarnContext(ctx, "ListCommits sent falhou no auto-commit",
			slog.String("module", logModule),
			slog.String("event_id", "AUTOCOMMIT_LIST_FAIL"),
			slog.String("error", err.Error()))
		return
	}

	// Sem .mdb conhecido: só dispara se nunca commitou nada (primeira
	// instalação). Sem essa guarda, root sem .mdb commitaria a cada tick.
	if snap.LastMDBChange.IsZero() {
		if len(sent) > 0 {
			return
		}
	} else if len(sent) > 0 && !snap.LastMDBChange.After(sent[0].CreatedAt) {
		// .mdb existe mas não mudou desde o último envio: nada a fazer.
		return
	}

	msg := defaultAutoCommitMsg
	if cfg.AutoCommitMsg != nil {
		msg = cfg.AutoCommitMsg
	}

	c, err := cfg.Commits.Commit(ctx, msg())
	if err != nil {
		slog.WarnContext(ctx, "auto-commit falhou",
			slog.String("module", logModule),
			slog.String("event_id", "AUTOCOMMIT_FAIL"),
			slog.String("error", err.Error()))
		return
	}
	if len(c.Files) == 0 {
		// Não há nada hashado ainda. Hasher pega no próximo tick.
		return
	}

	// Persiste como sent (espelha o que api.handleCommit faz).
	mfiles := make([]manifest.CommitFile, 0, len(c.Files))
	for _, f := range c.Files {
		mfiles = append(mfiles, manifest.CommitFile{Path: f.Path, Hash: f.Hash, Size: f.Size})
	}
	if err := cfg.Store.SaveCommit(manifest.Commit{
		ID:        c.ID,
		Author:    c.Author,
		Message:   c.Message,
		CreatedAt: c.CreatedAt,
		Direction: manifest.DirectionSent,
		Status:    manifest.CommitStatusAnnounced,
		Files:     mfiles,
	}); err != nil {
		slog.WarnContext(ctx, "auto-commit persist falhou",
			slog.String("module", logModule),
			slog.String("event_id", "AUTOCOMMIT_PERSIST_FAIL"),
			slog.String("commit_id", c.ID),
			slog.String("error", err.Error()))
		return
	}

	slog.InfoContext(ctx, "auto-commit disparado",
		slog.String("module", logModule),
		slog.String("event_id", "AUTOCOMMIT_DISPATCH"),
		slog.String("commit_id", c.ID),
		slog.Int("files", len(c.Files)),
		slog.String("message", c.Message))

	if err := cfg.Transport.Send(ctx, c); err != nil {
		slog.WarnContext(ctx, "auto-commit send falhou",
			slog.String("module", logModule),
			slog.String("event_id", "AUTOCOMMIT_SEND_FAIL"),
			slog.String("commit_id", c.ID),
			slog.String("error", err.Error()))
	}
}

// defaultAutoCommitMsg gera a mensagem default ("auto: YYYY-MM-DD HH:MM").
func defaultAutoCommitMsg() string {
	return fmt.Sprintf("auto: %s", time.Now().Format("2006-01-02 15:04"))
}
