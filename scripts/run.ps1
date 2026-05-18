# run.ps1 - sobe o MC Sinc em modo normal (uso real, nao teste).
#
# Diferente do cross-test.ps1:
# - NAO cria pasta fake nem .mxf de teste.
# - NAO passa --root: usa config.json (editavel pela UI) ou auto-discovery.
# - NAO apaga o exe no fim.
#
# Uso:    .\scripts\run.ps1
# Parar:  Ctrl+C.
#
# IMPORTANTE: ASCII puro (sem acentos) - PS 5.1 le em Windows-1252
# por default e quebra com UTF-8 invalido.
#
# Se PowerShell bloquear execucao, rode uma vez:
#   Set-ExecutionPolicy -Scope CurrentUser RemoteSigned

$ErrorActionPreference = 'Stop'
$ProjDir = (Get-Item (Split-Path -Parent $MyInvocation.MyCommand.Path)).Parent.FullName
Set-Location $ProjDir

# Pre-flight: Go instalado?
$goCmd = Get-Command go -ErrorAction SilentlyContinue
if (-not $goCmd) {
    Write-Host "[X] Go nao esta instalado." -ForegroundColor Yellow
    Write-Host "    Baixe em https://go.dev/dl/ e abra um PowerShell novo apos instalar."
    Read-Host "Pressione Enter para fechar"
    exit 1
}
$goVersion = (& go version).Split(' ')[2]
Write-Host "[OK] Go: $goVersion" -ForegroundColor Green

# Build sempre.
Write-Host "-> Compilando mcsinc.exe..."
& go build -o 'mcsinc.exe' '.\cmd\mcsinc'
if ($LASTEXITCODE -ne 0) { throw "go build falhou (exit $LASTEXITCODE)" }
Write-Host "[OK] Build OK" -ForegroundColor Green
Write-Host ""

Write-Host "-> Subindo mcsinc em http://localhost:7777"
Write-Host "   Sem --root na linha: usa ~/.mcsinc/config.json se existir,"
Write-Host "   senao tenta auto-discovery em drives conectados."
Write-Host "   Configure pela UI (Editar -> escolher Avid MediaFiles)."
Write-Host ""

# Foreground - Ctrl+C encerra normalmente.
& .\mcsinc.exe
