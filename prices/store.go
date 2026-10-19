package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

// The database. prices holds a row per ticker and trading day; downloads
// holds how each ticker's last download went, which is what lets a run pick
// up where an earlier one stopped; symbols holds what each listed ticker is.
const schema = `
CREATE TABLE IF NOT EXISTS prices (
	ticker    TEXT    NOT NULL,
	date      TEXT    NOT NULL, -- YYYY-MM-DD, on the exchange's calendar
	year      INTEGER NOT NULL,
	month     INTEGER NOT NULL, -- 1 to 12
	day       INTEGER NOT NULL, -- day of the month
	close     REAL    NOT NULL, -- adjusted for splits
	adj_close REAL    NOT NULL, -- adjusted for splits and dividends
	PRIMARY KEY (ticker, date)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS prices_by_day ON prices (year, month, day);

CREATE TABLE IF NOT EXISTS downloads (
	ticker        TEXT    PRIMARY KEY,
	status        TEXT    NOT NULL CHECK (status IN ('saved', 'no data', 'failed')),
	detail        TEXT    NOT NULL DEFAULT '', -- why it failed
	rows          INTEGER NOT NULL DEFAULT 0,  -- the ticker's rows in prices
	first_date    TEXT,
	last_date     TEXT,
	downloaded_at TEXT    NOT NULL             -- UTC
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS symbols (
	ticker   TEXT    PRIMARY KEY, -- as Yahoo Finance writes it: BRK-B
	symbol   TEXT    NOT NULL,    -- as the exchange writes it: BRK.B
	name     TEXT    NOT NULL,
	exchange TEXT    NOT NULL,
	etf      INTEGER NOT NULL     -- 1 for an exchange-traded fund
) WITHOUT ROWID;
`

// Download statuses.
const (
	statusSaved  = "saved"
	statusNoData = "no data" // Yahoo has no prices for the ticker
	statusFailed = "failed"
)

// store is the SQLite database the prices are saved in.
type store struct {
	db *sql.DB
}

// openStore opens the database at path, creating it and its tables if needed.
func openStore(path string) (*store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	// WAL with synchronous=NORMAL keeps a commit per ticker fast, and still
	// leaves the database intact if the run is killed.
	dsn := url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(abs),
		RawQuery: "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(10000)",
	}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	// One connection: SQLite takes one writer at a time anyway, and this
	// queues the workers' writes instead of having them collide.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot open %s as a prices database: %w", path, err)
	}
	return &store{db: db}, nil
}

func (s *store) Close() error { return s.db.Close() }

// download is how a ticker's last download went.
type download struct {
	Status string
	At     time.Time
}

// downloads returns the last download of every ticker tried so far.
func (s *store) downloads(ctx context.Context) (map[string]download, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ticker, status, downloaded_at FROM downloads`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := map[string]download{}
	for rows.Next() {
		var ticker, at string
		var d download
		if err := rows.Scan(&ticker, &d.Status, &at); err != nil {
			return nil, err
		}
		if d.At, err = time.Parse(time.RFC3339, at); err != nil {
			return nil, fmt.Errorf("downloads: %s has the time %q", ticker, at)
		}
		all[ticker] = d
	}
	return all, rows.Err()
}

// savePrices replaces a ticker's prices with the given history and records
// the download, in one transaction: either all of it is stored or none.
//
// The history is replaced rather than added to because a dividend or split
// changes the adjusted closes of every earlier day.
func (s *store) savePrices(ctx context.Context, ticker string, bars []bar, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM prices WHERE ticker = ?`, ticker); err != nil {
		return err
	}
	insert, err := tx.PrepareContext(ctx,
		`INSERT INTO prices (ticker, date, year, month, day, close, adj_close) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insert.Close()
	for _, b := range bars {
		day, err := time.Parse(time.DateOnly, b.Date)
		if err != nil {
			return fmt.Errorf("%s has the date %q", ticker, b.Date)
		}
		if _, err := insert.ExecContext(ctx, ticker, b.Date, day.Year(), int(day.Month()), day.Day(), price(b.Close), price(b.AdjClose)); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO downloads (ticker, status, rows, first_date, last_date, downloaded_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (ticker) DO UPDATE SET status = excluded.status, detail = '', rows = excluded.rows,
			first_date = excluded.first_date, last_date = excluded.last_date, downloaded_at = excluded.downloaded_at`,
		ticker, statusSaved, len(bars), bars[0].Date, bars[len(bars)-1].Date, timestamp(at))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// saveOutcome records a download that saved nothing. Prices from an earlier
// download, and the count of them, are left as they were.
func (s *store) saveOutcome(ctx context.Context, ticker, status, detail string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO downloads (ticker, status, detail, downloaded_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (ticker) DO UPDATE SET status = excluded.status, detail = excluded.detail,
			downloaded_at = excluded.downloaded_at`,
		ticker, status, detail, timestamp(at))
	return err
}

// saveListings replaces the symbols table with the current directory.
func (s *store) saveListings(ctx context.Context, listings []listing) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM symbols`); err != nil {
		return err
	}
	insert, err := tx.PrepareContext(ctx, `INSERT INTO symbols (ticker, symbol, name, exchange, etf) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insert.Close()
	for _, l := range listings {
		if _, err := insert.ExecContext(ctx, l.Ticker, l.Symbol, l.Name, l.Exchange, l.ETF); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func timestamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// price drops the noise from a price. Yahoo's numbers are 32-bit floats, so
// the shortest decimal that identifies one is the price it means: 222.64, not
// 222.63999938964844.
func price(v float64) float64 {
	clean, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', -1, 32), 64)
	return clean
}
