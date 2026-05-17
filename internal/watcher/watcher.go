// Package watcher observa a pasta MXF local do Avid e emite eventos
// agregados (com debounce) quando arquivos .mxf são criados ou modificados.
//
// O debounce é importante porque o Avid escreve mídia em pedaços e gera
// muitos eventos fsnotify em sequência. Sem debounce, o commit ficaria
// considerando arquivos ainda em escrita.
package watcher

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultDebounce é o intervalo de quietude exigido antes de emitir um evento.
const DefaultDebounce = 3 * time.Second

// Event representa uma mudança consolidada num arquivo .mxf.
type Event struct {
	Path string
	Op   fsnotify.Op
	At   time.Time
}

// Watcher acompanha um diretório raiz e emite eventos no canal Events.
type Watcher struct {
	root     string
	debounce time.Duration

	fsw     *fsnotify.Watcher
	Events  chan Event
	pending map[string]*time.Timer
	mu      sync.Mutex
}

// New cria um watcher para o diretório dado.
// `root` deve apontar para a subpasta do usuário dentro de MXF (ex: ".../MXF/1").
func New(root string) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		root:     root,
		debounce: DefaultDebounce,
		fsw:      fsw,
		Events:   make(chan Event, 64),
		pending:  make(map[string]*time.Timer),
	}, nil
}

// Run inicia o watcher. Bloqueia até o contexto ser cancelado.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.fsw.Add(w.root); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return w.fsw.Close()
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			if !isMXF(ev.Name) {
				continue
			}
			w.schedule(ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			_ = err // TODO: logger
		}
	}
}

// schedule (re)arma o timer de debounce para um path específico.
// Cada novo evento no mesmo path reseta o timer — o evento só é emitido
// após `debounce` segundos sem novas mudanças.
func (w *Watcher) schedule(ev fsnotify.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if t, ok := w.pending[ev.Name]; ok {
		t.Stop()
	}
	w.pending[ev.Name] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		delete(w.pending, ev.Name)
		w.mu.Unlock()

		w.Events <- Event{Path: ev.Name, Op: ev.Op, At: time.Now()}
	})
}

func isMXF(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".mxf")
}
