// Package store persists spans in an embedded DuckDB database and answers the
// queries the UI asks of them.
//
// DuckDB is columnar, which suits the access pattern exactly: writes arrive in
// large batches and reads are aggregations over a time range. Being embedded
// keeps apm2go a single process with no external database to install, secure or
// operate.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/marcboeker/go-duckdb/v2"

	"github.com/apm2go/apm2go/internal/config"
)

// metaSchemaVersion is the meta table key holding the schema version.
const metaSchemaVersion = "schema_version"

// Store owns the database handle and the single writer connection.
//
// DuckDB admits one writer at a time, so writes go through a dedicated
// connection guarded by a mutex while reads use the pooled handle. That split
// is why the pipeline batches: one large append costs far less than many small
// ones contending for the same lock.
type Store struct {
	cfg  config.StorageConfig
	log  *slog.Logger
	path string

	db        *sql.DB
	connector *duckdb.Connector

	// writeMu serialises the append path.
	writeMu sync.Mutex

	closeOnce sync.Once
}

// Open creates or opens the database at the configured path and applies the
// schema.
func Open(cfg config.StorageConfig, dataDir string, log *slog.Logger) (*Store, error) {
	path := cfg.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(dataDir, path)
	}

	connector, err := duckdb.NewConnector(path, nil)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}

	db := sql.OpenDB(connector)
	// The writer is serialised by writeMu, so the pool only ever serves reads;
	// a small bound keeps concurrent UI queries from monopolising the host.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	s := &Store{cfg: cfg, log: log, path: path, db: db, connector: connector}

	if err := s.initialize(context.Background()); err != nil {
		s.Close()
		return nil, err
	}

	log.Info("store opened", "path", path, "schema_version", schemaVersion)
	return s, nil
}

// initialize applies pragmas and the schema, and checks version compatibility.
func (s *Store) initialize(ctx context.Context) error {
	for _, stmt := range pragmaStatements(s.cfg.MemoryLimit, s.cfg.Threads) {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply pragma %q: %w", stmt, err)
		}
	}
	for _, stmt := range schemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
	}
	for _, stmt := range migrationStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply migration %q: %w", stmt, err)
		}
	}
	return s.checkSchemaVersion(ctx)
}

// checkSchemaVersion refuses a database written by a newer apm2go. Opening it
// read-write would risk writing rows the older code cannot represent.
func (s *Store) checkSchemaVersion(ctx context.Context) error {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, metaSchemaVersion).Scan(&raw)
	switch {
	case err == sql.ErrNoRows:
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO meta (key, value) VALUES (?, ?)`,
			metaSchemaVersion, strconv.Itoa(schemaVersion))
		if err != nil {
			return fmt.Errorf("record schema version: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("read schema version: %w", err)
	}

	found, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("database has an unreadable schema version %q", raw)
	}
	if found > schemaVersion {
		return fmt.Errorf(
			"database was written by a newer apm2go (schema version %d, this build understands %d); "+
				"upgrade apm2go or start from a fresh data directory", found, schemaVersion)
	}
	if found < schemaVersion {
		// The statements above are all CREATE ... IF NOT EXISTS, so an older
		// database has just been brought up to date by opening it. Recording
		// that is what keeps the version meaningful; leaving it stale would
		// make a future incompatible change unable to tell the two apart.
		if _, err := s.db.ExecContext(ctx,
			`UPDATE meta SET value = ? WHERE key = ?`,
			strconv.Itoa(schemaVersion), metaSchemaVersion); err != nil {
			return fmt.Errorf("record upgraded schema version: %w", err)
		}
		s.log.Info("database schema upgraded", "from", found, "to", schemaVersion)
	}
	return nil
}

// FileSizeBytes reports the database's footprint on disk.
//
// DuckDB writes new data to a write-ahead log beside the main file and folds
// it in on checkpoint; both exist between checkpoints, and both are real disk
// usage, so both are counted. Measuring only the main file would report a
// database as small right up until a checkpoint, then show a jump that never
// actually happened on disk.
func (s *Store) FileSizeBytes() (int64, error) {
	total, err := fileSize(s.path)
	if err != nil {
		return 0, err
	}
	if walSize, err := fileSize(s.path + ".wal"); err == nil {
		total += walSize
	}
	return total, nil
}

// fileSize reports one file's size, treating a missing file as zero rather
// than an error: the WAL in particular does not exist right after a
// checkpoint, which is an ordinary state, not a fault.
func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

// DB exposes the read handle for query code in this package.
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the database. It is safe to call more than once.
func (s *Store) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if s.db != nil {
			err = s.db.Close()
		}
		if s.connector != nil {
			if cerr := s.connector.Close(); err == nil {
				err = cerr
			}
		}
	})
	return err
}
