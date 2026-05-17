#!/usr/bin/env bash
# smoke-ui.sh — sobe o MC Sinc + injeta um arquivo fake + simula um commit recebido,
# pra exercitar visualmente a UI da PR #3.
#
# Uso:   ./scripts/smoke-ui.sh
# Parar: Ctrl+C neste terminal (vai parar o mcsinc também).

set -euo pipefail

PROJ_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TEST_DIR="/tmp/mc-ui"
PORT=7777

echo "→ Build do binário em $PROJ_DIR"
cd "$PROJ_DIR"
go build -o mcsinc ./cmd/mcsinc

echo "→ Limpando $TEST_DIR e recriando estrutura MXF/"
rm -rf "$TEST_DIR"
mkdir -p "$TEST_DIR/MXF/1"

echo "→ Gerando um .mxf fake de 5 MB"
dd if=/dev/urandom of="$TEST_DIR/MXF/1/scene01.mxf" bs=1M count=5 2>/dev/null

# Sobe o mcsinc em background; PID guardado pra parar no final.
echo "→ Subindo mcsinc em http://localhost:$PORT (logs em $TEST_DIR/mcsinc.log)"
./mcsinc \
  --root "$TEST_DIR/MXF" \
  --user dev \
  --port "$PORT" \
  --db "$TEST_DIR/manifest.db" \
  > "$TEST_DIR/mcsinc.log" 2>&1 &
MCSINC_PID=$!
trap "echo; echo '→ Parando mcsinc (pid $MCSINC_PID)'; kill $MCSINC_PID 2>/dev/null || true; rm -f \"$PROJ_DIR/mcsinc\"; exit 0" INT TERM

# Espera o watcher pegar o arquivo + hasher processar.
echo "→ Esperando 12s pra watcher + hasher processarem o arquivo"
sleep 12

# Anuncia um commit fake de "maria" pra exercitar a tela de recebidos.
echo "→ Injetando announce fake de 'maria'"
curl -s -X POST "localhost:$PORT/peer/commits" \
  -H 'Content-Type: application/json' \
  -d '{
    "id":"fake-test-001",
    "author":"maria",
    "message":"cena 14 montada",
    "files":[
      {"path":"1/A001.mxf","hash":"deadbeef00000001","size":15728640},
      {"path":"1/A001_audio.mxf","hash":"deadbeef00000002","size":3145728}
    ],
    "created_at":"2026-05-17T12:00:00Z"
  }' > /dev/null

# Abre a UI no browser padrão.
echo "→ Abrindo http://localhost:$PORT no browser"
open "http://localhost:$PORT"

cat <<EOF

==============================================================================
 MC Sinc rodando. Confira no browser:

 [Pendentes]         deve mostrar 1/scene01.mxf (5.0 MB)
 [Commits enviados]  vazio até você fazer commit pela UI
 [Commits recebidos] deve mostrar @maria com 2 arquivos · 18.0 MB

 Sugestões de teste:
   - Stage + commit do scene01.mxf pela UI (digite uma mensagem, clique Commit)
   - Clique "Baixar" no card da maria — vai falhar (esperado)

 Quando terminar, aperta Ctrl+C aqui pra parar tudo.
==============================================================================

EOF

# Mantém o script vivo enquanto o mcsinc estiver rodando.
wait $MCSINC_PID
