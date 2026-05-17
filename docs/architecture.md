# Arquitetura do MC Sinc

> Documento vivo. Reflete o estado atual da v0.1.0-alpha e algumas decisões que ainda estão para ser validadas em campo.

## Visão de 30 segundos

```
┌────────────────────────────────────────────────────────────────────────┐
│                          mcsinc (binário único)                        │
│                                                                        │
│  ┌──────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐   ┌──────┐  │
│  │ Watcher  │──▶│ Manifest │◀──│  Hasher  │   │  Commit  │──▶│Transp│──┼─▶ peers
│  │ fsnotify │   │  SQLite  │   │ xxhash64 │   │ service  │   │(LAN…)│  │
│  └──────────┘   └──────────┘   └──────────┘   └──────────┘   └──────┘  │
│        ▲             ▲              ▲              ▲             ▲     │
│        │             │              │              │             │     │
│        └────────── HTTP API (chi) ──┴──────────────┴─────────────┘     │
│                          │                                             │
│                     UI web local                                       │
└────────────────────────────────────────────────────────────────────────┘
```

## Componentes

### Watcher (`internal/watcher`)
- Usa `fsnotify` para observar **uma única subpasta** dentro de `MXF/` — a do usuário corrente (ex.: `MXF/1`).
- Filtra por extensão `.mxf`.
- Aplica **debounce de 3 segundos**: o Avid escreve mídia em chunks e gera dezenas de eventos por arquivo. O debounce garante que o arquivo está "quiescente" antes do MC Sinc considerá-lo pronto.
- Não tenta calcular hash no watcher — isso fica a cargo do `hasher`.

### Hasher (`internal/hasher`)
- Worker em background que calcula `xxhash64` dos arquivos `.mxf` registrados no manifest.
- **Polling a cada 5s** (`DefaultInterval`): pergunta ao manifest quais arquivos têm `hash = ''` e processa em série.
- Por que serial: hash de `.mxf` é I/O-bound no mesmo volume em que o Avid está escrevendo. Paralelismo não acelera e atrapalha o editor.
- Erros (arquivo sumiu, locked) são logados; a próxima passada tenta de novo — o worker é idempotente.
- Hash é metadado: **não muda `status`**. O ciclo `discovered → staged → committed` continua sendo decisão humana.
- TODO: re-hash quando o `mtime` do arquivo no disco diverge do registrado no manifest (Avid raramente reescreve `.mxf`, mas pode acontecer em recapture).

### Manifest (`internal/manifest`)
- Banco SQLite via `modernc.org/sqlite` (pure Go, sem CGO — facilita cross-compile).
- Tabela única `files`:

  | campo | tipo | descrição |
  |---|---|---|
  | `path` | TEXT PK | caminho relativo à raiz `MXF` |
  | `hash` | TEXT | xxhash64 do conteúdo, hex |
  | `size` | INTEGER | bytes |
  | `modified_at` | INTEGER | unix |
  | `status` | TEXT | `discovered` / `staged` / `committed` / `received` |
  | `updated_at` | INTEGER | unix |

- Status é uma máquina de estados muito simples — não há transições proibidas (ainda); o serviço de commit é quem dita as regras.

- Tabela `commits` (introduzida na PR #2): persiste anúncios enviados (`direction=sent`) e recebidos (`direction=received`) com status `announced` → `pulling` → `pulled`/`failed`.
- Tabela `commit_files`: lista de `(commit_id, path, hash, size)`. Permite ao sender autorizar `/peer/files` (só serve paths que pertencem a um commit `sent` próprio) e ao receiver enumerar o que precisa baixar.

### Commit (`internal/commit`)
- Modela a ação humana de "está pronto, pode mandar".
- `Stage` → `Unstage` → `Commit`.
- Um `Commit` é um snapshot: id + autor + mensagem + lista de `FileSpec{Path, Hash, Size}`.
- Importante: o pacote `commit` **não fala com a rede**. Ele só atualiza o manifest e devolve a struct `Commit` — o transport é quem decide o que fazer com ela.
- Arquivos staged ainda sem hash (hasher não passou) são silenciosamente pulados no commit — voltam no próximo.

### Transport (`internal/transport` + `internal/transport/lan`)
- Interface mínima: `Send` / `Pull` / `ListPeers` / `Routes` / `Close`.
- Modelo **pull explícito**: `Send` anuncia metadata aos peers; bytes só viajam quando o receiver chama `Pull`.
- A primeira implementação concreta é `lan` — usa `discovery` (mDNS) para achar peers e HTTP para anunciar/transferir.
- Endpoints peer-facing (montados sob `/peer/` pelo caller):
  - `POST /peer/commits` — recebe anúncio, persiste com `direction=received`, `status=announced`.
  - `GET  /peer/files/{commit_id}/{path}` — streama bytes; só serve arquivos pertencentes a um commit `direction=sent` do nó (não vaza outros arquivos do disco).
- `Send` é fan-out em goroutines; falhas individuais por peer só logam — o commit local não fracassa por peer offline.
- `Pull` baixa cada file num arquivo `.part`, streama por `xxhash.New()`, verifica contra o hash anunciado. Match → `rename` pro destino + `manifest.Upsert(status=received)`. Mismatch → deleta o temp, marca o commit como `failed`.
- File placement no receiver: `MXF/1-<sender>/<basename(file)>` — drop do prefixo numerado do sender (Avid lista `.mxf` flat dentro da pasta numerada).
- Auth entre peers: LAN-trust nesta versão. Header `X-MC-Sinc-User` viaja como rastro mas não é validado contra mDNS (TODO).
- A interface foi desenhada para suportar implementações futuras (relay WAN, S3-backed async, etc.) sem mudar o resto do sistema.

### Avid state (`internal/avid`)
- Detecta o estado do Avid Media Composer no host atual combinando:
  - **Processo Avid rodando?** Via `pgrep -f` (Unix) ou `tasklist` (Windows). Nome configurável por `--avid-process-name`.
  - **Mtime mais recente de `MXF/*/msmMMOB.mdb`** — o Avid atualiza esse arquivo logo após escrita de mídia.
- Resultado é um `Snapshot` com `State`: `busy` (gravando agora) / `open_idle` (aberto sem escrita) / `recently_closed` (fechou faz <5 min) / `idle` (fechado há tempo bom) / `unknown` (sem `.mdb` e sem processo).
- Janelas (configuráveis): `BusyWindow = 10s`, `RecentWindow = 5min`.
- **Não bloqueia nada nesta versão** — surfaceado em `/status` e na UI como informativo. Quando MC Sinc ganhar modo auto-pull/auto-commit, essa primitiva é o gate.

### Discovery (`internal/discovery`)
- mDNS via `hashicorp/mdns`.
- Service name: `_mcsinc._tcp.local.`
- TXT records: `user=<id>`, `v=<versão>`.
- Browse a cada 10s; mantém um cache thread-safe de peers.

### API HTTP (`internal/api`)
- Servidor `go-chi/chi` v5 ouvindo em `:7777` por default.
- Endpoints UI-facing:
  - `GET  /status` — info do nó + peers conhecidos
  - `GET  /pending` — arquivos em status `discovered`
  - `POST /stage` — marca arquivo pro próximo commit
  - `POST /commit` — executa commit + dispara `transport.Send` em background
  - `GET  /commits/sent` — histórico de commits anunciados
  - `GET  /commits/received` — commits anunciados por peers, aguardando pull
  - `POST /commits/{id}/pull` — dispara pull em background, devolve 202
  - `GET  /` (e estáticos) — UI web embedada
- Endpoints peer-facing são montados sob `/peer/*` via `transport.Routes()`.

### UI web (`web/`)
- HTML + CSS + JS puro. Sem framework, sem bundler, sem build step.
- Carregada via `//go:embed all:web` — vai dentro do binário.
- Poll de `/status` e `/pending` a cada 4s.

## Conceitos-chave

### Hot vs cold folders

| Tipo | Quem escreve | Sincronização | Exemplo |
|---|---|---|---|
| **Hot** local | o usuário | watcher + commit | `MXF/1` |
| **Hot** peer | outro usuário (read-only para você) | pull manual | `MXF/1-joao` |
| **Cold** | ninguém edita ativamente | sync passivo de fundo | `MXF/1000` |

Hot folders precisam de cuidado: o Avid escreve direto nelas. Cold folders podem ser sincronizadas de forma menos cerimoniosa.

### Commit manual

Inspirado em Git. O usuário **sempre decide** quando enviar e quando receber. Razão: editores Avid já têm uma relação delicada com automação silenciosa — qualquer sincronização "mágica" gera desconfiança (justificada).

### Namespace por usuário

Cada peer publica sua mídia numa subpasta `MXF/1-<user>`. Isso:
- evita colisão de nomes (`A001C001.new.01.mxf` de duas pessoas).
- mantém o Avid feliz (ele simplesmente vê mais pastas e indexa).
- permite read-only para o receptor (só o dono escreve em `1-<user>`).

## O que ainda não está implementado

- [x] Cálculo de hash em background.
- [ ] Re-hash quando mtime do arquivo muda.
- [x] Transferência HTTP real entre peers.
- [ ] Pull com confirmação na UI (backend pronto via `POST /commits/{id}/pull`; UI fica pra PR #3).
- [x] Renomeação automática quando um arquivo veio de um peer (`MXF/1-<sender>/`).
- [x] Histórico persistido de commits enviados/recebidos.
- [ ] Conflito: dois peers commitam um path com o mesmo nome ao mesmo tempo.
- [ ] Auth entre peers (token/HMAC); por ora trust LAN.
- [x] Verificação de saúde da database do Avid (`msmMMOB.mdb`) — primitiva `internal/avid` exposta em `/status` (informativo; auto-mode usará no futuro).

Veja issues abertas no GitHub para o estado atual de cada item.
