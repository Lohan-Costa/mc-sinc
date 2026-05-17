// Package web embute os assets estáticos (HTML/CSS/JS) servidos pela API
// HTTP local. Mantemos o embed.FS aqui — em vez do binário main — porque
// //go:embed resolve paths relativos ao diretório do arquivo .go, e essa
// localização nos permite manter cmd/mcsinc/ contendo só a entrada.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:files
var assets embed.FS

// FS devolve o sistema de arquivos enraizado na pasta `files/`,
// pronto pra ser entregue a um http.FileServer.
func FS() (fs.FS, error) {
	return fs.Sub(assets, "files")
}
