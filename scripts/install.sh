#!/usr/bin/env bash
# MC Sinc — install script (macOS / Linux)
#
# PLACEHOLDER: builds binários ainda não publicados.
# Por enquanto este script só explica os passos manuais.
# Quando publicarmos releases, ele vai baixar o binário correto
# para a arquitetura corrente e instalar em /usr/local/bin.

set -euo pipefail

cat <<'EOF'
MC Sinc — instalação (macOS / Linux)
=====================================

Builds binários ainda não publicados. Por enquanto, build a partir do source:

  git clone https://github.com/Lohan-Costa/mc-sinc.git
  cd mc-sinc
  go build -o mcsinc ./cmd/mcsinc
  sudo mv mcsinc /usr/local/bin/

Depois rode:

  mcsinc --root "/Volumes/Media/Avid MediaFiles/MXF"

E abra http://localhost:7777 no navegador.

Quando a v0.1.0 sair, este script vai baixar o binário pronto automaticamente.
EOF
