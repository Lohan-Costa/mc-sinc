# MC Sinc

> Sincronização colaborativa de mídia para editores **Avid Media Composer** em pequenas equipes.

**MC Sinc** é uma ferramenta open source que resolve um problema antigo: como vários editores Avid podem compartilhar e sincronizar arquivos `.mxf` entre si **sem corromper a database do Avid, sem causar Media Offline, e sem race conditions**.

A sigla **MC** oficialmente significa **Media Ciclo Sinc** — mas, dependendo do humor, também é aceito **Mestre de Cerimônia Sinc**. Afinal, alguém precisa coordenar a festa.

[![Build](https://github.com/Lohan-Costa/mc-sinc/actions/workflows/build.yml/badge.svg)](https://github.com/Lohan-Costa/mc-sinc/actions/workflows/build.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

---

## O problema

O Avid Media Composer guarda toda a mídia importada em:

```
Avid MediaFiles/MXF/1/
```

Ferramentas genéricas de sincronização (Dropbox, Syncthing, Resilio Sync, Google Drive...) **não entendem** a lógica dessa pasta. Quando dois ou mais editores apontam essas ferramentas para o mesmo diretório, acontece:

- arquivos `.mxf` sendo sobrescritos no meio de uma escrita
- `msmMMOB.mdb` corrompido
- Media Offline aleatório
- conflitos de nomenclatura entre `A001C001.new.01.mxf` de máquinas diferentes
- a database do Avid simplesmente desistindo da vida

## A solução

Cada editor mantém **sua própria subpasta** isolada dentro de `MXF/`, e a sincronização acontece de forma **explícita, com commits manuais** — inspirado no Git.

```
Avid MediaFiles/MXF/
├── 1              → mídia local do editor atual (hot folder)
├── 1-joao         → mídia recebida do João (read-only para você)
├── 1-maria        → mídia recebida da Maria (read-only para você)
└── 1000           → mídia "fria", estável, raramente muda
```

O fluxo é:

1. Você edita normalmente no Avid. O MC Sinc fica observando `MXF/1`.
2. Quando algo muda, o MC Sinc lista os arquivos novos numa fila.
3. Você decide quando fazer um **commit** ("ok, pode mandar isso pra galera").
4. Os outros editores recebem uma notificação e escolhem fazer **pull**.
5. O Avid enxerga as novas pastas automaticamente — sem corromper nada.

Nada é sincronizado de forma silenciosa ou automática. Você é o MC. Você decide.

---

## Status

🚧 **Alpha — v0.1.0-alpha**. Ainda não pronto para produção. Estamos validando arquitetura.

---

## Stack

- **Go 1.22** — binário único, cross-platform (macOS + Windows).
- **SQLite** via [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite) (pure Go, sem CGO).
- **fsnotify** para watcher de filesystem.
- **hashicorp/mdns** para discovery na LAN.
- **go-chi/chi** v5 para o servidor HTTP local.
- **cespare/xxhash** v2 para hashing rápido de arquivos.
- **UI:** página web local servida pelo próprio binário (HTML + JS puro por enquanto).

---

## Instalação

> ⚠️ Builds binários ainda não publicados. Por enquanto, build a partir do source.

```bash
git clone https://github.com/Lohan-Costa/mc-sinc.git
cd mc-sinc
go build -o mcsinc ./cmd/mcsinc
./mcsinc --root "/Volumes/Media/Avid MediaFiles/MXF"
```

Depois abra http://localhost:7777 no navegador.

Para os scripts de instalação automatizados (em desenvolvimento):

```bash
# macOS / Linux
./scripts/install.sh

# Windows (PowerShell)
.\scripts\install.ps1
```

---

## Uso básico

```bash
mcsinc --root "/Volumes/Media/Avid MediaFiles/MXF" --user joao --port 7777
```

| Flag | Descrição | Default |
|---|---|---|
| `--root` | Caminho da pasta `MXF` do Avid | — (obrigatório) |
| `--user` | Identificador deste editor na rede | hostname |
| `--port` | Porta do servidor HTTP local | `7777` |
| `--db` | Caminho do SQLite local | `$HOME/.mcsinc/manifest.db` |

A UI web mostra:
- arquivos pendentes para commit
- peers descobertos na LAN
- histórico de commits enviados e recebidos

---

## Limites do escopo

- ✅ Até **10 usuários** simultâneos numa LAN.
- ✅ Apenas filesystem — **sem usar o SDK do Avid**.
- ✅ Não modifica nada do comportamento interno do Avid.
- ❌ Não substitui Avid NEXIS / shared storage profissional.
- ❌ Não é solução de backup.
- ❌ Não trabalha (ainda) com bins/`.avp` — só mídia em `MXF/`.

---

## Arquitetura

Veja [docs/architecture.md](docs/architecture.md) para a visão geral, e [docs/avid-structure.md](docs/avid-structure.md) para entender por que sincronizar a pasta `MXF/1` diretamente é uma péssima ideia.

---

## Contribuindo

PRs, issues e ideias são muito bem-vindos. Veja [CONTRIBUTING.md](CONTRIBUTING.md).

---

## Licença

MIT © [Ciclo Media](https://github.com/Lohan-Costa)
