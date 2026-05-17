package avid

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// avidMediaFilesDirName é o nome canônico da pasta raiz onde o Avid
// guarda a estrutura MXF/. Sempre fica na raiz do volume — nunca em
// subpasta — pela convenção do Media Composer.
const avidMediaFilesDirName = "Avid MediaFiles"

// RootCandidate é um possível root pro mcsinc, encontrado pelo discovery.
type RootCandidate struct {
	// Path é o caminho absoluto da pasta MXF dentro do volume.
	// Ex: "/Volumes/CICLO-MEDIA/Avid MediaFiles/MXF".
	Path string

	// VolumeName é o nome do volume (basename do mount point).
	// Ex: "CICLO-MEDIA" (macOS), "D:\\" (Windows).
	VolumeName string

	// LastMDBChange é o mtime mais recente entre os msmMMOB.mdb do volume.
	// Zero (time.Time{}) se nenhum .mdb foi encontrado (volume ainda sem
	// mídia, mas com a estrutura criada).
	LastMDBChange time.Time
}

// Discovery agrupa as dependências de DiscoverRoots.
type Discovery struct {
	// ListVolumes é injetado pra testes. Em produção, usa ListVolumesPlatform
	// (definido por build tag em volumes_<os>.go).
	ListVolumes func() ([]string, error)
}

// DiscoverRoots varre os volumes do sistema e devolve candidatos a `--root`
// — pastas MXF dentro de `Avid MediaFiles` na raiz de cada volume.
//
// Resultado é ordenado por LastMDBChange descendente: o volume "trabalhado
// mais recentemente" vem primeiro. Volumes sem .mdb (zero time) vão pro
// final.
func DiscoverRoots(d Discovery) ([]RootCandidate, error) {
	if d.ListVolumes == nil {
		return nil, fmt.Errorf("discover: ListVolumes não foi injetado")
	}

	volumes, err := d.ListVolumes()
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}

	var out []RootCandidate
	for _, vol := range volumes {
		mxfPath := filepath.Join(vol, avidMediaFilesDirName, "MXF")
		info, statErr := os.Stat(mxfPath)
		if statErr != nil || !info.IsDir() {
			continue
		}
		mtime, _, _, _ := mostRecentMDBOptional(mxfPath)
		out = append(out, RootCandidate{
			Path:          mxfPath,
			VolumeName:    filepath.Base(vol),
			LastMDBChange: mtime,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastMDBChange.After(out[j].LastMDBChange)
	})
	return out, nil
}
