// Package pricestest builds small prices databases for tests, in the layout
// the prices command writes.
package pricestest

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mustard-money/financial-planning/calendar"
)

// Ticker is one security's made-up history.
type Ticker struct {
	// Start is the month of its first close, and Returns the change in each
	// month after it.
	Start   calendar.Month
	Returns []float64
	// Stock lists it in the symbol directory as a company's shares; Fund
	// lists it as an ETF. With neither it isn't in the directory, like a
	// mutual fund.
	Stock, Fund bool
}

// Flat is a ticker that returns the same amount in each of the given number
// of months after start.
func Flat(start calendar.Month, months int, monthlyReturn float64) Ticker {
	returns := make([]float64, months)
	for i := range returns {
		returns[i] = monthlyReturn
	}
	return Ticker{Start: start, Returns: returns}
}

// Write creates a prices database at path holding the tickers, as if they
// had all been downloaded at the given time. Each month gets two closes, so
// that reading has to pick the later one.
func Write(t testing.TB, path string, downloaded time.Time, tickers map[string]Ticker) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	exec(`CREATE TABLE prices (ticker TEXT NOT NULL, date TEXT NOT NULL, year INTEGER NOT NULL, month INTEGER NOT NULL,
		day INTEGER NOT NULL, close REAL NOT NULL, adj_close REAL NOT NULL, PRIMARY KEY (ticker, date)) WITHOUT ROWID`)
	exec(`CREATE TABLE downloads (ticker TEXT PRIMARY KEY, status TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '',
		rows INTEGER NOT NULL DEFAULT 0, first_date TEXT, last_date TEXT, downloaded_at TEXT NOT NULL) WITHOUT ROWID`)
	exec(`CREATE TABLE symbols (ticker TEXT PRIMARY KEY, symbol TEXT NOT NULL, name TEXT NOT NULL, exchange TEXT NOT NULL,
		etf INTEGER NOT NULL) WITHOUT ROWID`)

	for name, ticker := range tickers {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		level, rows := 100.0, 0
		for i := 0; i <= len(ticker.Returns); i++ {
			if i > 0 {
				level *= 1 + ticker.Returns[i-1]
			}
			m := ticker.Start.Add(i)
			// A mid-month close that differs from the month's last.
			for day, close := range map[int]float64{15: level * 0.9, 28: level} {
				date := fmt.Sprintf("%04d-%02d-%02d", m.Year(), m.Month(), day)
				if _, err := tx.Exec(`INSERT INTO prices VALUES (?, ?, ?, ?, ?, ?, ?)`, name, date, m.Year(), m.Month(), day, close, close); err != nil {
					t.Fatal(err)
				}
				rows++
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO downloads (ticker, status, rows, downloaded_at) VALUES (?, 'saved', ?, ?)`, name, rows, downloaded.UTC().Format(time.RFC3339))
		if ticker.Stock || ticker.Fund {
			exec(`INSERT INTO symbols VALUES (?, ?, ?, 'NYSE', ?)`, name, name, name, ticker.Fund)
		}
	}
}
