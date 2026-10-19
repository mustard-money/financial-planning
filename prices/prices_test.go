package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mustard-money/financial-planning/market"
)

// now is a Sunday; the last session was Friday October 2.
var now = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

const (
	nasdaqListed = "Symbol|Security Name|Market Category|Test Issue|Financial Status|Round Lot Size|ETF|NextShares\r\n" +
		"MSFT|Microsoft Corporation - Common Stock|Q|N|N|100|N|N\r\n" +
		"QQQ|Invesco QQQ Trust, Series 1|G|N|N|100|Y|N\r\n" +
		"ZXYZ.A|Nasdaq Symbology Test Common Stock|G|Y|N|100|N|N\r\n" +
		"File Creation Time: 1002202621:31|||||||\r\n"
	otherListed = "ACT Symbol|Security Name|Exchange|CQS Symbol|ETF|Round Lot Size|Test Issue|NASDAQ Symbol\r\n" +
		"IBM|International Business Machines Corporation Common Stock|N|IBM|N|100|N|IBM\r\n" +
		"BRK.B|Berkshire Hathaway Inc. New Common Stock|N|BRK.B|N|100|N|BRK.B\r\n" +
		"BAC$B|Bank of America Corporation Depositary Shares|N|BACpB|N|100|N|BAC-B\r\n" +
		"AAC.U|Ares Acquisition Corporation III Units|N|AAC.U|N|100|N|AAC=\r\n" +
		"VTI|Vanguard Total Stock Market ETF|P|VTI|Y|100|N|VTI\r\n" +
		"NEW|A Listing On An Exchange Added Later|F|NEW|N|100|N|NEW\r\n" +
		"ZZZTX|TXSE Test Stk 5 Common Stock|F|ZZZTX|N|40|Y|ZZZTX\r\n" +
		"../X|A Symbol That Is Not A File Name|N|X|N|100|N|X\r\n" +
		"File Creation Time: 1002202621:31||||||\r\n"
)

type day struct {
	date     string // YYYY-MM-DD
	close    *float64
	adjClose *float64
}

func closed(date string, close, adjClose float64) day { return day{date, &close, &adjClose} }

// stamp is 9:30 in New York on the date, as Yahoo stamps a daily bar.
func stamp(date string) int64 {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		panic(err)
	}
	return t.Add(13*time.Hour + 30*time.Minute).Unix()
}

// historyBody is a Yahoo chart response whose current session is the one on
// the given date.
func historyBody(session string, days ...day) string {
	stamps := make([]int64, len(days))
	closes := make([]*float64, len(days))
	adjusted := make([]*float64, len(days))
	for i, d := range days {
		stamps[i], closes[i], adjusted[i] = stamp(d.date), d.close, d.adjClose
	}
	body, _ := json.Marshal(map[string]any{"chart": map[string]any{
		"result": []any{map[string]any{
			"meta": map[string]any{
				"gmtoffset": -14400,
				"currentTradingPeriod": map[string]any{"regular": map[string]any{
					"start": stamp(session), "end": stamp(session) + int64(6.5*3600),
				}},
			},
			"timestamp": stamps,
			"indicators": map[string]any{
				"quote":    []any{map[string]any{"close": closes}},
				"adjclose": []any{map[string]any{"adjclose": adjusted}},
			},
		}},
		"error": nil,
	}})
	return string(body)
}

var twoDays = historyBody("2026-10-02", closed("2026-10-01", 225.6199951171875, 224.5), closed("2026-10-02", 222.63999938964844, 222.63999938964844))

// web stands in for Yahoo Finance and the symbol directory.
type web struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex
	// requests counts chart requests by ticker.
	requests map[string]int
	agents   []string
	// respond answers the nth request (from 1) for a ticker.
	respond func(ticker string, n int) (status int, body string)
	// directoryStatus, when set, is what the symbol directory answers.
	directoryStatus int
}

func newWeb(t *testing.T) *web {
	w := &web{t: t, requests: map[string]int{}}
	w.respond = func(string, int) (int, string) { return http.StatusOK, twoDays }
	w.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "listed.txt") && w.directoryStatus != 0:
			rw.WriteHeader(w.directoryStatus)
		case r.URL.Path == "/nasdaqlisted.txt":
			fmt.Fprint(rw, nasdaqListed)
		case r.URL.Path == "/otherlisted.txt":
			fmt.Fprint(rw, otherListed)
		case strings.HasPrefix(r.URL.Path, "/v8/finance/chart/"):
			ticker := strings.TrimPrefix(r.URL.Path, "/v8/finance/chart/")
			w.mu.Lock()
			w.requests[ticker]++
			n := w.requests[ticker]
			w.agents = append(w.agents, r.UserAgent())
			w.mu.Unlock()
			if r.URL.Query().Get("interval") != "1d" {
				t.Errorf("request for %s is not for daily data: %s", ticker, r.URL.RawQuery)
			}
			status, body := w.respond(ticker, n)
			rw.WriteHeader(status)
			fmt.Fprint(rw, body)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(w.server.Close)
	return w
}

// downloader saves into a new database, without pacing and with pauses too
// short to notice.
func (w *web) downloader() *downloader {
	w.t.Helper()
	s, err := openStore(filepath.Join(w.t.TempDir(), "prices.db"))
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { s.Close() })
	return w.on(s)
}

// on is a downloader saving into the given store.
func (w *web) on(s *store) *downloader {
	return &downloader{
		client:       w.server.Client(),
		yahooURL:     w.server.URL,
		directoryURL: w.server.URL,
		store:        s,
		maxAge:       20 * time.Hour,
		attempts:     3,
		backoff:      time.Millisecond,
		now:          func() time.Time { return now },
	}
}

// at moves the downloader's clock.
func at(d *downloader, t time.Time) { d.now = func() time.Time { return t } }

func (w *web) total() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := 0
	for _, n := range w.requests {
		total += n
	}
	return total
}

// query returns a query's rows, each with its columns joined by "|".
func query(t *testing.T, db *sql.DB, statement string) []string {
	t.Helper()
	rows, err := db.Query(statement)
	if err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
	defer rows.Close()
	columns, _ := rows.Columns()
	var lines []string
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		cells := make([]string, len(values))
		for i, v := range values {
			cells[i] = fmt.Sprint(v)
		}
		lines = append(lines, strings.Join(cells, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func wantRows(t *testing.T, db *sql.DB, statement string, want ...string) {
	t.Helper()
	if got := query(t, db, statement); !slices.Equal(got, want) {
		t.Errorf("%s\n\tgot  %q\n\twant %q", statement, got, want)
	}
}

func numbered(n int) []string {
	tickers := make([]string, n)
	for i := range tickers {
		tickers[i] = fmt.Sprintf("T%02d", i)
	}
	return tickers
}

func TestYahooTicker(t *testing.T) {
	for symbol, want := range map[string]string{
		"IBM":    "IBM",
		"BRK.B":  "BRK-B",
		"BAC$B":  "BAC-PB",
		"ABC$":   "ABC-P",
		"AAC.U":  "AAC-UN",
		"AAC.W":  "AAC-WT",
		"AIIA.R": "AIIA-RI",
		"BCAT.V": "BCAT-WI",
	} {
		if got := yahooTicker(symbol); got != want {
			t.Errorf("yahooTicker(%q) = %q, want %q", symbol, got, want)
		}
	}
}

func TestListedSymbols(t *testing.T) {
	listings, err := newWeb(t).downloader().listedSymbols(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Sorted by ticker, without the test issues or the symbol that isn't a file name.
	var tickers []string
	for _, l := range listings {
		tickers = append(tickers, l.Ticker)
	}
	if want := []string{"AAC-UN", "BAC-PB", "BRK-B", "IBM", "MSFT", "NEW", "QQQ", "VTI"}; !slices.Equal(tickers, want) {
		t.Fatalf("tickers = %v, want %v", tickers, want)
	}
	for _, want := range []listing{
		{Ticker: "BRK-B", Symbol: "BRK.B", Name: "Berkshire Hathaway Inc. New Common Stock", Exchange: "NYSE"},
		{Ticker: "QQQ", Symbol: "QQQ", Name: "Invesco QQQ Trust, Series 1", Exchange: "NASDAQ", ETF: true},
		{Ticker: "VTI", Symbol: "VTI", Name: "Vanguard Total Stock Market ETF", Exchange: "NYSE Arca", ETF: true},
		{Ticker: "NEW", Symbol: "NEW", Name: "A Listing On An Exchange Added Later", Exchange: "F"},
	} {
		if !slices.Contains(listings, want) {
			t.Errorf("missing %+v in %+v", want, listings)
		}
	}
}

func TestListedSymbolsRejectsAnotherFormat(t *testing.T) {
	_, err := parseDirectory("<html>Service unavailable</html>")
	if err == nil || !strings.Contains(err.Error(), "unexpected symbol directory format") {
		t.Errorf("error = %v", err)
	}
}

func TestReadTickers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tickers.txt")
	os.WriteFile(path, []byte("IBM\n\n# indexes\n ^GSPC \nbrk-b\nIBM\n"), 0o644)
	tickers, err := readTickers(path)
	if err != nil || !slices.Equal(tickers, []string{"IBM", "^GSPC", "BRK-B"}) {
		t.Errorf("got %v, %v", tickers, err)
	}
	os.WriteFile(path, []byte("IBM\n../etc/passwd\n"), 0o644)
	if _, err := readTickers(path); err == nil || !strings.Contains(err.Error(), "is not a ticker") {
		t.Errorf("a path as a ticker: %v", err)
	}
	if _, err := readTickers(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Error("a missing file succeeded")
	}
}

func TestParseHistory(t *testing.T) {
	bars, err := parseHistory([]byte(twoDays), now)
	want := []bar{{"2026-10-01", 225.6199951171875, 224.5}, {"2026-10-02", 222.63999938964844, 222.63999938964844}}
	if err != nil || !slices.Equal(bars, want) {
		t.Errorf("got %v, %v; want %v", bars, err, want)
	}
}

func TestParseHistorySkipsDaysWithoutACloseAndFillsAMissingAdjustedClose(t *testing.T) {
	close := 10.0
	body := historyBody("2026-10-02", closed("2026-09-30", 9, 8), day{date: "2026-10-01"}, day{date: "2026-10-02", close: &close})
	bars, err := parseHistory([]byte(body), now)
	want := []bar{{"2026-09-30", 9, 8}, {"2026-10-02", 10, 10}}
	if err != nil || !slices.Equal(bars, want) {
		t.Errorf("got %v, %v; want %v", bars, err, want)
	}
}

func TestParseHistoryLeavesOutASessionInProgress(t *testing.T) {
	body := historyBody("2026-10-02", closed("2026-10-01", 9, 9), closed("2026-10-02", 10, 10))
	open := time.Unix(stamp("2026-10-02"), 0)
	for name, tc := range map[string]struct {
		at   time.Time
		last string
	}{
		"before the open": {open.Add(-time.Hour), "2026-10-01"},
		"mid-session":     {open.Add(3 * time.Hour), "2026-10-01"},
		"after the close": {open.Add(7 * time.Hour), "2026-10-02"},
	} {
		bars, err := parseHistory([]byte(body), tc.at)
		if err != nil || bars[len(bars)-1].Date != tc.last {
			t.Errorf("%s: last bar %v, %v; want %s", name, bars, err, tc.last)
		}
	}
}

func TestParseHistoryKeepsTheLaterOfTwoBarsForADay(t *testing.T) {
	body := historyBody("2026-10-02", closed("2026-10-01", 9, 9), closed("2026-10-02", 10, 10), closed("2026-10-02", 11, 11))
	bars, err := parseHistory([]byte(body), now)
	if err != nil || len(bars) != 2 || bars[1].Close != 11 {
		t.Errorf("got %v, %v", bars, err)
	}
}

func TestParseHistoryErrors(t *testing.T) {
	for body, want := range map[string]string{
		"<html>": "unexpected response from Yahoo Finance",
		`{"chart":{"result":null,"error":{"description":"Too far back"}}}`: "Yahoo Finance: Too far back",
		`{"chart":{"result":[]}}`:                          "unexpected response from Yahoo Finance",
		`{"chart":{"result":[{"meta":{}}]}}`:               "no data",
		historyBody("2026-10-02", day{date: "2026-10-01"}): "no data",
	} {
		if _, err := parseHistory([]byte(body), now); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.40s: error = %v, want %q", body, err, want)
		}
	}
}

func TestRunStoresEachTickersHistory(t *testing.T) {
	w := newWeb(t)
	d := w.downloader()
	counts, err := d.run(context.Background(), []string{"IBM", "BRK-B"}, 2, &strings.Builder{})
	if err != nil || counts != (tally{Saved: 2}) {
		t.Fatalf("got %+v, %v", counts, err)
	}
	// Prices are stored as Yahoo means them, without the 32-bit float noise.
	wantRows(t, d.store.db, `SELECT ticker, date, year, month, day, close, adj_close FROM prices ORDER BY ticker, date`,
		"BRK-B|2026-10-01|2026|10|1|225.62|224.5",
		"BRK-B|2026-10-02|2026|10|2|222.64|222.64",
		"IBM|2026-10-01|2026|10|1|225.62|224.5",
		"IBM|2026-10-02|2026|10|2|222.64|222.64",
	)
	wantRows(t, d.store.db, `SELECT * FROM downloads ORDER BY ticker`,
		"BRK-B|saved||2|2026-10-01|2026-10-02|2026-10-04T12:00:00Z",
		"IBM|saved||2|2026-10-01|2026-10-02|2026-10-04T12:00:00Z",
	)
	if !slices.Equal(w.agents, []string{"Mozilla/5.0", "Mozilla/5.0"}) {
		t.Errorf("user agents sent to Yahoo: %v", w.agents)
	}
}

func TestQueriesByYearAndMonthUseAnIndex(t *testing.T) {
	d := newWeb(t).downloader()
	for statement, index := range map[string]string{
		`SELECT * FROM prices WHERE year = 2008 AND month = 10`:              "prices_by_day",
		`SELECT * FROM prices WHERE ticker = 'IBM' AND date >= '2008-01-01'`: "PRIMARY KEY",
	} {
		plan := strings.Join(query(t, d.store.db, "EXPLAIN QUERY PLAN "+statement), "\n")
		if !strings.Contains(plan, "SEARCH") || !strings.Contains(plan, index) {
			t.Errorf("%s\n\tplan: %s\n\twant a search using %s", statement, plan, index)
		}
	}
}

func TestAnInterruptedRunIsResumed(t *testing.T) {
	w := newWeb(t)
	path := filepath.Join(t.TempDir(), "prices.db")
	tickers := numbered(30)

	// The first run is interrupted while it downloads the sixth ticker.
	first, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, interrupt := context.WithCancel(context.Background())
	w.respond = func(ticker string, _ int) (int, string) {
		if ticker == "T05" {
			interrupt()
		}
		return http.StatusOK, twoDays
	}
	counts, err := w.on(first).run(ctx, tickers, 1, &strings.Builder{})
	if !errors.Is(err, context.Canceled) || counts != (tally{Saved: 5, Pending: 25}) {
		t.Fatalf("the interrupted run: %+v, %v; want 5 saved and 25 not reached", counts, err)
	}
	first.Close()

	// The second, on the same file, does only what the first didn't.
	second, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	w.respond = func(string, int) (int, string) { return http.StatusOK, twoDays }
	counts, err = w.on(second).run(context.Background(), tickers, 3, &strings.Builder{})
	if err != nil || counts != (tally{Saved: 25, Fresh: 5}) {
		t.Fatalf("the resumed run: %+v, %v; want 25 saved and 5 already up to date", counts, err)
	}
	for _, ticker := range tickers {
		// T05 was asked for twice: once when interrupted, once to finish it.
		want := 1
		if ticker == "T05" {
			want = 2
		}
		if w.requests[ticker] != want {
			t.Errorf("%s was requested %d times, want %d", ticker, w.requests[ticker], want)
		}
	}
	wantRows(t, second.db, `SELECT count(DISTINCT ticker), count(*) FROM prices`, "30|60")
	wantRows(t, second.db, `SELECT status, count(*) FROM downloads GROUP BY status`, "saved|30")
}

func TestRunSkipsRecentDownloadsAndReplacesOldOnes(t *testing.T) {
	w := newWeb(t)
	d := w.downloader()
	tickers := []string{"IBM", "VTI"}
	if _, err := d.run(context.Background(), tickers, 1, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}

	at(d, now.Add(19*time.Hour))
	counts, err := d.run(context.Background(), tickers, 1, &strings.Builder{})
	if err != nil || counts != (tally{Fresh: 2}) || w.total() != 2 {
		t.Errorf("19 hours on: %+v, %v after %d requests; want both left alone", counts, err, w.total())
	}

	// A day on, a dividend has changed the adjusted closes, and Yahoo has
	// dropped one day it had before and gained another.
	at(d, now.Add(24*time.Hour))
	w.respond = func(string, int) (int, string) {
		return http.StatusOK, historyBody("2026-10-02", closed("2026-09-30", 100, 99), closed("2026-10-02", 222.64, 220))
	}
	counts, err = d.run(context.Background(), tickers, 1, &strings.Builder{})
	if err != nil || counts != (tally{Saved: 2}) {
		t.Errorf("a day on: %+v, %v; want both downloaded again", counts, err)
	}
	wantRows(t, d.store.db, `SELECT date, close, adj_close FROM prices WHERE ticker = 'IBM' ORDER BY date`,
		"2026-09-30|100|99", "2026-10-02|222.64|220")
	wantRows(t, d.store.db, `SELECT rows, first_date, last_date, downloaded_at FROM downloads WHERE ticker = 'IBM'`,
		"2|2026-09-30|2026-10-02|2026-10-05T12:00:00Z")

	d.maxAge = 0
	if counts, _ := d.run(context.Background(), tickers, 1, &strings.Builder{}); counts != (tally{Saved: 2}) {
		t.Errorf("with no max age: %+v, want both downloaded again", counts)
	}
}

func TestRunRecordsWhatYahooDoesNotHave(t *testing.T) {
	w := newWeb(t)
	w.respond = func(ticker string, _ int) (int, string) {
		switch ticker {
		case "GONE":
			return http.StatusNotFound, `{"chart":{"result":null,"error":{"description":"No data found, symbol may be delisted"}}}`
		case "ODD":
			return http.StatusUnprocessableEntity, `{"chart":{"result":null,"error":{"description":"1d data not available"}}}`
		}
		return http.StatusOK, twoDays
	}
	d := w.downloader()
	tickers := []string{"GONE", "IBM", "ODD"}
	counts, err := d.run(context.Background(), tickers, 1, &strings.Builder{})
	if err != nil || counts != (tally{Saved: 1, NoData: 1, Failed: 1}) {
		t.Errorf("got %+v, %v", counts, err)
	}
	wantRows(t, d.store.db, `SELECT ticker, status, detail, rows FROM downloads ORDER BY ticker`,
		"GONE|no data||0", "IBM|saved||2", "ODD|failed|HTTP 422|0")
	wantRows(t, d.store.db, `SELECT DISTINCT ticker FROM prices`, "IBM")

	// Neither answer is worth a second try within a run. On the next run, a
	// ticker without data is settled, but a failure is tried again.
	counts, err = d.run(context.Background(), tickers, 1, &strings.Builder{})
	if err != nil || counts != (tally{Fresh: 2, Failed: 1}) {
		t.Errorf("the next run: %+v, %v", counts, err)
	}
	if w.requests["GONE"] != 1 || w.requests["IBM"] != 1 || w.requests["ODD"] != 2 {
		t.Errorf("requests: %v", w.requests)
	}
}

func TestAFailedRefreshKeepsThePricesAlreadyStored(t *testing.T) {
	w := newWeb(t)
	d := w.downloader()
	if _, err := d.run(context.Background(), []string{"IBM"}, 1, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	at(d, now.Add(24*time.Hour))
	w.respond = func(string, int) (int, string) { return http.StatusInternalServerError, "" }
	counts, err := d.run(context.Background(), []string{"IBM"}, 1, &strings.Builder{})
	if err != nil || counts != (tally{Failed: 1}) {
		t.Errorf("got %+v, %v", counts, err)
	}
	wantRows(t, d.store.db, `SELECT count(*) FROM prices WHERE ticker = 'IBM'`, "2")
	wantRows(t, d.store.db, `SELECT status, detail, rows, last_date, downloaded_at FROM downloads`,
		"failed|unavailable after 3 tries: HTTP 500|2|2026-10-02|2026-10-05T12:00:00Z")
}

func TestRunRetriesWhenYahooThrottles(t *testing.T) {
	w := newWeb(t)
	w.respond = func(ticker string, n int) (int, string) {
		if ticker == "DOWN" {
			return http.StatusInternalServerError, ""
		}
		if n < 3 {
			return http.StatusTooManyRequests, "Too Many Requests"
		}
		return http.StatusOK, twoDays
	}
	d := w.downloader()
	counts, err := d.run(context.Background(), []string{"IBM", "DOWN"}, 1, &strings.Builder{})
	if err != nil || counts != (tally{Saved: 1, Failed: 1}) {
		t.Errorf("got %+v, %v", counts, err)
	}
	if w.requests["IBM"] != 3 || w.requests["DOWN"] != 3 {
		t.Errorf("requests: %v, want three tries each", w.requests)
	}
	wantRows(t, d.store.db, `SELECT ticker, detail FROM downloads WHERE status = 'failed'`, "DOWN|unavailable after 3 tries: HTTP 500")
}

func TestRunStopsWhenYahooStopsAnswering(t *testing.T) {
	w := newWeb(t)
	w.respond = func(string, int) (int, string) { return http.StatusTooManyRequests, "" }
	d := w.downloader()
	d.attempts = 1
	counts, err := d.run(context.Background(), numbered(100), 1, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "Yahoo Finance stopped answering (20 tickers in a row failed)") {
		t.Errorf("error = %v", err)
	}
	if counts != (tally{Failed: 20, Pending: 80}) || w.total() != 20 {
		t.Errorf("got %+v after %d requests; want 20 failed and 80 not reached", counts, w.total())
	}
	wantRows(t, d.store.db, `SELECT status, count(*) FROM downloads GROUP BY status`, "failed|20")
}

func TestRunDoesNotStopForScatteredFailures(t *testing.T) {
	w := newWeb(t)
	w.respond = func(ticker string, _ int) (int, string) {
		if strings.HasSuffix(ticker, "0") {
			return http.StatusOK, twoDays
		}
		return http.StatusTooManyRequests, ""
	}
	d := w.downloader()
	d.attempts = 1
	counts, err := d.run(context.Background(), numbered(100), 1, &strings.Builder{})
	if err != nil || counts != (tally{Saved: 10, Failed: 90}) {
		t.Errorf("got %+v, %v; every tenth answer should keep the run going", counts, err)
	}
}

func TestRunStopsWhenTheDatabaseCannotBeWritten(t *testing.T) {
	w := newWeb(t)
	d := w.downloader()
	if _, err := d.store.db.Exec(`DROP TABLE prices`); err != nil {
		t.Fatal(err)
	}
	counts, err := d.run(context.Background(), numbered(50), 1, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "cannot write to the database") {
		t.Errorf("error = %v", err)
	}
	if counts.Saved != 0 || counts.Pending != 50 || w.total() > 2 {
		t.Errorf("got %+v after %d requests; want it to stop at the first write", counts, w.total())
	}
}

func TestRunReportsProgress(t *testing.T) {
	var progress strings.Builder
	if _, err := newWeb(t).downloader().run(context.Background(), numbered(500), 4, &progress); err != nil {
		t.Fatal(err)
	}
	want := "250 of 500: 250 saved, 0 already up to date, 0 without data, 0 failed\n500 of 500: 500 saved, 0 already up to date, 0 without data, 0 failed\n"
	if progress.String() != want {
		t.Errorf("progress = %q", progress.String())
	}
}

func TestTheBacktesterReadsWhatTheDownloaderWrites(t *testing.T) {
	// Month-end closes of 100, 110 and 99, then a month still in progress
	// when the download was made.
	w := newWeb(t)
	w.respond = func(string, int) (int, string) {
		return http.StatusOK, historyBody("2026-10-02",
			closed("2026-07-15", 90, 90), closed("2026-07-31", 100, 100),
			closed("2026-08-31", 111, 110),
			closed("2026-09-30", 99, 99),
			closed("2026-10-01", 500, 500))
	}
	path := filepath.Join(t.TempDir(), "prices.db")
	code := run(context.Background(), []string{"-db", path}, &strings.Builder{}, &strings.Builder{},
		func(s *store, maxAge time.Duration, _ float64) *downloader { return w.on(s) })
	if code != 0 {
		t.Fatalf("the download exited %d", code)
	}

	prices, err := market.OpenPricesDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer prices.Close()
	series, err := (&market.Loader{Prices: prices}).Load("db:QQQ")
	if err != nil {
		t.Fatal(err)
	}
	// Total returns from the adjusted closes; October is left out.
	if series.Start.String() != "2026-08" || len(series.Returns) != 2 || math.Abs(series.Returns[0]-0.10) > 1e-9 || math.Abs(series.Returns[1]+0.10) > 1e-9 {
		t.Errorf("from %v: %v", series.Start, series.Returns)
	}
	// The directory says QQQ is a fund and MSFT a company's stock.
	for ticker, want := range map[string]bool{"QQQ": false, "MSFT": true} {
		if stock, err := prices.IsStock(ticker); err != nil || stock != want {
			t.Errorf("IsStock(%s) = %v, %v; want %v", ticker, stock, err, want)
		}
	}
	if has, err := prices.Has("BRK-B"); err != nil || !has {
		t.Errorf("Has(BRK-B) = %v, %v", has, err)
	}
}

func TestOpenStoreKeepsWhatIsThereAndRejectsOtherFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "prices.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.savePrices(context.Background(), "IBM", []bar{{"2026-10-02", 222.64, 222.64}}, now); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	wantRows(t, s.db, `SELECT ticker, date FROM prices`, "IBM|2026-10-02")
	earlier, err := s.downloads(context.Background())
	if err != nil || earlier["IBM"] != (download{Status: "saved", At: now}) {
		t.Errorf("downloads = %v, %v", earlier, err)
	}
	s.Close()

	other := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(other, []byte(strings.Repeat("these are not prices\n", 100)), 0o644)
	if _, err := openStore(other); err == nil || !strings.Contains(err.Error(), "as a prices database") {
		t.Errorf("opening a text file: %v", err)
	}
}

// command runs the whole program against the fake web and returns the path
// of its database.
func command(t *testing.T, w *web, args ...string) (code int, path, stdout, stderr string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "prices.db")
	var stdoutB, stderrB strings.Builder
	code = run(context.Background(), append([]string{"-db", path}, args...), &stdoutB, &stderrB,
		func(s *store, maxAge time.Duration, _ float64) *downloader {
			d := w.on(s)
			d.maxAge = maxAge
			return d
		})
	return code, path, stdoutB.String(), stderrB.String()
}

// opened is the database a command wrote.
func opened(t *testing.T, path string) *sql.DB {
	t.Helper()
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s.db
}

func TestCommandDownloadsEveryListedSymbol(t *testing.T) {
	w := newWeb(t)
	w.respond = func(ticker string, _ int) (int, string) {
		if ticker == "NEW" {
			return http.StatusNotFound, ""
		}
		return http.StatusOK, twoDays
	}
	code, path, stdout, stderr := command(t, w)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "Downloading 8 tickers into "+path) {
		t.Errorf("stderr = %q", stderr)
	}
	want := "7 saved, 0 already up to date, 1 without data, 0 failed\nTo list the tickers that weren't saved: sqlite3 " + path +
		" \"SELECT ticker, status, detail FROM downloads WHERE status <> 'saved'\"\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	db := opened(t, path)
	wantRows(t, db, `SELECT * FROM symbols WHERE ticker IN ('BRK-B', 'QQQ')`,
		"BRK-B|BRK.B|Berkshire Hathaway Inc. New Common Stock|NYSE|0",
		"QQQ|QQQ|Invesco QQQ Trust, Series 1|NASDAQ|1")
	wantRows(t, db, `SELECT count(*) FROM symbols`, "8")
	wantRows(t, db, `SELECT count(DISTINCT ticker), count(*) FROM prices`, "7|14")
	wantRows(t, db, `SELECT ticker, status FROM downloads WHERE status <> 'saved'`, "NEW|no data")
}

func TestCommandWithASymbolsFileAndALimit(t *testing.T) {
	w := newWeb(t)
	tickers := filepath.Join(t.TempDir(), "tickers.txt")
	os.WriteFile(tickers, []byte("IBM\nVTI\n^GSPC\n"), 0o644)
	code, path, stdout, stderr := command(t, w, "-symbols", tickers, "-limit", "2", "-max-age", "0")
	if code != 0 || stdout != "2 saved, 0 already up to date, 0 without data, 0 failed\n" {
		t.Fatalf("exit %d: %q %q", code, stdout, stderr)
	}
	if w.requests["IBM"] != 1 || w.requests["VTI"] != 1 || w.requests["^GSPC"] != 0 {
		t.Errorf("requests: %v", w.requests)
	}
	// The directory wasn't downloaded.
	wantRows(t, opened(t, path), `SELECT count(*) FROM symbols`, "0")
}

func TestCommandErrors(t *testing.T) {
	w := newWeb(t)
	for _, tc := range []struct {
		args    []string
		code    int
		message string
	}{
		{[]string{"-workers", "0"}, 2, "-workers must be at least 1"},
		{[]string{"-rate", "0"}, 2, "-rate above 0"},
		{[]string{"-limit", "-1"}, 2, "not negative"},
		{[]string{"IBM"}, 2, `unexpected argument "IBM"`},
		{[]string{"-nope"}, 2, "flag provided but not defined"},
		{[]string{"-symbols", "missing.txt"}, 1, "missing.txt"},
	} {
		code, path, stdout, stderr := command(t, w, tc.args...)
		if code != tc.code || stdout != "" || !strings.Contains(stderr, tc.message) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want exit %d mentioning %q", tc.args, code, stdout, stderr, tc.code, tc.message)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%v created a database: %v", tc.args, err)
		}
	}
	if w.total() != 0 {
		t.Errorf("made %d requests", w.total())
	}
	if code, _, _, stderr := command(t, w, "-h"); code != 0 || !strings.Contains(stderr, "Usage: prices [flags]") {
		t.Errorf("-h: exit %d, %q", code, stderr)
	}
}

func TestCommandWhenTheDirectoryIsDown(t *testing.T) {
	w := newWeb(t)
	w.directoryStatus = http.StatusServiceUnavailable
	code, _, stdout, stderr := command(t, w)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "could not download the symbol directory (nasdaqlisted.txt): HTTP 503") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if w.total() != 0 {
		t.Errorf("made %d price requests", w.total())
	}
}
