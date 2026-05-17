# Logging

O MC Sinc usa um sistema de logging estruturado central em [`internal/logging`](../internal/logging/). Toda nova feature deve emitir logs por esse caminho — `log.Printf` direto da stdlib **não deve mais ser usado**.

## Onde os logs moram

Por padrão, em `$HOME/.mcsinc/logs/`:

- **`app.log`** — versão humanamente legível, texto puro, em **PT-BR**.
- **`app.jsonl`** — JSON Lines (uma linha por log), pronto pra parser/IA/automação futura.

Path configurável via flag `--log-dir`. Verbosidade via `--log-level` (`trace`/`debug`/`info`/`warn`/`error`; default `info`).

Rotação automática: 20 MB por arquivo, 10 backups, retenção 30 dias.

## Como emitir um log

Use `log/slog` da stdlib, com o pacote `slog` global após `logging.Init`.

```go
import "log/slog"

slog.InfoContext(ctx, "mensagem em PT-BR descrevendo o que aconteceu",
    slog.String("module", "lan"),
    slog.String("event_id", "ANNOUNCE_OK"),
    slog.String("commit_id", c.ID),
    slog.String("peer_id", peer.ID))
```

### Convenções

- **Sempre passe `ctx`** (use `slog.InfoContext` / `slog.WarnContext` etc.) — assim o `op_id` propaga automaticamente.
- **Sempre inclua `module`** — string curta (`lan`, `discovery`, `hasher`, `api`, `main`, `session`).
- **Inclua `event_id`** quando o evento for catalogável (eventos importantes). Use `SCREAMING_SNAKE_CASE`. Exemplos: `ANNOUNCE_OK`, `ANNOUNCE_FAIL`, `PEER_DISCOVERED`, `HASH_OK`, `PULL_HASH_MISMATCH`.
- **Mensagens em PT-BR**, claras, descrevendo o que aconteceu (não apenas "erro" ou "ok").
- **Sem dados sensíveis em logs**: paths absolutos do usuário são sanitizados automaticamente (`/Users/lohan/...` vira `<USER>/...`).

## Níveis

| Nível | Quando usar |
|---|---|
| `TRACE` (custom = -8) | Eventos extremamente detalhados; desabilitado em produção. |
| `DEBUG` | Detalhes técnicos úteis pra desenvolvimento. |
| `INFO` | Ações normais do sistema: peer descoberto, commit anunciado, arquivo baixado. |
| `WARN` | Situações inesperadas mas recuperáveis: hash mismatch numa retry, peer ofline. |
| `ERROR` | Falhas reais que impedem operação: persistência falhou, request HTTP retornou 5xx. |

## Correlation IDs (`op_id`)

Toda request HTTP recebe automaticamente um `op_id` no `ctx` via middleware (`internal/api/api.go opIDMiddleware`). Esse ID:

- Vai em todos os logs derivados dessa request (`slog.InfoContext` consome do ctx).
- Atravessa a rede em chamadas peer-to-peer via header `X-MC-Sinc-Op`.
- Permite reconstruir o fluxo completo de um announce cross-host:

```bash
# No Mac:
jq 'select(.op_id == "abc123")' ~/.mcsinc/logs/app.jsonl

# No Windows (PowerShell):
Get-Content $env:USERPROFILE\.mcsinc\logs\app.jsonl | Where-Object { $_ -match '"op_id":"abc123"' }
```

Pra começar uma operação correlacionável fora de uma request HTTP (ex: tarefa em background):

```go
opCtx, opID := logpkg.NewOp(ctx)
// passa opCtx adiante; opID se precisar mandar via header
```

## Session

Cada execução do binário gera um `session_id` único. Todos os logs daquela sessão carregam o mesmo ID. Pra agrupar logs por execução:

```bash
jq -r '.session_id' ~/.mcsinc/logs/app.jsonl | sort -u
```

A primeira linha de cada arquivo é o `SESSION_START`, com `version`, `os`, `arch`, `build_hash`. A última linha (no shutdown normal) é o `SESSION_END`.

## Privacidade

O sanitizer substitui automaticamente nos logs:

- `$HOME` → `<USER>`
- `$USERPROFILE` (Windows) → `<USER>`
- Path do `--root` configurado → `<ROOT>`

Mesmo assim, **nunca log conteúdo de arquivos**, nomes de projeto sensíveis, ou outras infos privadas em attrs. Hashes e paths relativos são OK.

## Adicionando um log num módulo novo

1. Importe `log/slog`.
2. Defina `const logModule = "meu-modulo"` no topo do arquivo.
3. Use `slog.LevelContext` + atributos. Exemplo:

```go
slog.InfoContext(ctx, "iniciando job de exemplo",
    slog.String("module", logModule),
    slog.String("event_id", "EXAMPLE_START"),
    slog.String("input_path", path))
```

## Próximas evoluções (não implementadas ainda)

- Catálogo formal de `event_id` (`internal/logging/events.go` com enum). Hoje a convenção é solta.
- Export de "debug package" (zip com app.log + app.jsonl + crash.log + metadata) via endpoint `GET /debug/export`.
- Crash capture global via `recover()` + `runtime/debug.Stack()`.
- Developer Mode UI mostrando peers, queue, operations em tempo real.
- Webhook reporting / telemetria remota (opt-in).
