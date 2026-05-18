//go:build windows

package avid

import (
	"bytes"
	"os/exec"
	"strings"
)

// isProcessRunning usa tasklist com filtro por nome de imagem.
//
//	tasklist /NH /FO CSV /FI "IMAGENAME eq <name>.exe"
//
// O filtro IMAGENAME do Windows exige nome EXATO da imagem — que sempre
// termina em .exe. processFilter normaliza isso (se o caller passou
// "AvidMediaComposer", vira "AvidMediaComposer.exe").
//
// Quando nada é encontrado, tasklist imprime "INFO: No tasks are running..."
// em stdout (não em stderr) e retorna exit 0. Detectamos a presença olhando
// se a saída cita literalmente o nome do processo (case-insensitive).
func isProcessRunning(name string) (bool, error) {
	imageName := processFilter(name)
	cmd := exec.Command("tasklist", "/NH", "/FO", "CSV", "/FI", "IMAGENAME eq "+imageName)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return false, err
	}
	return strings.Contains(strings.ToLower(out.String()), strings.ToLower(imageName)), nil
}
