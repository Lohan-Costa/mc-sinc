package logging

import (
	"log/slog"
	"os"
	"strings"
)

// makeReplacer devolve um slog.HandlerOptions.ReplaceAttr que substitui
// paths sensíveis em valores string. Aplica em todo attr e em msg.
//
// Substituições:
//   - $HOME ou $USERPROFILE → "<USER>"
//   - root (se passado em Config.Root) → "<ROOT>"
//
// Match é simples: substring replace. Paths absolutos do usuário aparecem
// em muitos lugares (manifest paths relativos não, mas erros os.Open
// embutem caminho completo no err.Error()).
func makeReplacer(rootSubst string) func([]string, slog.Attr) slog.Attr {
	home, _ := os.UserHomeDir()
	winProfile := os.Getenv("USERPROFILE")

	return func(groups []string, a slog.Attr) slog.Attr {
		if a.Value.Kind() != slog.KindString {
			return a
		}
		s := a.Value.String()
		out := sanitizePath(s, home, winProfile, rootSubst)
		if out != s {
			a.Value = slog.StringValue(out)
		}
		return a
	}
}

// sanitizePath aplica as substituições. Extraído pra ser testável.
func sanitizePath(s, home, winProfile, root string) string {
	if root != "" {
		s = strings.ReplaceAll(s, root, "<ROOT>")
	}
	if home != "" {
		s = strings.ReplaceAll(s, home, "<USER>")
	}
	if winProfile != "" {
		s = strings.ReplaceAll(s, winProfile, "<USER>")
	}
	return s
}
