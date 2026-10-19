package market

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mustard-money/financial-planning/calendar"
)

// PricesDB reads the SQLite database the prices command writes: every
// ticker's daily closes, and when each was last downloaded.
type PricesDB struct {
	db *sql.DB
}

// OpenPricesDB opens the database at path for reading only, so it can be read
// while a download is writing to it.
func OpenPricesDB(path string) (*PricesDB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("there is no prices database at %s; the prices command creates one", path)
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: "mode=ro&_pragma=busy_timeout(10000)"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	var tables int
	err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('prices', 'downloads', 'symbols')`).Scan(&tables)
	if err != nil || tables != 3 {
		db.Close()
		return nil, fmt.Errorf("%s is not a prices database", path)
	}
	return &PricesDB{db: db}, nil
}

func (p *PricesDB) Close() error { return p.db.Close() }

// Has reports whether the database holds prices for the ticker.
func (p *PricesDB) Has(ticker string) (bool, error) {
	var rows int
	err := p.db.QueryRow(`SELECT rows FROM downloads WHERE ticker = ?`, ticker).Scan(&rows)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return rows > 0, err
}

// IsStock reports whether the ticker is a listed security that isn't a fund:
// a single company's shares. Tickers missing from the symbol directory, such
// as mutual funds, are not counted as stocks.
func (p *PricesDB) IsStock(ticker string) (bool, error) {
	var etf bool
	err := p.db.QueryRow(`SELECT etf FROM symbols WHERE ticker = ?`, ticker).Scan(&etf)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return !etf, err
}

// monthEnds returns the ticker's last adjusted close of each month, which
// includes dividends, so the change from one month to the next is the total
// return. The month the download was made in is left out: it wasn't over.
func (p *PricesDB) monthEnds(ticker string) (map[calendar.Month]float64, error) {
	var downloadedAt string
	err := p.db.QueryRow(`SELECT downloaded_at FROM downloads WHERE ticker = ? AND rows > 0`, ticker).Scan(&downloadedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("the prices database has no prices for %s", ticker)
	}
	if err != nil {
		return nil, err
	}
	downloaded, err := time.Parse(time.RFC3339, downloadedAt)
	if err != nil {
		return nil, fmt.Errorf("the prices database has the download time %q for %s", downloadedAt, ticker)
	}
	current := calendar.MonthOf(downloaded.UTC())

	rows, err := p.db.Query(`SELECT year, month, adj_close FROM prices WHERE ticker = ? ORDER BY date`, ticker)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	levels := map[calendar.Month]float64{}
	for rows.Next() {
		var year, month int
		var close float64
		if err := rows.Scan(&year, &month, &close); err != nil {
			return nil, err
		}
		// Rows come in date order, so a month's last row is its last close.
		if m := calendar.NewMonth(year, month); m < current && close > 0 {
			levels[m] = close
		}
	}
	return levels, rows.Err()
}
