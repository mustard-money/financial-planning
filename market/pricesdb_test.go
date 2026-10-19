package market

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mustard-money/financial-planning/pricestest"
)

func testPrices(t *testing.T, downloaded time.Time, tickers map[string]pricestest.Ticker) *PricesDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prices.db")
	pricestest.Write(t, path, downloaded, tickers)
	prices, err := OpenPricesDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { prices.Close() })
	return prices
}

func TestDBSeriesAreMonthEndReturns(t *testing.T) {
	// Closes for 2000-01 to 2000-05, downloaded partway through May.
	prices := testPrices(t, time.Date(2000, 5, 20, 12, 0, 0, 0, time.UTC), map[string]pricestest.Ticker{
		"VTI": {Start: month(2000, 1), Returns: []float64{0.10, -0.10, 0.05, 0.50}},
	})
	loader := &Loader{Prices: prices}
	series, err := loader.Load("db:VTI")
	if err != nil {
		t.Fatal(err)
	}
	// Each return is from one month's last close to the next's; May wasn't
	// over when the prices were downloaded, so it is left out.
	if series.ID != "db:VTI" || series.Start != month(2000, 2) || !nearAll(series.Returns, []float64{0.10, -0.10, 0.05}) {
		t.Errorf("got %s from %v: %v", series.ID, series.Start, series.Returns)
	}
	if !near(series.LastLevel, 100*1.1*0.9*1.05) {
		t.Errorf("last level %v", series.LastLevel)
	}
}

func TestDBSeriesExtendBackLikeAnyOther(t *testing.T) {
	prices := testPrices(t, now, map[string]pricestest.Ticker{
		"NEW": {Start: month(2000, 6), Returns: []float64{0.03, 0.04}},
		"OLD": {Start: month(2000, 1), Returns: []float64{0.01, 0.01, 0.01, 0.01, 0.01, 0.99, 0.99}},
	})
	series, err := (&Loader{Prices: prices}).Load("db:NEW|db:OLD")
	if err != nil {
		t.Fatal(err)
	}
	if series.Start != month(2000, 2) || !nearAll(series.Returns, []float64{0.01, 0.01, 0.01, 0.01, 0.01, 0.03, 0.04}) {
		t.Errorf("from %v: %v", series.Start, series.Returns)
	}
}

func TestPricesDBHasAndIsStock(t *testing.T) {
	prices := testPrices(t, now, map[string]pricestest.Ticker{
		"AAPL":  {Start: month(2000, 1), Returns: []float64{0.01}, Stock: true},
		"SPY":   {Start: month(2000, 1), Returns: []float64{0.01}, Fund: true},
		"VFINX": {Start: month(2000, 1), Returns: []float64{0.01}},
	})
	for ticker, want := range map[string][2]bool{"AAPL": {true, true}, "SPY": {true, false}, "VFINX": {true, false}, "NOPE": {false, false}} {
		has, err := prices.Has(ticker)
		if err != nil || has != want[0] {
			t.Errorf("Has(%s) = %v, %v; want %v", ticker, has, err, want[0])
		}
		stock, err := prices.IsStock(ticker)
		if err != nil || stock != want[1] {
			t.Errorf("IsStock(%s) = %v, %v; want %v", ticker, stock, err, want[1])
		}
	}
}

func TestDBSeriesErrors(t *testing.T) {
	prices := testPrices(t, now, map[string]pricestest.Ticker{"SPY": {Start: month(2000, 1), Returns: []float64{0.01}}})
	_, err := (&Loader{Prices: prices}).Load("db:NOPE")
	wantErr(t, err, "the prices database has no prices for NOPE")
	_, err = (&Loader{}).Load("db:SPY")
	wantErr(t, err, "db:SPY needs a prices database")

	_, err = OpenPricesDB(filepath.Join(t.TempDir(), "missing.db"))
	wantErr(t, err, "there is no prices database at")
	other := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(other, []byte("these are not prices"), 0o644)
	_, err = OpenPricesDB(other)
	wantErr(t, err, "is not a prices database")
}
