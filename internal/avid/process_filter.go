package avid

import "strings"

// processFilter retorna o nome a ser usado no filtro IMAGENAME do tasklist
// do Windows. O tasklist exige nome EXATO da imagem, que sempre termina
// em .exe — então se o caller passou só "AvidMediaComposer", apêndamos ".exe".
//
// Helper puro (sem build tag) pra ser testável em qualquer OS.
func processFilter(name string) string {
	if !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name + ".exe"
	}
	return name
}
