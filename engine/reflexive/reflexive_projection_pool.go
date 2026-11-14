package reflexive

// reflexive_projection_pool.go
//
// The shared connection pool on the current projection file. Opening the
// database for every request costs more than a small query and made readers
// queue behind writers; with the write-ahead log, one pool serves everyone.
//
// A rebuild never replaces a file under a path in use (see dbPath): it publishes
// a new generation file, and the pool follows the pointer.

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"time"
)

// oldGenerationGrace is how long a replaced projection file stays open for
// readers that already hold it. Tests shorten it.

const defaultProjectionConns = 16

// projectionPool is the connection pool on the current projection file.
type projectionPool struct {
	mu   sync.Mutex
	path string
	info os.FileInfo // identity of the file the pool was opened on
	db   *sql.DB
}

// projection returns the shared pool on the current projection file, opening it
// on first use. If the current file is no longer the one the pool was opened on
// (new generation, or the file was deleted or replaced), the old pool is
// retired and a new one is opened.
//
// The pool must never be closed by its callers.
func (l *ReflexiveLoop) projection() (*sql.DB, error) {
	p := &l.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	// The path is resolved under the lock: resolved before it, a caller could
	// carry a path that another caller has already moved past, move the pool
	// back to a generation that is being removed, and recreate it empty.
	path := dbPath(l.frame.ContextDir)
	if p.db != nil {
		if fi, err := os.Stat(path); err == nil && p.path == path && os.SameFile(fi, p.info) {
			return p.db, nil
		}
		if p.path == path {
			// Same path, different or missing file: close BEFORE opening again, so
			// the old connections cannot touch the companion files of the new one.
			_ = p.db.Close()
		} else {
			retireProjection(p.db, p.path, oldGenerationGrace)
		}
		p.db, p.info, p.path = nil, nil, ""
	}
	// The directory is not created here: a read before any projection exists
	// must fail to open, as it always did. Writers create the directory themselves.
	db, err := sql.Open(sqliteDriverName, sqliteConnDSN(path))
	if err != nil {
		return nil, err
	}
	conns := l.cfg.ProjectionConns
	if conns <= 0 {
		conns = defaultProjectionConns
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	db.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	p.db, p.info, p.path = db, fi, path
	return db, nil
}

// retireProjection closes a pool and removes its file after a grace period, so
// readers that already obtained it can finish.
func retireProjection(db *sql.DB, path string, grace time.Duration) {
	time.AfterFunc(grace, func() {
		_ = db.Close()
		removeProjectionFile(path)
	})
}

// retireOldGeneration is called once a rebuild has published a new generation.
//
// A file is only ever removed by the retirement of the pool that used it, after
// that pool was closed: an old pool still in use would otherwise open a new
// connection on the removed path and silently create an empty database there.
func (l *ReflexiveLoop) retireOldGeneration(oldPath string) {
	p := &l.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.db != nil {
		if p.path == oldPath {
			retireProjection(p.db, p.path, oldGenerationGrace)
			p.db, p.info, p.path = nil, nil, ""
		}
		// Otherwise a reader already moved the pool to the new generation and
		// retired the old one, which removes its own file.
		return
	}
	// No pool: nothing in this process has the old file open.
	removeProjectionFile(oldPath)
}

// closeProjection closes the shared pool (at Stop).
func (l *ReflexiveLoop) closeProjection() {
	p := &l.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.db != nil {
		_ = p.db.Close()
		p.db, p.info, p.path = nil, nil, ""
	}
}
