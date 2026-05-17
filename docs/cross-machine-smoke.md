# Teste cross-máquina — Mac ↔ Windows

Este runbook explica como exercitar o **caminho P2P real** do MC Sinc entre duas máquinas (uma macOS, outra Windows) na mesma LAN. Os testes unitários e o `smoke-ui.sh` validam o protocolo localmente, mas mDNS não funciona bem sobre loopback do macOS — então a única forma de confirmar discovery + transfer reais é com dois hosts distintos.

A meta final: subir `mcsinc` em cada máquina, ver as duas se descobrirem via mDNS, fazer um commit num lado e baixar pelo outro.

---

## Pré-requisitos

### No Windows

- **Go 1.25+**: instalador oficial em <https://go.dev/dl/>. Depois confirme num PowerShell novo:

  ```powershell
  go version
  ```

- **Git for Windows**: <https://git-scm.com/download/win>.
- **(Opcional)** Liberar execução de scripts PowerShell — só uma vez, em PowerShell como administrador:

  ```powershell
  Set-ExecutionPolicy -Scope CurrentUser RemoteSigned
  ```

  Sem isso, o `smoke-ui.ps1` é bloqueado por padrão.

### No Mac

- Já está rodando aí. Confirma com `go version` (≥ 1.25).

### Rede

- Ambas as máquinas no **mesmo Wi-Fi/cabo, mesma subnet** (faixa de IP comum, tipo `192.168.x.x`).
- VPN ligada, Wi-Fi de visitante ("guest network") com isolamento de clientes, ou VLAN segregada **quebram mDNS**.
- Se as duas máquinas não estiverem se vendo, é quase sempre a rede — checa primeiro com um `ping` entre elas.

---

## Setup Windows (uma vez)

1. **Clone** o repositório em qualquer pasta:

   ```powershell
   git clone https://github.com/Lohan-Costa/mc-sinc.git
   cd mc-sinc
   ```

2. **Libere a porta 7777 no firewall** (PowerShell como administrador, só na primeira vez):

   ```powershell
   New-NetFirewallRule -DisplayName "MC Sinc" `
     -Direction Inbound -LocalPort 7777 -Protocol TCP -Action Allow
   ```

3. **Smoke local primeiro**, pra confirmar que o lado Windows roda de boa antes de tentar cross-máquina:

   ```powershell
   .\scripts\smoke-ui.ps1
   ```

   Deve abrir o browser default em `http://localhost:7777` com a mesma interface do Mac. Se isso funcionou, o build está OK e o binário roda.

---

## Setup Mac (uma vez)

Já está configurado. Pra refrescar:

```bash
cd "/Users/ciclomedia/Documents/PROGRAMAS COM CLAUDE/MC SINC/mc-sinc"
git pull
go build -o mcsinc ./cmd/mcsinc
```

---

## Teste cross-máquina propriamente dito

### 1. Subir os dois nós

**Mac** (terminal):

```bash
./mcsinc --user mac-dev --port 7777
```

Deve aparecer no log:

```
auto-discovery: usando "/Volumes/CICLO-MEDIA/Avid MediaFiles/MXF" (volume "CICLO-MEDIA", último .mdb ...)
MC Sinc 0.1.0-alpha — user="mac-dev" root="..." http=:7777
```

**Windows** (PowerShell):

```powershell
.\mcsinc.exe --user win-dev --port 7777
```

Se você não tem Avid Media Composer no Windows com pasta `Avid MediaFiles\MXF` na raiz de algum drive, o auto-discovery vai falhar. Nesse caso, crie uma estrutura fake e passe `--root` explícito:

```powershell
New-Item -ItemType Directory -Force "$env:TEMP\mc-win\MXF\1" | Out-Null
.\mcsinc.exe --user win-dev --port 7777 --root "$env:TEMP\mc-win\MXF"
```

### 2. Validar descoberta mútua

Em cada máquina, num **segundo terminal**:

```bash
# Mac
curl http://localhost:7777/status
```

```powershell
# Windows
Invoke-RestMethod http://localhost:7777/status
```

A resposta JSON deve incluir o outro usuário em `peers`. Exemplo do Mac:

```json
{
  "user": "mac-dev",
  "peers": ["win-dev"],
  ...
}
```

**Se `peers` ficar vazio depois de 30 segundos**, vai pra seção [Gotchas](#gotchas-conhecidos) abaixo.

### 3. Ciclo de pull real

**No Mac**, criar um arquivo fake na pasta MXF, stagear, e enviar:

```bash
# Cria um .mxf qualquer dentro de MXF/1
dd if=/dev/urandom of="/Volumes/CICLO-MEDIA/Avid MediaFiles/MXF/1/teste-cross.mxf" bs=1M count=3

# Aguarda watcher + hasher (12s)
sleep 12

# Stage + commit via API
curl -X POST localhost:7777/stage \
  -H 'Content-Type: application/json' \
  -d '{"path":"1/teste-cross.mxf"}'

curl -X POST localhost:7777/commit \
  -H 'Content-Type: application/json' \
  -d '{"message":"teste cross-máquina"}'
```

(Substitua o caminho do `dd` pelo seu root real se for diferente. Você pode descobrir abrindo `http://localhost:7777` no browser e olhando a linha "Pasta raiz".)

**No Windows**, abra `http://localhost:7777` no browser. Em poucos segundos, na seção **Recebidos** deve aparecer:

```
@mac-dev · <id-do-commit>                              [AGUARDANDO]
teste cross-máquina
1 arquivo · 3.0 MB · ...                              [Baixar]
```

Clique em **Baixar**. O badge vira `BAIXANDO` e depois `BAIXADO`. O arquivo deve aterrissar em:

```
<seu --root no Windows>\1-mac-dev\teste-cross.mxf
```

### 4. Conferir integridade

No Windows, confira que o conteúdo é idêntico ao original (`Get-FileHash` ou comparar tamanho):

```powershell
Get-FileHash "$env:TEMP\mc-win\MXF\1-mac-dev\teste-cross.mxf" -Algorithm SHA256
```

E no Mac:

```bash
shasum -a 256 "/Volumes/CICLO-MEDIA/Avid MediaFiles/MXF/1/teste-cross.mxf"
```

Os SHA-256 devem bater. O MC Sinc já verifica xxhash64 durante o pull e rejeita mismatches — mas conferir SHA-256 manualmente é a última camada de paranoia.

---

## Gotchas conhecidos

### `peers` continua vazio

- **Rede errada**: confirme que ambos estão no mesmo Wi-Fi. Wi-Fi com "client isolation" (comum em hotéis e cafés) bloqueia mDNS.
- **Ping não funciona entre elas**: provavelmente VPN ativa ou Wi-Fi de visitante.
- **Firewall do Windows**: a regra `New-NetFirewallRule` é por porta TCP — mDNS usa UDP 5353. Versões recentes do Windows permitem mDNS por padrão; se não, libere também UDP 5353.
- **Avast / Norton / outros**: antivírus de terceiros muitas vezes bloqueiam mDNS sem avisar. Desative temporariamente pra testar.

### Pull falha com "hash mismatch"

- Improvável: significa que o byte que chegou no Windows não bate com o xxhash64 anunciado pelo Mac. Pode ser corrupção real de rede. Repita.

### Pull falha sem mensagem clara

- Olha o log do `mcsinc.exe` no Windows: o erro fica detalhado lá.
- A janela do Windows fica com o log inline; no Mac, o terminal de origem também.

### Avid Media Composer interfere

- Se você abrir o Avid no meio do teste, o badge do estado dele muda na UI (`aberto (ocioso)` ou `gravando`). O sync continua funcionando — a detecção é apenas informativa nesta versão.

---

## Descobrir o nome do processo do Avid no Windows

A flag `--avid-process-name` controla qual nome o detector procura. O default `AvidMediaComposer` funciona no macOS, mas o `.exe` do Windows pode ter sufixo. Pra descobrir o nome certo:

1. Abra o Avid Media Composer.
2. Abra o **Gerenciador de Tarefas** (`Ctrl+Shift+Esc`) → aba **Detalhes**.
3. Procure uma entrada com "Avid" no nome. Provavelmente é `AvidMediaComposer.exe`.
4. Rode o `mcsinc.exe` passando esse nome:

   ```powershell
   .\mcsinc.exe --user win-dev --port 7777 --avid-process-name "AvidMediaComposer.exe"
   ```

5. Conferir em `http://localhost:7777` se o badge "Avid" mostra `ABERTO (OCIOSO)` ou similar (em vez de `DESCONHECIDO`).

Se descobrir um nome diferente do esperado, conta pra gente — vamos atualizar o default.

---

## Limpeza após o teste

**Mac:**

```bash
# Apaga os arquivos de teste (não toca em nada que não seja teste-cross.*)
rm "/Volumes/CICLO-MEDIA/Avid MediaFiles/MXF/1/teste-cross.mxf"
rm -rf ~/.mcsinc      # zera o manifest local (cuidado se rodar mcsinc com outros projetos)
```

**Windows:**

```powershell
Remove-Item -Recurse -Force "$env:TEMP\mc-win"
Remove-Item -Recurse -Force "$env:USERPROFILE\.mcsinc"   # mesma ressalva
```

Os binários `mcsinc` / `mcsinc.exe` continuam onde estão se você quiser rodar de novo; é só apagar quando for descartar tudo.
