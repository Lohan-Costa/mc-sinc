# MC Sinc - install script (Windows / PowerShell)
#
# PLACEHOLDER: builds binarios ainda nao publicados.
# Por enquanto este script so explica os passos manuais.
# Quando publicarmos releases, ele vai baixar o binario .exe correto
# e instalar em $env:LOCALAPPDATA\Programs\mcsinc\.

Write-Host @"
MC Sinc - instalacao (Windows)
==============================

Builds binarios ainda nao publicados. Por enquanto, build a partir do source:

  git clone https://github.com/Lohan-Costa/mc-sinc.git
  cd mc-sinc
  go build -o mcsinc.exe .\cmd\mcsinc

Depois rode:

  .\mcsinc.exe --root "D:\Avid MediaFiles\MXF"

E abra http://localhost:7777 no navegador.

Quando a v0.1.0 sair, este script vai baixar o binario pronto automaticamente.
"@
