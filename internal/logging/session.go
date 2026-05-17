package logging

import (
	"crypto/rand"
	"encoding/hex"
)

// newSessionID gera um identificador opaco de 16 bytes em hex (32 chars).
// Não é UUID v4 estrito (sem versão/variante), mas tem entropia equivalente
// e nenhuma dep externa.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Cenário improvável; ainda assim, melhor um id constante que
		// nenhum — bloqueia o startup do app se entropy estiver indisponível
		// seria pior UX que um log com session_id "fallback".
		return "fallback"
	}
	return hex.EncodeToString(b[:])
}
