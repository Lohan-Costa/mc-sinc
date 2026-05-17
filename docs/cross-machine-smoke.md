# Teste cross-máquina — Mac ↔ Windows

## TL;DR

Rode um script em cada máquina, no terminal:

```bash
# Mac
./scripts/cross-test.sh
```

```powershell
# Windows
.\scripts\cross-test.ps1
```

Cada script faz tudo sozinho:

- Confere se o Go está instalado.
- (Windows) Tenta criar a regra de firewall pra porta 7777.
- Compila o `mcsinc`.
- Cria uma pasta de teste fake (`~/mc-sinc-cross-test/` no Mac, `%USERPROFILE%\mc-sinc-cross-test\` no Windows) com um `.mxf` aleatório de 3 MB pra ter o que sincronizar.
- Sobe o binário, abre o browser, e fica monitorando a descoberta mDNS.
- Quando o outro lado conecta, mostra `✓ Conectado a <peer>` em verde.

A partir daí, é só usar a UI:

1. Em **qualquer** lado, escreva uma mensagem e clique **Enviar**.
2. No **outro** lado, atualize a página → vai aparecer um card em **Recebidos**.
3. Clique **Baixar** → o arquivo aterrissa em `mc-sinc-cross-test/MXF/1-<sender>/`.
4. `Ctrl+C` em cada terminal pra parar.

---

## Pré-requisitos

- **Go 1.25+** nas duas máquinas. Mac: `brew install go`. Windows: <https://go.dev/dl/>.
- **Mesma rede** (Wi-Fi/cabo, mesma subnet). VPN, VLAN segregada e Wi-Fi de visitante com client isolation **quebram mDNS**.
- **Git for Windows** se for clonar pelo PowerShell: <https://git-scm.com/download/win>.
- **(Windows, uma vez)** Permissão pra rodar scripts PowerShell:
  ```powershell
  Set-ExecutionPolicy -Scope CurrentUser RemoteSigned
  ```

---

## Troubleshooting

### A janela do PowerShell abre e fecha em 1 segundo, sem aparecer nada

Você rodou via botão direito → "Run with PowerShell" no Explorer. Esse modo abre uma janela nova que fecha sozinha quando o script termina — **mesmo que termine com erro**. Não dá pra ler.

Solução: abra um PowerShell **antes**, navegue até a pasta, e rode de lá:

```powershell
cd 'S:\caminho\pro\mc-sinc'
.\scripts\cross-test.ps1
```

A partir da PR #10 os scripts pausam com `Read-Host` ao final em caso de erro, então mesmo o "Run with PowerShell" mostra a mensagem antes de fechar — mas rodar de um PowerShell aberto continua sendo o jeito mais confortável.

### Os dois terminais ficam em "aguardando peer" pra sempre

Quase sempre é rede. Confere na ordem:

1. **Ambas máquinas no mesmo Wi-Fi/cabo?** Faz um `ping` (use o IP impresso por cada script no início). Se o ping não passar, é problema de rede, não do mcsinc.
2. **VPN ligada?** Desliga.
3. **Wi-Fi corporativo / de hotel / "Guest"?** Esses ambientes geralmente isolam clientes entre si. Use cabo ou um Wi-Fi doméstico.
4. **Firewall do Windows?** O script tenta criar a regra; se não rodou como admin, precisa criar manualmente (o script imprime o comando exato).
5. **Antivírus de terceiros?** Norton/Avast/etc. costumam silenciosamente bloquear mDNS. Desativa temporariamente pra confirmar.

### Pull falha com badge `FALHOU` (vermelho)

- Se o hash não bateu, foi corrupção real de rede. Tenta de novo.
- Se a conexão caiu, idem.
- Pra ver detalhes, olha o log no terminal onde o `mcsinc` está rodando — o motivo da falha sai descritivo.

### Avid Media Composer interfere?

Não interfere com o sync — a detecção de estado (badge "Avid" no card Status) é só informativa. Pode até abrir o Avid no meio do teste pra ver o badge mudar de "Desconhecido" pra "Aberto (ocioso)".

---

## Descobrir o nome do processo do Avid no Windows

Pra que o badge "Avid" funcione no Windows, a flag `--avid-process-name` precisa do nome exato do executável. Default é `AvidMediaComposer` (funciona no macOS). No Windows provavelmente é `AvidMediaComposer.exe` — mas pra confirmar:

1. Abra o Avid Media Composer.
2. **Gerenciador de Tarefas** (`Ctrl+Shift+Esc`) → aba **Detalhes**.
3. Procure uma entrada com "Avid" no nome.
4. Rode o script com o nome certo:
   ```powershell
   .\scripts\cross-test.ps1   # rodaria com default
   # ou pra customizar, edite o script ou use mcsinc.exe direto:
   .\mcsinc.exe --user $env:COMPUTERNAME --avid-process-name "AvidMediaComposer.exe"
   ```

Se você descobrir um nome diferente, me avisa pra eu atualizar o default.

---

## Limpeza

Os scripts removem o binário compilado no Ctrl+C, mas **mantêm** a pasta `mc-sinc-cross-test/` (com o `.mxf` que aterrissar) — você confere e deleta à mão quando quiser:

```bash
# Mac
rm -rf ~/mc-sinc-cross-test ~/.mcsinc
```

```powershell
# Windows
Remove-Item -Recurse -Force $env:USERPROFILE\mc-sinc-cross-test, $env:USERPROFILE\.mcsinc
```

(`~/.mcsinc` é onde o manifest SQLite mora; só apague se não estiver usando o mcsinc com mídia real.)
