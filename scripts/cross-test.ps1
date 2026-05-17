# cross-test.ps1 — equivalente Windows do cross-test.sh.
#
# Sobe o MC Sinc em modo "esperando outra máquina" pra teste P2P real.
# Roda em paralelo com o cross-test.sh do Mac (ou outro Windows na mesma
# LAN). Ambos os terminais devem mostrar "Conectado a <peer>" em alguns
# segundos.
#
# Uso:   .\scripts\cross-test.ps1
# Parar: Ctrl+C (limpa tudo).
#
# Se PowerShell bloquear execução, rode uma vez:
#   Set-ExecutionPolicy -Scope CurrentUser RemoteSigned

$ErrorActionPreference = 'Stop'

$ProjDir = (Get-Item (Split-Path -Parent $MyInvocation.MyCommand.Path)).Parent.FullName
$TestDir = Join-Path $env:USERPROFILE 'mc-sinc-cross-test'
$Port    = 7777
$UserId  = $env:COMPUTERNAME
$BinPath = Join-Path $ProjDir 'mcsinc.exe'

Write-Host "MC Sinc — teste cross-máquina" -ForegroundColor Cyan
Write-Host ""

# Pré-flight: Go instalado?
$goCmd = Get-Command go -ErrorAction SilentlyContinue
if (-not $goCmd) {
    Write-Host "✗ Go não está instalado." -ForegroundColor Yellow
    Write-Host "  Instale: https://go.dev/dl/"
    Write-Host "  Depois abra um PowerShell novo (pra recarregar o PATH) e rode de novo."
    exit 1
}
$goVersion = (& go version).Split(' ')[2]
Write-Host "✓ Go: $goVersion" -ForegroundColor Green

# IP local (informativo).
try {
    $localIp = (Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
        Where-Object { $_.IPAddress -notlike '127.*' -and $_.IPAddress -notlike '169.254.*' } |
        Select-Object -First 1).IPAddress
} catch { $localIp = '?' }
if (-not $localIp) { $localIp = '?' }
Write-Host "✓ IP local: $localIp" -ForegroundColor Green
Write-Host "✓ Identidade nesta máquina: $UserId" -ForegroundColor Green
Write-Host ""

# Firewall: tenta criar regra na porta. Se não for admin, só avisa.
$isAdmin = ([Security.Principal.WindowsPrincipal]::new(
    [Security.Principal.WindowsIdentity]::GetCurrent()
)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

$existingRule = Get-NetFirewallRule -DisplayName 'MC Sinc' -ErrorAction SilentlyContinue
if (-not $existingRule) {
    if ($isAdmin) {
        Write-Host "→ Criando regra de firewall pra porta $Port"
        New-NetFirewallRule -DisplayName 'MC Sinc' `
            -Direction Inbound -LocalPort $Port `
            -Protocol TCP -Action Allow | Out-Null
        Write-Host "✓ Firewall liberado" -ForegroundColor Green
    } else {
        Write-Host "⚠ Sem regra de firewall pra porta $Port." -ForegroundColor Yellow
        Write-Host "  Pra criar (uma vez), abra PowerShell como ADMINISTRADOR e rode:"
        Write-Host "    New-NetFirewallRule -DisplayName 'MC Sinc' -Direction Inbound -LocalPort $Port -Protocol TCP -Action Allow" -ForegroundColor Cyan
        Write-Host "  Vou seguir mesmo assim — pode funcionar dependendo do seu perfil de rede."
    }
}
Write-Host ""

# Build.
Write-Host "→ Compilando mcsinc.exe…"
Set-Location $ProjDir
& go build -o 'mcsinc.exe' '.\cmd\mcsinc'
if ($LASTEXITCODE -ne 0) { throw "go build falhou (exit $LASTEXITCODE)" }
Write-Host "✓ Build OK" -ForegroundColor Green

# Pasta fake de teste.
Write-Host "→ Preparando pasta de teste em $TestDir"
$MxfDir = Join-Path $TestDir 'MXF\1'
New-Item -ItemType Directory -Force -Path $MxfDir | Out-Null

# Cria 1 .mxf de 3 MB se ainda não tem nada na pasta.
$existingMxf = Get-ChildItem -Path $MxfDir -Filter '*.mxf' -ErrorAction SilentlyContinue
if (-not $existingMxf) {
    Write-Host "→ Gerando arquivo .mxf fake de 3 MB"
    $fakePath = Join-Path $MxfDir 'cena01.mxf'
    $bytes = [byte[]]::new(3MB)
    [System.Random]::new().NextBytes($bytes)
    [IO.File]::WriteAllBytes($fakePath, $bytes)
}
Write-Host "✓ Pasta de teste pronta" -ForegroundColor Green
Write-Host ""

# Sobe mcsinc em background.
$LogPath = Join-Path $TestDir 'mcsinc.log'
$ErrPath = Join-Path $TestDir 'mcsinc.err'
$DbPath  = Join-Path $TestDir 'manifest.db'

$proc = Start-Process `
    -FilePath $BinPath `
    -ArgumentList @(
        '--root',  (Join-Path $TestDir 'MXF'),
        '--user',  $UserId,
        '--port',  "$Port",
        '--db',    $DbPath
    ) `
    -PassThru -NoNewWindow `
    -RedirectStandardOutput $LogPath `
    -RedirectStandardError  $ErrPath

# Cleanup function.
$cleanup = {
    Write-Host ""
    Write-Host "Encerrando mcsinc (pid $($proc.Id))…" -ForegroundColor DarkGray
    if ($proc -and -not $proc.HasExited) {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
    }
    if (Test-Path $BinPath) { Remove-Item -Force $BinPath -ErrorAction SilentlyContinue }
}

try {
    # Confirma que mcsinc respondeu.
    $up = $false
    for ($i = 0; $i -lt 5; $i++) {
        try {
            Invoke-RestMethod -Uri "http://localhost:$Port/status" -TimeoutSec 1 | Out-Null
            $up = $true; break
        } catch { Start-Sleep -Seconds 1 }
    }
    if (-not $up) {
        Write-Host "✗ mcsinc.exe não respondeu em 5s. Veja o log:" -ForegroundColor Yellow
        Get-Content $LogPath -Tail 20
        return
    }

    Write-Host "✓ MC Sinc rodando em http://localhost:$Port" -ForegroundColor Green
    Write-Host ""

    # Abre o browser uma vez.
    Start-Process "http://localhost:$Port"

    Write-Host "Aguardando outra máquina conectar…" -ForegroundColor Cyan
    Write-Host "  -> Rode o ./scripts/cross-test.sh (Mac) ou .\scripts\cross-test.ps1 (Windows) na outra máquina." -ForegroundColor DarkGray
    Write-Host "  -> Ambas precisam estar na mesma rede Wi-Fi/cabo (sem VPN, sem Wi-Fi de visitante)." -ForegroundColor DarkGray
    Write-Host ""

    $peerFound = ''
    $dots = ''
    while (-not $proc.HasExited) {
        $peersStr = ''
        try {
            $resp = Invoke-RestMethod -Uri "http://localhost:$Port/status" -TimeoutSec 2
            if ($resp.peers) { $peersStr = ($resp.peers -join ',') }
        } catch { }

        if ($peersStr) {
            if ($peersStr -ne $peerFound) {
                $peerFound = $peersStr
                Write-Host "`r$(' ' * 60)`r" -NoNewline
                Write-Host "✓ Conectado a: $peerFound" -ForegroundColor Green
                Write-Host ""
                Write-Host "Passos pra testar a transferência:"
                Write-Host "  1. Aqui no Windows, abra http://localhost:$Port (já abriu sozinho)."
                Write-Host "  2. Digite uma mensagem na caixa e clique Enviar."
                Write-Host "  3. Na outra máquina, atualize a página → vai aparecer um card em Recebidos."
                Write-Host "  4. Clique Baixar → o arquivo aterrissa em $TestDir\MXF\1-$UserId\."
                Write-Host ""
                Write-Host "Ctrl+C aqui pra encerrar o mcsinc." -ForegroundColor DarkGray
            }
        } else {
            $dots = $dots + '.'
            if ($dots.Length -gt 5) { $dots = '.' }
            Write-Host "`r$(' ' * 60)`raguardando peer$dots" -NoNewline -ForegroundColor Cyan
        }
        Start-Sleep -Seconds 3
    }
}
finally {
    & $cleanup
}
