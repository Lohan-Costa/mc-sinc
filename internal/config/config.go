// Package config persiste preferências do usuário entre execuções do
// mcsinc — hoje só a pasta raiz, futuramente outras opções.
//
// Arquivo padrão: ~/.mcsinc/config.json.
//
// Precedência em main.go:
//
//	--root flag    >    config.json    >    auto-discovery
//
// Mudanças pela UI (POST /config) atualizam o arquivo mas NÃO recriam
// o watcher/manifest em runtime — o usuário precisa reiniciar pra
// aplicar. Caminho conservador: refactor pra restart in-process é
// arriscado e fica como follow-up.
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Persistent é o conteúdo serializado em config.json. Adicione campos
// novos com `omitempty` pra preservar compatibilidade futura.
type Persistent struct {
	Root string `json:"root,omitempty"`
}

// Load lê o config.json do path dado. Se o arquivo não existir,
// devolve config zerado + nil (não é erro). Erros de parse propagam.
func Load(path string) (Persistent, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Persistent{}, nil
	}
	if err != nil {
		return Persistent{}, err
	}
	var c Persistent
	if err := json.Unmarshal(b, &c); err != nil {
		return Persistent{}, err
	}
	return c, nil
}

// Save grava config.json atomicamente: escreve num .tmp, depois rename.
// Cria diretórios pais se necessário.
func Save(path string, c Persistent) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
