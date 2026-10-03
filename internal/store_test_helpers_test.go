package buzz

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/lib/pq"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	return openTestStoreWithSetup(t, nil)
}

func openTestStoreWithSetup(t *testing.T, setup func(*sql.DB)) *Store {
	t.Helper()
	rawURL := os.Getenv("BUZZ_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("BUZZ_TEST_DATABASE_URL is not set")
	}

	schema := "test_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	adminDB, err := sql.Open("postgres", rawURL)
	if err != nil {
		t.Fatal(err)
	}
	quotedSchema := pq.QuoteIdentifier(schema)
	if _, err := adminDB.Exec(`CREATE SCHEMA ` + quotedSchema); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}

	setupDB, err := sql.Open("postgres", databaseURLWithSearchPath(rawURL, schema))
	if err != nil {
		adminDB.Exec(`DROP SCHEMA IF EXISTS ` + quotedSchema + ` CASCADE`)
		adminDB.Close()
		t.Fatal(err)
	}
	if setup != nil {
		setup(setupDB)
	}
	if err := setupDB.Close(); err != nil {
		adminDB.Exec(`DROP SCHEMA IF EXISTS ` + quotedSchema + ` CASCADE`)
		adminDB.Close()
		t.Fatal(err)
	}

	store, err := OpenStore(DatabaseConfig{URL: databaseURLWithSearchPath(rawURL, schema)})
	if err != nil {
		adminDB.Exec(`DROP SCHEMA IF EXISTS ` + quotedSchema + ` CASCADE`)
		adminDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := adminDB.Exec(`DROP SCHEMA IF EXISTS ` + quotedSchema + ` CASCADE`); err != nil {
			t.Fatal(err)
		}
		if err := adminDB.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return store
}

func databaseURLWithSearchPath(rawURL, schema string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.Scheme != "" {
		q := parsed.Query()
		q.Set("options", "-c search_path="+schema)
		parsed.RawQuery = q.Encode()
		return parsed.String()
	}
	return fmt.Sprintf("%s options='-c search_path=%s'", rawURL, schema)
}
