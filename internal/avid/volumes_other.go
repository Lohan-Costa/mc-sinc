//go:build !darwin && !windows

package avid

// ListVolumesPlatform stub para SOs não suportados oficialmente (Linux,
// BSD). Não há convenção universal de "raiz dos volumes externos" — o
// usuário precisa passar --root manualmente nesses ambientes.
func ListVolumesPlatform() ([]string, error) {
	return nil, nil
}
