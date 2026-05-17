//go:build windows

package avid

import (
	"fmt"
	"os"
)

// ListVolumesPlatform enumera as drive letters montadas no Windows.
//
// Implementação pure-Go: probe de A: até Z: via os.Stat. Mais simples que
// chamar syscall.GetLogicalDrives e sem dependências extras. Tempo gasto
// é insignificante (26 syscalls, todas locais).
//
// Pula A: e B: por convenção — historicamente reservadas pra floppies e
// raramente atribuídas pra storage Avid. Sem impacto real.
func ListVolumesPlatform() ([]string, error) {
	var out []string
	for c := 'A'; c <= 'Z'; c++ {
		drive := fmt.Sprintf("%c:\\", c)
		info, err := os.Stat(drive)
		if err != nil || !info.IsDir() {
			continue
		}
		out = append(out, drive)
	}
	return out, nil
}
