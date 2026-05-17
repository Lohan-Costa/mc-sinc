package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// opIDKey é a chave unexported usada pra carregar op_id no context.
type opIDKey struct{}

// WithOp insere um op_id existente no context. Logs com esse ctx propagam
// o op_id automaticamente via o multiHandler.
func WithOp(ctx context.Context, opID string) context.Context {
	return context.WithValue(ctx, opIDKey{}, opID)
}

// NewOp gera um op_id curto (8 bytes hex = 16 chars) e devolve (ctx, opID).
// Use no início de uma operação correlacionável (announce, pull, etc.) e
// passe o ctx adiante. Quando o opID precisa cruzar a rede, envie via
// header X-MC-Sinc-Op pro outro nó usar com WithOp.
func NewOp(ctx context.Context) (context.Context, string) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback determinístico - colisão é aceitável neste cenário.
		return ctx, "noop"
	}
	id := hex.EncodeToString(b[:])
	return WithOp(ctx, id), id
}

// OpFromContext devolve o op_id se carregado pelo ctx.
func OpFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v := ctx.Value(opIDKey{})
	s, ok := v.(string)
	return s, ok && s != ""
}
