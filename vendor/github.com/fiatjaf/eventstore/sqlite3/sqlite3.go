package sqlite3

import (
	"github.com/jmoiron/sqlx"
)

type SQLite3Backend struct {
	*sqlx.DB
	DatabaseURL       string
	QueryLimit        int
	QueryIDsLimit     int
	QueryAuthorsLimit int
	QueryKindsLimit   int
	QueryTagsLimit    int
}

func (b *SQLite3Backend) Close() {
	b.DB.Close()
}
