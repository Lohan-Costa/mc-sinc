//go:build darwin

package avid

import (
	"os"
	"path/filepath"
)

// ListVolumesPlatform lista os volumes montados no macOS.
//
// Convenção: cada disco montado aparece como subpasta direta de /Volumes/.
// O disco do sistema aparece como /Volumes/<nome> via symlink, mas como
// ninguém trabalha com mídia no disco do sistema, esse caso é incluído
// sem tratamento especial — vai ser descartado depois se não tiver
// "Avid MediaFiles/MXF" na raiz.
func ListVolumesPlatform() ([]string, error) {
	entries, err := os.ReadDir("/Volumes")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		// Ignora qualquer coisa que não seja diretório (raro, mas possível).
		if !e.IsDir() {
			continue
		}
		out = append(out, filepath.Join("/Volumes", e.Name()))
	}
	return out, nil
}
