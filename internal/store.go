package buzz

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	_ "github.com/lib/pq"
)

func OpenStore(cfg DatabaseConfig) (*Store, error) {
	driver := strings.ToLower(cfg.Driver)
	if driver == "" {
		driver = "postgres"
	}
	var dsn string
	switch driver {
	case "postgres", "postgresql", "pg":
		driver = "postgres"
		dsn = cfg.URL
		if dsn == "" {
			return nil, errors.New("database.url is required for postgres")
		}
	default:
		return nil, fmt.Errorf("unsupported database driver %q", cfg.Driver)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.EnsureSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) exec(query string, args ...any) (sql.Result, error) {
	return s.db.Exec(s.rebind(query), args...)
}

func (s *Store) query(query string, args ...any) (*sql.Rows, error) {
	return s.db.Query(s.rebind(query), args...)
}

func (s *Store) queryRow(query string, args ...any) *sql.Row {
	return s.db.QueryRow(s.rebind(query), args...)
}

func (s *Store) prepareTx(tx *sql.Tx, query string) (*sql.Stmt, error) {
	return tx.Prepare(s.rebind(query))
}

func (s *Store) insertReturningID(query string, args ...any) (int64, error) {
	var id int64
	err := s.queryRow(query+" RETURNING id", args...).Scan(&id)
	return id, err
}

func (s *Store) rebind(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	arg := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(arg))
			arg++
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
