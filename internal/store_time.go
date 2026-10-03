package buzz

import (
	"database/sql"
	"time"
)

func storeNow() time.Time {
	return time.Now().UTC()
}

func formatStoreTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func formatNullStoreTime(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return formatStoreTime(t.Time)
}
