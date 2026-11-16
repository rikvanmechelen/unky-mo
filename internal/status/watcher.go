package status

import (
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Watcher monitors JSONL files for write events and triggers reconciliation.
type Watcher struct {
	w        *fsnotify.Watcher
	onChange func(sessionID string, path string)

	mu       sync.Mutex
	pathToID map[string]string // absolute path → sessionID
	dirRefs  map[string]int    // directory → reference count (for shared dirs)
	done     chan struct{}
	wg       sync.WaitGroup
}

// NewWatcher creates a JSONL file watcher. The onChange callback is called
// when a watched file is written to, with the session ID and file path.
func NewWatcher(onChange func(sessionID string, path string)) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		w:        fsw,
		onChange: onChange,
		pathToID: make(map[string]string),
		dirRefs:  make(map[string]int),
		done:     make(chan struct{}),
	}
	w.wg.Add(1)
	go w.loop()
	return w, nil
}

// WatchSession starts watching a JSONL file for a session.
func (w *Watcher) WatchSession(sessionID, jsonlPath string) {
	abs, err := filepath.Abs(jsonlPath)
	if err != nil {
		return
	}
	dir := filepath.Dir(abs)

	w.mu.Lock()
	defer w.mu.Unlock()

	// Already watching this path for this or another session.
	if _, exists := w.pathToID[abs]; exists {
		w.pathToID[abs] = sessionID // update mapping
		return
	}

	if w.dirRefs[dir] == 0 {
		// First file in this directory — start watching. A session that
		// has just started has no project directory yet (Claude creates
		// it with the first transcript write), so Add fails. Record
		// nothing then: the next poll's WatchSession tries again. Recording
		// the path anyway left the session unwatched for good, and with
		// its status never reconciled it stayed whatever the JSONL
		// bootstrap first read (often "active").
		if err := w.w.Add(dir); err != nil {
			return
		}
	}
	w.pathToID[abs] = sessionID
	w.dirRefs[dir]++
}

// UnwatchSession stops watching a session's JSONL file.
func (w *Watcher) UnwatchSession(sessionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for path, id := range w.pathToID {
		if id != sessionID {
			continue
		}
		delete(w.pathToID, path)
		dir := filepath.Dir(path)
		w.dirRefs[dir]--
		if w.dirRefs[dir] <= 0 {
			delete(w.dirRefs, dir)
			w.w.Remove(dir)
		}
		return
	}
}

// Stop shuts down the watcher and waits for the event loop to exit.
func (w *Watcher) Stop() {
	close(w.done)
	w.w.Close()
	w.wg.Wait()
}

func (w *Watcher) loop() {
	defer w.wg.Done()
	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.w.Events:
			if !ok {
				return
			}
			if !event.Has(fsnotify.Write) {
				continue
			}
			abs, err := filepath.Abs(event.Name)
			if err != nil {
				continue
			}
			w.mu.Lock()
			sid, found := w.pathToID[abs]
			w.mu.Unlock()
			if found {
				w.onChange(sid, abs)
			}
		case _, ok := <-w.w.Errors:
			if !ok {
				return
			}
			// Errors are non-fatal; continue watching.
		}
	}
}
