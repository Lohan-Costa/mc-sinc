//go:build !windows

package avid

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// isProcessRunning usa `pgrep -x` (exact match no COMM) para encontrar
// o binário principal do editor. -x evita matches indesejados em:
//
//  1. Processos auxiliares no bundle do Avid (avid-api-gateway, etc.)
//     que vivem em AvidMediaComposer.app mas têm COMM próprio.
//  2. O próprio mcsinc, que tem o nome no argv via --avid-process-name —
//     pgrep -x compara o COMM, não a linha de comando inteira.
//
// Códigos de retorno do pgrep:
//
//	0 → match encontrado
//	1 → nenhum match
//	2/3 → erro de invocação
func isProcessRunning(name string) (bool, error) {
	self := os.Getpid()
	out, err := exec.Command("pgrep", "-x", name).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil // não encontrado, sem erro
		}
		return false, err
	}
	// Filtro de self-pid mantido por segurança (defesa em profundidade —
	// se algum dia voltarmos pra -f, ou se o usuário rebatizar o binário
	// pra coincidir com o nome procurado).
	for _, line := range strings.Fields(string(out)) {
		pid, convErr := strconv.Atoi(line)
		if convErr != nil || pid == 0 || pid == self {
			continue
		}
		return true, nil
	}
	return false, nil
}
