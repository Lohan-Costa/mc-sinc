//go:build windows

package avid

import (
	"bytes"
	"os/exec"
	"strings"
)

// isProcessRunning usa tasklist com filtro por nome de imagem.
//
//	tasklist /NH /FO CSV /FI "IMAGENAME eq <name>"
//
// Quando nada é encontrado, tasklist imprime "INFO: No tasks are running..."
// em stdout (não em stderr) e retorna exit 0. Detectamos a presença olhando
// se a saída cita literalmente o nome do processo.
func isProcessRunning(name string) (bool, error) {
	cmd := exec.Command("tasklist", "/NH", "/FO", "CSV", "/FI", "IMAGENAME eq "+name)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return false, err
	}
	return strings.Contains(out.String(), name), nil
}
