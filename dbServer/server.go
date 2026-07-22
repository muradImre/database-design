package dbServer

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/muradImre/database-design/db"
	"github.com/muradImre/database-design/persist"
)

// snapshotDebounce is how long the background writer waits for writes to settle
// before flushing a snapshot, so bursts collapse into a single disk write.
const snapshotDebounce = 250 * time.Millisecond

// Server wraps the HTTP server with optional disk snapshot persistence. Mutating
// requests only mark the store dirty; a background goroutine debounces and
// performs the actual disk I/O off the request path.
type Server struct {
	HTTP         *http.Server
	handlers     *serverHandlers
	dbFactory    DBFactory
	snapshotPath string
	saveMu       sync.Mutex

	dirty chan struct{}
	done  chan struct{}
	wg    sync.WaitGroup
}

// NewManaged creates a Server that supports loading and saving snapshots in
// addition to serving the HTTP API.
func NewManaged(port int, authfac AuthFactory, dbfac DBFactory, dbsfac DatabasesFactory[string, DB], valfac ValidatorFactory, patchApplier PatchApplier) *Server {
	handlers := newHandlers(authfac, dbfac, valfac, dbsfac, patchApplier)

	s := &Server{
		handlers:  handlers,
		dbFactory: dbfac,
		HTTP: &http.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Handler: newMux(handlers),
		},
		dirty: make(chan struct{}, 1),
		done:  make(chan struct{}),
	}

	handlers.onMutation = s.markDirty

	s.wg.Add(1)
	go s.writeLoop()
	return s
}

// SetSnapshotPath sets the file used for persistence. An empty path disables it.
func (s *Server) SetSnapshotPath(path string) {
	s.snapshotPath = path
}

// markDirty records that state changed without blocking the request path.
func (s *Server) markDirty() {
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}

// writeLoop debounces dirty signals and flushes snapshots in the background.
func (s *Server) writeLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.done:
			return
		case <-s.dirty:
			timer := time.NewTimer(snapshotDebounce)
			settled := false
			for !settled {
				select {
				case <-s.dirty:
					if !timer.Stop() {
						<-timer.C
					}
					timer.Reset(snapshotDebounce)
				case <-timer.C:
					settled = true
				case <-s.done:
					timer.Stop()
					return
				}
			}
			if err := s.SaveSnapshot(); err != nil {
				slog.Error("failed to save snapshot", "error", err)
			}
		}
	}
}

// ListenAndServe starts serving HTTP requests.
func (s *Server) ListenAndServe() error {
	return s.HTTP.ListenAndServe()
}

// Close stops the background writer, flushes a final snapshot, and shuts down
// the HTTP server.
func (s *Server) Close() error {
	close(s.done)
	s.wg.Wait()
	if err := s.SaveSnapshot(); err != nil {
		slog.Error("failed to save final snapshot", "error", err)
	}
	return s.HTTP.Close()
}

// SaveSnapshot serializes every store to the configured snapshot path. It is a
// no-op when no path is configured.
func (s *Server) SaveSnapshot() error {
	if s.snapshotPath == "" {
		return nil
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	snap := persist.NewSnapshot()
	pairs, err := s.handlers.databases.Query(context.Background(), "", maxKey)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		database, ok := p.Value.(*db.Database)
		if !ok {
			slog.Warn("skipping store with unexpected type during snapshot", "store", p.Key)
			continue
		}
		store, err := database.ExportSnapshot()
		if err != nil {
			return err
		}
		snap.Stores[p.Key] = store
	}

	if err := persist.Save(s.snapshotPath, snap); err != nil {
		return err
	}
	slog.Debug("snapshot saved", "path", s.snapshotPath, "stores", len(snap.Stores))
	return nil
}

// LoadSnapshot restores stores from the configured snapshot path. A missing file
// is treated as an empty start.
func (s *Server) LoadSnapshot() error {
	if s.snapshotPath == "" {
		return nil
	}

	snap, ok, err := persist.Load(s.snapshotPath)
	if err != nil {
		return err
	}
	if !ok {
		slog.Info("no snapshot found; starting empty", "path", s.snapshotPath)
		return nil
	}

	for name, store := range snap.Stores {
		newDB := s.dbFactory(name)
		database, ok := newDB.(*db.Database)
		if !ok {
			slog.Warn("skipping store with unexpected type during load", "store", name)
			continue
		}
		if err := database.ImportSnapshot(store); err != nil {
			return err
		}
		if _, err := s.handlers.databases.Upsert(name, func(key string, currValue DB, exists bool) (DB, error) {
			return newDB, nil
		}); err != nil {
			return err
		}
	}
	slog.Info("snapshot loaded", "path", s.snapshotPath, "stores", len(snap.Stores))
	return nil
}
