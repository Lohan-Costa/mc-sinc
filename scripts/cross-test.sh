#!/usr/bin/env bash
# cross-test.sh — sobe o MC Sinc em modo "esperando outra máquina" pra teste P2P real.
#
# Roda em uma máquina por terminal. Quando você rodar a outra (mac ou windows
# com cross-test.ps1), os dois lados devem se descobrir via mDNS e mostrar
# "Conectado a <peer>". Aí é só usar a UI no browser.
#
# Uso:   ./scripts/cross-test.sh
# Parar: Ctrl+C (limpa tudo).

set -euo pipefail

PROJ_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TEST_DIR="$HOME/mc-sinc-cross-test"
PORT=7777
USER_ID="$(hostname -s)"

# Cores ANSI pro terminal.
C_GREEN=$'\033[32m'
C_YELLOW=$'\033[33m'
C_BLUE=$'\033[34m'
C_DIM=$'\033[2m'
C_RESET=$'\033[0m'
C_BOLD=$'\033[1m'

echo "${C_BOLD}MC Sinc — teste cross-máquina${C_RESET}"
echo

# Pré-flight: Go instalado?
if ! command -v go >/dev/null 2>&1; then
    echo "${C_YELLOW}✗${C_RESET} Go não está instalado."
    echo "  Instale via: ${C_BOLD}brew install go${C_RESET}"
    echo "  Ou baixe direto: https://go.dev/dl/"
    exit 1
fi

echo "${C_GREEN}✓${C_RESET} Go: $(go version | awk '{print $3}')"

# IP local (informativo).
LOCAL_IP="$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || echo "?")"
echo "${C_GREEN}✓${C_RESET} IP local: $LOCAL_IP"
echo "${C_GREEN}✓${C_RESET} Identidade nesta máquina: ${C_BOLD}$USER_ID${C_RESET}"
echo

# Build.
echo "→ Compilando mcsinc…"
cd "$PROJ_DIR"
go build -o mcsinc ./cmd/mcsinc
echo "${C_GREEN}✓${C_RESET} Build OK"

# Pasta fake de teste.
echo "→ Preparando pasta de teste em $TEST_DIR"
mkdir -p "$TEST_DIR/MXF/1"

# Cria 1 .mxf de 3 MB se ainda não tem nada na pasta — evita ficar sem
# conteúdo pra commit. Se o usuário já rodou uma vez, mantém o que tá lá.
if [ -z "$(ls -A "$TEST_DIR/MXF/1" 2>/dev/null | grep -i '\.mxf$' || true)" ]; then
    echo "→ Gerando arquivo .mxf fake de 3 MB"
    dd if=/dev/urandom of="$TEST_DIR/MXF/1/cena01.mxf" bs=1M count=3 2>/dev/null
fi
echo "${C_GREEN}✓${C_RESET} Pasta de teste pronta"
echo

# Sobe o mcsinc em background.
LOG_PATH="$TEST_DIR/mcsinc.log"
DB_PATH="$TEST_DIR/manifest.db"
./mcsinc \
    --root "$TEST_DIR/MXF" \
    --user "$USER_ID" \
    --port "$PORT" \
    --db "$DB_PATH" \
    > "$LOG_PATH" 2>&1 &
MCSINC_PID=$!

# Cleanup no Ctrl+C ou qualquer término.
cleanup() {
    printf "\n"
    echo "${C_DIM}Encerrando mcsinc (pid $MCSINC_PID)…${C_RESET}"
    kill "$MCSINC_PID" 2>/dev/null || true
    rm -f "$PROJ_DIR/mcsinc"
    exit 0
}
trap cleanup INT TERM

# Confirma que subiu (tenta /status num loop curto).
for i in 1 2 3 4 5; do
    if curl -sf "http://localhost:$PORT/status" >/dev/null 2>&1; then
        break
    fi
    sleep 1
    if [ "$i" = "5" ]; then
        echo "${C_YELLOW}✗${C_RESET} mcsinc não respondeu em 5s. Veja o log:"
        tail -20 "$LOG_PATH"
        cleanup
    fi
done

echo "${C_GREEN}✓${C_RESET} MC Sinc rodando em http://localhost:$PORT"
echo

# Abre o browser uma vez.
open "http://localhost:$PORT" 2>/dev/null || true

# Loop de polling — fica re-escrevendo a mesma linha.
echo "${C_BLUE}${C_BOLD}Aguardando outra máquina conectar…${C_RESET}"
echo "${C_DIM}  → Rode o ./scripts/cross-test.sh (Mac) ou .\\scripts\\cross-test.ps1 (Windows) na outra máquina.${C_RESET}"
echo "${C_DIM}  → Ambas precisam estar na mesma rede Wi-Fi/cabo (sem VPN, sem Wi-Fi de visitante).${C_RESET}"
echo

PEER_FOUND=""
DOTS=""
while true; do
    PEERS_JSON="$(curl -s "http://localhost:$PORT/status" 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); print(",".join(d.get("peers") or []))' 2>/dev/null || echo "")"

    if [ -n "$PEERS_JSON" ]; then
        if [ "$PEERS_JSON" != "$PEER_FOUND" ]; then
            PEER_FOUND="$PEERS_JSON"
            printf "\r\033[K"
            echo "${C_GREEN}${C_BOLD}✓ Conectado a: $PEER_FOUND${C_RESET}"
            echo
            echo "${C_BOLD}Passos pra testar a transferência:${C_RESET}"
            echo "  1. Aqui no Mac, abra http://localhost:$PORT (já abriu sozinho)."
            echo "  2. Digite uma mensagem na caixa e clique ${C_BOLD}Enviar${C_RESET}."
            echo "  3. Na outra máquina, atualize a página → vai aparecer um card em ${C_BOLD}Recebidos${C_RESET}."
            echo "  4. Clique ${C_BOLD}Baixar${C_RESET} → o arquivo aterrissa em $TEST_DIR/MXF/1-$USER_ID/."
            echo
            echo "${C_DIM}Ctrl+C aqui pra encerrar o mcsinc.${C_RESET}"
        fi
    else
        DOTS="${DOTS}."
        if [ ${#DOTS} -gt 5 ]; then DOTS="."; fi
        printf "\r\033[K${C_BLUE}aguardando peer${DOTS}${C_RESET}"
    fi
    sleep 3
done
