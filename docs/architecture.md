# Arquitetura do MC Sinc

> Documento vivo. Reflete o estado atual da v0.1.0-alpha e algumas decisões que ainda estão para ser validadas em campo.

## Visão de 30 segundos

```
┌───────────────────────────────────────────────────────────────┐
│                          mcsinc (binário único)               │
│                                                               │
│  ┌──────────┐   ┌──────────┐   ┌──────────┐   ┌────────────┐  │
│  │ Watcher  │──▶│ Manifest │◀──│  Commit  │──▶│ Transport  │──┼─▶ peers
│  │ fsnotify │   │  SQLite  │   │ service  │   │  (LAN…)    │  │
│  └──────────┘   └──────────┘   └──────────┘   └────────────┘  │
│        ▲             ▲              ▲              ▲          │
│        │             │              │              │          │
│        └─────────── HTTP API (chi) ──┴──────────────┘          │
│                          │                                    │
│                     UI web local                              │
└───────────────────────────────────────────────────────────────┘
```

## Componentes

### Watcher (`internal/watcher`)
- Usa `fsnotify` para observar **uma única subpasta** dentro de `MXF/` — a do usuário corrente (ex.: `MXF/1`).
- Filtra por extensão `.mxf`.
- Aplica **debounce de 3 segundos**: o Avid escreve mídia em chunks e gera dezenas de eventos por arquivo. O debounce garante que o arquivo está "quiescente" antes do MC Sinc considerá-lo pronto.
- Não tenta calcular hash no watcher — isso fica para um worker separado (não implementado ainda).

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

### Commit (`internal/commit`)
- Modela a ação humana de "está pronto, pode mandar".
- `Stage` → `Unstage` → `Commit`.
- Um `Commit` é um snapshot: id + autor + mensagem + lista de paths.
- Importante: o pacote `commit` **não fala com a rede**. Ele só atualiza o manifest e devolve a struct `Commit` — o transport é quem decide o que fazer com ela.

### Transport (`internal/transport` + `internal/transport/lan`)
- Interface mínima: `Send` / `Receive` / `ListPeers` / `Close`.
- A primeira implementação concreta é `lan` — usa `discovery` (mDNS) para achar peers e HTTP para anunciar/transferir.
- A interface foi desenhada para suportar implementações futuras (relay WAN, S3-backed async, etc.) sem mudar o resto do sistema.

### Discovery (`internal/discovery`)
- mDNS via `hashicorp/mdns`.
- Service name: `_mcsinc._tcp.local.`
- TXT records: `user=<id>`, `v=<versão>`.
- Browse a cada 10s; mantém um cache thread-safe de peers.

### API HTTP (`internal/api`)
- Servidor `go-chi/chi` v5 ouvindo em `:7777` por default.
- Endpoints (v0.1):
  - `GET /status` — info do nó + peers conhecidos
  - `GET /pending` — arquivos em status `discovered`
  - `POST /commit` — executa um commit dos arquivos `staged`
  - `GET /` (e estáticos) — serve a UI web embedada via `embed.FS`.

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

- [ ] Cálculo de hash em background.
- [ ] Transferência HTTP real entre peers.
- [ ] Pull com confirmação na UI.
- [ ] Renomeação automática quando um arquivo veio de um peer.
- [ ] Histórico persistido de commits enviados/recebidos.
- [ ] Conflito: dois peers commitam um path com o mesmo nome ao mesmo tempo.
- [ ] Verificação de saúde da database do Avid (`msmMMOB.mdb`) antes de mexer em qualquer coisa.

Veja issues abertas no GitHub para o estado atual de cada item.
