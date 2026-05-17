# Estrutura de pastas do Avid e por que sincronizar `MXF/1` é uma má ideia

> Este documento existe para que qualquer um (inclusive nós no futuro) consiga responder a pergunta "por que vocês não usam Dropbox?" sem perder a paciência.

## A pasta `Avid MediaFiles`

Todo projeto Avid escreve mídia importada numa estrutura assim:

```
Avid MediaFiles/
└── MXF/
    ├── 1/
    │   ├── A001C001.new.01.mxf
    │   ├── A001C001.new.02.mxf
    │   ├── ...
    │   ├── msmMMOB.mdb        ← database binário do Avid (índice da pasta)
    │   └── msmFMID.pmr        ← metadados de mídia
    ├── 2/
    └── ...
```

O número (`1`, `2`, ...) é arbitrário — o Avid simplesmente cria a próxima subpasta quando a anterior fica cheia (limite configurável de arquivos por pasta).

## O que o Avid faz nessa pasta

1. Quando você importa/captura/exporta mídia, o Avid escreve `.mxf` na subpasta ativa.
2. **Logo depois**, atualiza `msmMMOB.mdb` com os novos arquivos.
3. Ao abrir um projeto, o Avid **lê o `.mdb`** para saber o que existe — ele não escaneia a pasta toda toda hora.
4. Se o `.mdb` está desatualizado, o Avid pode rodar um "Refresh Media Databases", que reescreve o arquivo.

O `.mdb` é o **ponto frágil**. Se ele estiver corrompido (escrita parcial, dois processos escrevendo juntos, encoding errado), o sintoma é:
- **Media Offline** em clipes que estão fisicamente ali.
- Avid pedindo para refazer o database.
- No pior caso, perda silenciosa de relink em sequências antigas.

## Por que sincronização genérica quebra isso

Imagine **dois editores** apontando Dropbox/Syncthing/Resilio para `MXF/1`:

### Cenário 1 — Escrita simultânea
- Editor A importa um clipe novo. Avid começa a escrever `A001C001.new.05.mxf`.
- Antes de terminar, o sync detecta o arquivo parcial e começa a propagar.
- Editor B recebe um `.mxf` truncado. Media Offline.

### Cenário 2 — Database race
- Editor A atualiza `msmMMOB.mdb` (escrita atômica do ponto de vista do Avid).
- Editor B faz o mesmo, em paralelo.
- O sync resolve "quem ganha" arbitrariamente. O `.mdb` resultante pode listar arquivos que não existem na máquina, ou faltar listar arquivos que existem.
- O Avid tenta abrir, falha, oferece "rebuild" — e o rebuild pode demorar muito em projetos grandes.

### Cenário 3 — Colisão de nomes
- Dois editores capturam material com o mesmo prefixo (`A001C001.new.01.mxf`).
- O sync sobrescreve um pelo outro. Conteúdo perdido, sem aviso útil.

### Cenário 4 — Conflict files
- Dropbox/Syncthing criam arquivos do tipo `A001C001.new.01 (conflict from joao).mxf`.
- O Avid não reconhece esse padrão de nome. O arquivo fica invisível e gera ruído.

## Como o MC Sinc evita tudo isso

1. **Namespace por usuário.** Cada editor escreve apenas na sua subpasta (`MXF/1`). Mídia recebida vai para `MXF/1-<user>`, jamais sobrescrevendo a do dono.
2. **Commits manuais.** O usuário decide quando "fechar" um conjunto de arquivos. Nada é propagado durante uma importação em curso.
3. **Debounce no watcher.** Mesmo que o usuário aperte commit cedo demais, o watcher espera 3s sem mudanças num arquivo antes de considerá-lo elegível.
4. **Sem tocar no `.mdb` alheio.** O Avid de cada editor gerencia seu próprio `.mdb` na sua subpasta. Quando uma nova pasta `1-joao` aparece, o Avid indexa naturalmente (e cria um `.mdb` próprio dentro dela na primeira leitura).
5. **Pull explícito.** O receptor escolhe se quer baixar. Em projetos grandes, isso evita encher disco com material que o editor não precisa.

## O que o MC Sinc NÃO faz

- Não modifica `msmMMOB.mdb` de ninguém.
- Não roda nada dentro do Avid (sem plugin, sem AMA, sem AAX).
- Não substitui storage compartilhado profissional (Avid NEXIS, EditShare, etc). Para times maiores que ~10 pessoas, use shared storage de verdade.
- Não toca em bins (`.avp`) — só mídia. Compartilhamento de bins é outro problema (e outro projeto, talvez).

## Leitura adicional

- [Avid Knowledge Base — Media Database Concepts](https://avid.secure.force.com/pkb/) (procure por "MSM Media Indexer").
- Discussões em [Avid Community](https://community.avid.com/) sobre Dropbox/Sync em `Avid MediaFiles` (spoiler: todas terminam mal).
