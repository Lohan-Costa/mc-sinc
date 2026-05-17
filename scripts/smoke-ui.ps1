# smoke-ui.ps1 — equivalente Windows do scripts/smoke-ui.sh.
#
# Sobe o MC Sinc, injeta um .mxf fake, simula um commit recebido da "maria",
# e abre a UI no browser. Útil pra validar que o lado Windows funciona
# antes de fazer o teste cross-máquina (ver docs/cross-machine-smoke.md).
#
# Uso:   .\scripts\smoke-ui.ps1
# Parar: Ctrl+C neste terminal (vai parar o mcsinc também).
#
# Se PowerShell bloquear execução, rode uma vez (admin):
#   Set-ExecutionPolicy -Scope CurrentUser RemoteSigned

$ErrorActionPreference = 'Stop'

$ProjDir = (Get-Item (Split-Path -Parent $MyInvocation.MyCommand.Path)).Parent.FullName
$TestDir = Join-Path $env:TEMP 'mc-ui'
$Port    = 7777
$BinPath = Join-Path $ProjDir 'mcsinc.exe'

Write-Host "-> Build do binário em $ProjDir"
Set-Location $ProjDir
& go build -o 'mcsinc.exe' '.\cmd\mcsinc'
if ($LASTEXITCODE -ne 0) { throw "go build falhou (exit $LASTEXITCODE)" }

Write-Host "-> Limpando $TestDir e recriando estrutura MXF\"
if (Test-Path $TestDir) { Remove-Item -Recurse -Force $TestDir }
$MxfDir = Join-Path $TestDir 'MXF\1'
New-Item -ItemType Directory -Force -Path $MxfDir | Out-Null

Write-Host "-> Gerando um .mxf fake de 5 MB"
$FakeMxf = Join-Path $MxfDir 'scene01.mxf'
$bytes = [byte[]]::new(5MB)
[System.Random]::new().NextBytes($bytes)
[IO.File]::WriteAllBytes($FakeMxf, $bytes)

# Sobe o mcsinc em background; PID guardado pra parar no final.
Write-Host "-> Subindo mcsinc em http://localhost:$Port (logs em $TestDir\mcsinc.log)"
$LogPath = Join-Path $TestDir 'mcsinc.log'
$ErrPath = Join-Path $TestDir 'mcsinc.err'
$proc = Start-Process `
    -FilePath $BinPath `
    -ArgumentList @(
        '--root', (Join-Path $TestDir 'MXF'),
        '--user', 'dev',
        '--port', "$Port",
        '--db',   (Join-Path $TestDir 'manifest.db')
    ) `
    -PassThru -NoNewWindow `
    -RedirectStandardOutput $LogPath `
    -RedirectStandardError  $ErrPath

# Cleanup ao sair (Ctrl+C ou término normal).
$cleanup = {
    Write-Host ''
    Write-Host "-> Parando mcsinc (pid $($proc.Id))"
    if ($proc -and -not $proc.HasExited) {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
    }
    if (Test-Path $BinPath) { Remove-Item -Force $BinPath -ErrorAction SilentlyContinue }
}

try {
    # Espera o watcher pegar o arquivo + hasher processar.
    Write-Host "-> Esperando 12s pra watcher + hasher processarem o arquivo"
    Start-Sleep -Seconds 12

    # Anuncia um commit fake de "maria" pra exercitar a tela de recebidos.
    Write-Host "-> Injetando announce fake de 'maria'"
    $body = @{
        id         = 'fake-test-001'
        author     = 'maria'
        message    = 'cena 14 montada'
        files      = @(
            @{ path = '1/A001.mxf';       hash = 'deadbeef00000001'; size = 15728640 }
            @{ path = '1/A001_audio.mxf'; hash = 'deadbeef00000002'; size = 3145728  }
        )
        created_at = '2026-05-17T12:00:00Z'
    } | ConvertTo-Json -Depth 5

    Invoke-RestMethod `
        -Method Post `
        -Uri "http://localhost:$Port/peer/commits" `
        -ContentType 'application/json' `
        -Body $body | Out-Null

    # Abre a UI no browser padrão.
    Write-Host "-> Abrindo http://localhost:$Port no browser"
    Start-Process "http://localhost:$Port"

    Write-Host @"

==============================================================================
 MC Sinc rodando. Confira no browser:

 [Pendentes]   deve mostrar 1/scene01.mxf (5.0 MB)
 [Enviados]    vazio até você fazer um envio pela UI
 [Recebidos]   deve mostrar @maria com 2 arquivos · 18.0 MB

 Sugestões de teste:
   - Digite uma mensagem e clique Enviar (vai gerar um envio com 0 arquivos
     porque não fizemos stage explícito — isso é esperado por enquanto).
   - Clique "Baixar" no card da maria — vai falhar (esperado, peer fake).

 Quando terminar, aperta Ctrl+C aqui pra parar tudo.
==============================================================================
"@

    # Mantém o script vivo enquanto o mcsinc estiver rodando.
    while (-not $proc.HasExited) { Start-Sleep -Seconds 1 }
}
finally {
    & $cleanup
}
