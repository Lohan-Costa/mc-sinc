// Package fsbrowse expõe um browser de pastas via HTTP pra UI poder
// navegar volumes/diretórios sem precisar de file picker nativo
// (impossível em navegador puro por restrição de segurança).
//
// Suporta:
//   - List(path): lista subdiretórios do path (ou volumes/drives se vazio).
//   - ValidateAvidRoot(path): confirma que path tem nome exato
//     "Avid MediaFiles" E contém subpasta MXF/. Devolve o root final
//     (path/MXF) pra ser salvo no config.
//
// Não lista arquivos — só pastas — porque o picker de raiz só faz
// sentido pra pastas.
package fsbrowse

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// AvidMediaFilesName é o nome exato que o Avid usa pra pasta raiz da
// mídia. O picker da UI só permite selecionar pasta com este nome.
const AvidMediaFilesName = "Avid MediaFiles"

// MxfSubfolderName é a subpasta obrigatória dentro de Avid MediaFiles —
// onde de fato vivem as pastas numéricas (1, 2, ...) com .mxf/.mdb/.pmr.
const MxfSubfolderName = "MXF"

// Entry representa um item navegável (sempre pasta).
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"` // absoluto, separador nativo do OS
	// IsAvidMediaFiles é true quando Name == "Avid MediaFiles" — UI
	// pode destacar visualmente pra orientar o usuário.
	IsAvidMediaFiles bool `json:"is_avid_media_files"`
}

// ListResult é o retorno de /fs/browse.
type ListResult struct {
	// Path absoluto que foi listado. Vazio quando listando volumes raiz.
	Path string `json:"path"`
	// Parent é o path do diretório pai (pra UI permitir "voltar"). Vazio
	// quando Path está numa raiz (volume / drive).
	Parent string `json:"parent,omitempty"`
	// Entries é a lista de subdiretórios. Ordenada alfabeticamente.
	Entries []Entry `json:"entries"`
	// IsVolumeList: true quando estamos listando volumes (Path vazio).
	IsVolumeList bool `json:"is_volume_list,omitempty"`
}

// List devolve os subdiretórios de path. Se path vazio, lista os
// volumes/drives do sistema.
func List(path string) (ListResult, error) {
	if strings.TrimSpace(path) == "" {
		return listVolumes()
	}

	info, err := os.Stat(path)
	if err != nil {
		return ListResult{}, err
	}
	if !info.IsDir() {
		return ListResult{}, fmt.Errorf("nao eh diretorio: %s", path)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return ListResult{}, err
	}

	out := ListResult{Path: path, Parent: parentOf(path)}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Pula pastas escondidas (começam com .) pra reduzir ruído.
		if strings.HasPrefix(name, ".") {
			continue
		}
		out.Entries = append(out.Entries, Entry{
			Name:             name,
			Path:             filepath.Join(path, name),
			IsAvidMediaFiles: name == AvidMediaFilesName,
		})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		return strings.ToLower(out.Entries[i].Name) < strings.ToLower(out.Entries[j].Name)
	})
	return out, nil
}

// ValidateAvidRoot confirma que avidMediaFilesPath aponta pra pasta com
// nome exato "Avid MediaFiles" E contém subpasta "MXF/". Devolve o
// path final pra salvar como --root (avidMediaFilesPath/MXF).
func ValidateAvidRoot(avidMediaFilesPath string) (string, error) {
	clean := filepath.Clean(avidMediaFilesPath)
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("pasta nao acessivel: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("o caminho selecionado nao eh um diretorio")
	}
	if filepath.Base(clean) != AvidMediaFilesName {
		return "", fmt.Errorf("a pasta selecionada precisa se chamar exatamente %q (got %q)",
			AvidMediaFilesName, filepath.Base(clean))
	}
	mxfPath := filepath.Join(clean, MxfSubfolderName)
	mxfInfo, err := os.Stat(mxfPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("subpasta %q nao encontrada em %s", MxfSubfolderName, clean)
	}
	if err != nil {
		return "", err
	}
	if !mxfInfo.IsDir() {
		return "", fmt.Errorf("%s existe mas nao eh diretorio", mxfPath)
	}
	return mxfPath, nil
}

// parentOf devolve o diretório pai, ou vazio se for raiz (/, C:\, etc).
func parentOf(p string) string {
	clean := filepath.Clean(p)
	parent := filepath.Dir(clean)
	if parent == clean {
		return ""
	}
	return parent
}

// listVolumes retorna os pontos de montagem / drives do sistema.
func listVolumes() (ListResult, error) {
	out := ListResult{IsVolumeList: true}
	switch runtime.GOOS {
	case "darwin":
		entries, err := os.ReadDir("/Volumes")
		if err != nil {
			return out, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			full := filepath.Join("/Volumes", e.Name())
			out.Entries = append(out.Entries, Entry{Name: e.Name(), Path: full})
		}
		// Inclui ~ pra usuário poder navegar pelo home se quiser.
		if home, err := os.UserHomeDir(); err == nil {
			out.Entries = append(out.Entries, Entry{Name: "~ (home)", Path: home})
		}
	case "windows":
		// Tenta as letras A-Z. Apenas as que respondem a os.Stat entram.
		for letter := 'A'; letter <= 'Z'; letter++ {
			drive := string(letter) + `:\`
			if _, err := os.Stat(drive); err == nil {
				out.Entries = append(out.Entries, Entry{Name: drive, Path: drive})
			}
		}
	default:
		// Linux / outros: começa em /mnt e /media + home.
		for _, base := range []string{"/mnt", "/media"} {
			if entries, err := os.ReadDir(base); err == nil {
				for _, e := range entries {
					if !e.IsDir() {
						continue
					}
					out.Entries = append(out.Entries, Entry{
						Name: e.Name(),
						Path: filepath.Join(base, e.Name()),
					})
				}
			}
		}
		if home, err := os.UserHomeDir(); err == nil {
			out.Entries = append(out.Entries, Entry{Name: "~ (home)", Path: home})
		}
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		return strings.ToLower(out.Entries[i].Name) < strings.ToLower(out.Entries[j].Name)
	})
	return out, nil
}
