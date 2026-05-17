# Contribuindo com o MC Sinc

Antes de qualquer coisa: obrigado por considerar contribuir. MC Sinc é um projeto pequeno, focado, e queremos manter assim. Toda contribuição — código, documentação, bug report, ideia — é bem-vinda.

## Como começar

1. Abra uma **issue** antes de mandar um PR grande. Vamos alinhar escopo e abordagem.
2. Para bugs, inclua: SO, versão do Avid, versão do MC Sinc, e passos para reproduzir.
3. Para features, descreva o caso de uso real — não recursos hipotéticos.

## Setup de desenvolvimento

```bash
git clone https://github.com/Lohan-Costa/mc-sinc.git
cd mc-sinc
go mod download
go build -o mcsinc ./cmd/mcsinc
```

Requisitos:
- Go 1.25+
- Sem CGO (todo o build é pure Go — `modernc.org/sqlite` substitui `mattn/go-sqlite3`).

## Padrão de código

- `gofmt` (ou `goimports`) sempre.
- Nomes em inglês no código; documentação pode ser PT-BR ou EN.
- Logs do binário em inglês (são lidos por usuários internacionais).
- Mensagens da UI em PT-BR por enquanto (i18n virá depois).
- Sem dependências adicionais sem discussão prévia — queremos manter o binário enxuto.

## Padrão de commit

Seguimos um estilo leve de [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(watcher): debounce de 5s para eventos do Avid
fix(manifest): corrigir lock SQLite em Windows
docs: explicar fluxo de commit manual
chore: bump fsnotify v1.7.0
```

## Pull Requests

- Branch a partir de `main`.
- Um PR = uma mudança lógica.
- Inclua testes quando possível.
- Descreva no PR **o problema** que resolve, não só o que faz.
- Marque como `draft` se ainda estiver explorando.

## Plataformas suportadas

Toda mudança precisa funcionar em:
- macOS (Intel + Apple Silicon)
- Windows 10/11

Linux funciona, mas não é prioridade (Avid não roda em Linux).

## Código de conduta

Seja gentil. Editores de vídeo já sofrem o suficiente.
