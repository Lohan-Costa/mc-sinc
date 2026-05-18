#!/usr/bin/env bash
# run.sh — sobe o MC Sinc em modo normal (uso real, não teste).
#
# Diferente do cross-test.sh:
# - NÃO cria pasta fake nem .mxf de teste.
# - NÃO passa --root: deixa o mcsinc usar config.json (editavel pela UI)
#   ou auto-discovery.
# - NÃO apaga o binário no fim.
#
# Uso:   ./scripts/run.sh
# Parar: Ctrl+C.
set -euo pipefail

PROJ_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$PROJ_DIR"

# Cores ANSI.
C_GREEN=$'\033[32m'
C_YELLOW=$'\033[33m'
C_BOLD=$'\033[1m'
C_RESET=$'\033[0m'

# Pré-flight: Go instalado.
if ! command -v go >/dev/null 2>&1; then
    echo "${C_YELLOW}✗${C_RESET} Go não está instalado."
    echo "  Instale via: ${C_BOLD}brew install go${C_RESET}"
    echo "  Ou baixe direto: https://go.dev/dl/"
    exit 1
fi
echo "${C_GREEN}✓${C_RESET} Go: $(go version | awk '{print $3}')"

# Build sempre — garante que você está rodando a versão mais nova do main.
echo "→ Compilando mcsinc…"
go build -o mcsinc ./cmd/mcsinc
echo "${C_GREEN}✓${C_RESET} Build OK"
echo

echo "→ Subindo mcsinc em http://localhost:7777"
echo "  Sem --root na linha: usa ~/.mcsinc/config.json se existir,"
echo "  senão tenta auto-discovery em /Volumes."
echo "  Configure pela UI (Editar → escolher Avid MediaFiles)."
echo

# Foreground — Ctrl+C encerra normalmente. Sem --root: lê config.json.
exec ./mcsinc
