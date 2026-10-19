package market

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mustard-money/financial-planning/calendar"
)

var now = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

const (
	fredIndex = "observation_date,X\n2000-01-01,100\n2000-02-01,.\n2000-03-01,121\n"
	fredYield = "observation_date,TB3MS\n2000-01-01,12.0\n2000-02-01,12.0\n"
	day       = 24 * time.Hour
)

type bar struct {
	stamp int64
	close *float64
}

// at is midnight US Eastern (UTC-4), as Yahoo stamps its monthly bars.
func at(year, m, d int, close float64) bar {
	return bar{time.Date(year, time.Month(m), d, 4, 0, 0, 0, time.UTC).Unix(), &close}
}

func yahooBody(bars ...bar) string {
	stamps := make([]int64, len(bars))
	closes := make([]*float64, len(bars))
	for i, b := range bars {
		stamps[i], closes[i] = b.stamp, b.close
	}
	body, _ := json.Marshal(map[string]any{"chart": map[string]any{
		"result": []any{map[string]any{
			"meta":       map[string]any{"gmtoffset": -14400},
			"timestamp":  stamps,
			"indicators": map[string]any{"adjclose": []any{map[string]any{"adjclose": closes}}},
		}},
		"error": nil,
	}})
	return string(body)
}

func TestParseFREDCSVSkipsMissingObservations(t *testing.T) {
	got, err := parseFREDCSV(fredIndex, now)
	want := map[calendar.Month]float64{month(2000, 1): 100, month(2000, 3): 121}
	if err != nil || !maps.Equal(got, want) {
		t.Errorf("got %v, %v; want %v", got, err, want)
	}
	_, err = parseFREDCSV("<html>Not found</html>", now)
	wantErr(t, err, "unexpected response from FRED")
}

func TestParseYahooChartReadsMonthEndClosesInExchangeTime(t *testing.T) {
	missing := at(2026, 8, 1, 0)
	missing.close = nil
	got, err := parseYahooChart(yahooBody(at(2026, 7, 1, 100), missing, at(2026, 9, 1, 110)), now)
	want := map[calendar.Month]float64{month(2026, 7): 100, month(2026, 9): 110}
	if err != nil || !maps.Equal(got, want) {
		t.Errorf("got %v, %v; want %v", got, err, want)
	}
}

func TestParseYahooChartLeavesOutTheMonthInProgress(t *testing.T) {
	// Fetched on October 4: October has a bar and a live quote, neither a month-end close.
	got, err := parseYahooChart(yahooBody(at(2026, 9, 1, 110), at(2026, 10, 1, 111), at(2026, 10, 3, 112)), now)
	want := map[calendar.Month]float64{month(2026, 9): 110}
	if err != nil || !maps.Equal(got, want) {
		t.Errorf("got %v, %v; want %v", got, err, want)
	}
}

func TestParseYahooChartErrors(t *testing.T) {
	_, err := parseYahooChart(`{"chart":{"result":null,"error":{"code":"Not Found","description":"No data found"}}}`, now)
	wantErr(t, err, "Yahoo Finance: No data found")
	for _, body := range []string{"<html>", `{"chart":{"result":[]}}`, `{}`} {
		_, err = parseYahooChart(body, now)
		wantErr(t, err, "unexpected response from Yahoo Finance")
	}
}

func TestParseSeriesCSV(t *testing.T) {
	levels, err := ParseSeriesCSV("csv:a", "month,level\n2000-01,100\n2000-02,110\n")
	if err != nil || levels.Start != month(2000, 2) || !nearAll(levels.Returns, []float64{0.10}) {
		t.Errorf("levels: got %+v, %v", levels, err)
	}
	returns, err := ParseSeriesCSV("csv:b", "Month, Return\n2000-01,0.01\n\n2000-02-29, -0.02\n")
	if err != nil || returns.Start != month(2000, 1) || !nearAll(returns.Returns, []float64{0.01, -0.02}) {
		t.Errorf("returns: got %+v, %v", returns, err)
	}
}

func TestParseSeriesCSVErrors(t *testing.T) {
	for body, message := range map[string]string{
		"date,price\n2000-01,1\n":    "first line must be",
		"":                           "first line must be",
		"month,level\n2000-01,abc\n": "line 2 is not a month and a number",
		"month,level\n2000-01\n":     "line 2 is not a month and a number",
		"month,return\n2000-01,0.1\n2000-03,0.1\n": "consecutive",
		"month,return\n2000-01,0.1\n2000-01,0.2\n": "2000-01 appears twice",
		"month,return\n": "no data",
	} {
		_, err := ParseSeriesCSV("csv:x", body)
		wantErr(t, err, message)
	}
}

// fakeWeb answers downloads from canned responses, by host.
type fakeWeb struct {
	t         *testing.T
	dir       string
	requests  []string
	responses map[string]string
	failure   error
}

func newFakeWeb(t *testing.T) *fakeWeb {
	return &fakeWeb{t: t, dir: t.TempDir(), responses: map[string]string{"fred.stlouisfed.org": fredIndex}}
}

func (w *fakeWeb) loader(at time.Time) *Loader {
	return &Loader{
		CacheDir: filepath.Join(w.dir, "cache"),
		CSVDir:   w.dir,
		Now:      at,
		Fetch: func(address string) (string, error) {
			w.requests = append(w.requests, address)
			if w.failure != nil {
				return "", w.failure
			}
			for host, body := range w.responses {
				if strings.Contains(address, host) {
					return body, nil
				}
			}
			w.t.Errorf("unexpected request: %s", address)
			return "", errors.New("unexpected request")
		},
	}
}

func (w *fakeWeb) mustLoad(l *Loader, id string) *Series {
	w.t.Helper()
	series, err := l.Load(id)
	if err != nil {
		w.t.Fatalf("Load(%q): %v", id, err)
	}
	return series
}

func TestLoaderDownloadsOnceAndThenUsesTheCache(t *testing.T) {
	web := newFakeWeb(t)
	series := web.mustLoad(web.loader(now), "fred:X")
	// The missing February is interpolated.
	if series.ID != "fred:X" || series.Start != month(2000, 2) || !nearAll(series.Returns, []float64{0.10, 0.10}) {
		t.Errorf("got %+v", series)
	}
	if len(web.requests) != 1 || !strings.Contains(web.requests[0], "id=X") {
		t.Errorf("requests = %v", web.requests)
	}
	web.mustLoad(web.loader(now.Add(6*day)), "fred:X")
	if len(web.requests) != 1 {
		t.Errorf("a 6-day-old copy was downloaded again: %v", web.requests)
	}
}

func TestLoaderDownloadsAgainWhenTheCacheIsOldOrOnRefresh(t *testing.T) {
	web := newFakeWeb(t)
	web.mustLoad(web.loader(now), "fred:X")
	web.mustLoad(web.loader(now.Add(8*day)), "fred:X")
	if len(web.requests) != 2 {
		t.Errorf("an 8-day-old copy made %d requests, want 2", len(web.requests))
	}
	refresh := web.loader(now.Add(8 * day))
	refresh.Refresh = true
	web.mustLoad(refresh, "fred:X")
	if len(web.requests) != 3 {
		t.Errorf("refresh made %d requests, want 3", len(web.requests))
	}
}

func TestLoaderOfflineUsesTheCacheHoweverOldAndNeverDownloads(t *testing.T) {
	web := newFakeWeb(t)
	offline := web.loader(now.Add(400 * day))
	offline.Offline = true
	_, err := offline.Load("fred:X")
	wantErr(t, err, "fred:X is not in the cache")

	web.mustLoad(web.loader(now), "fred:X")
	web.mustLoad(offline, "fred:X")
	if len(web.requests) != 1 {
		t.Errorf("made %d requests, want 1", len(web.requests))
	}
}

func TestLoaderFallsBackToTheOldCopyWhenADownloadFails(t *testing.T) {
	web := newFakeWeb(t)
	web.mustLoad(web.loader(now), "fred:X")
	later := now.Add(30 * day)

	web.failure = errors.New("network down")
	if series := web.mustLoad(web.loader(later), "fred:X"); len(series.Returns) != 2 {
		t.Errorf("after a network failure: %+v", series)
	}
	web.failure = nil
	web.responses["fred.stlouisfed.org"] = "<html>Service unavailable</html>"
	if series := web.mustLoad(web.loader(later), "fred:X"); len(series.Returns) != 2 {
		t.Errorf("after an error page: %+v", series)
	}
	// The error page didn't replace the good copy.
	offline := web.loader(later)
	offline.Offline = true
	if series := web.mustLoad(offline, "fred:X"); len(series.Returns) != 2 {
		t.Errorf("the cache now holds: %+v", series)
	}
}

func TestLoaderFailedDownloadWithNothingCachedIsAnError(t *testing.T) {
	web := newFakeWeb(t)
	web.failure = errors.New("network down")
	_, err := web.loader(now).Load("fred:X")
	wantErr(t, err, "could not download fred:X: network down")
}

func TestLoaderCashIsTreasuryBillYields(t *testing.T) {
	web := newFakeWeb(t)
	web.responses["fred.stlouisfed.org"] = fredYield
	series := web.mustLoad(web.loader(now), Cash)
	if !strings.Contains(web.requests[0], "id=TB3MS") {
		t.Errorf("requested %s", web.requests[0])
	}
	if series.ID != "cash" || series.Start != month(2000, 1) || len(series.Returns) != 2 {
		t.Errorf("got %+v", series)
	}
}

func TestLoaderExtendsAYahooSeriesBackWithAProxy(t *testing.T) {
	web := newFakeWeb(t)
	proxy := "month,return\n2026-05,0.5\n2026-06,0.5\n2026-07,0.5\n2026-08,0.9\n"
	if err := os.WriteFile(filepath.Join(web.dir, "old.csv"), []byte(proxy), 0o644); err != nil {
		t.Fatal(err)
	}
	web.responses["query1.finance.yahoo.com"] = yahooBody(at(2026, 7, 1, 100), at(2026, 8, 1, 110), at(2026, 9, 1, 99), at(2026, 10, 1, 1))

	series := web.mustLoad(web.loader(now), "yahoo:BRK-B|csv:old.csv")
	if !strings.Contains(web.requests[0], "/chart/BRK-B?") {
		t.Errorf("requested %s", web.requests[0])
	}
	if series.ID != "yahoo:BRK-B|csv:old.csv" || series.Start != month(2026, 5) || series.End() != month(2026, 9) {
		t.Errorf("got %s covering %v to %v", series.ID, series.Start, series.End())
	}
	if !nearAll(series.Returns, []float64{0.5, 0.5, 0.5, 0.10, -0.10}) || series.LastLevel != 99 {
		t.Errorf("returns %v, last level %v", series.Returns, series.LastLevel)
	}
}

func TestLoaderLoadAll(t *testing.T) {
	web := newFakeWeb(t)
	data, err := web.loader(now).LoadAll([]string{"fred:X", "fred:Y"})
	if err != nil || len(data) != 2 || data["fred:Y"].ID != "fred:Y" {
		t.Errorf("got %v, %v", data, err)
	}
}

func TestLoaderUnknownSeries(t *testing.T) {
	web := newFakeWeb(t)
	for _, id := range []string{"VTI", "yahoo:", "yahoo:../etc", "stooq:vti", "csv:"} {
		_, err := web.loader(now).Load(id)
		wantErr(t, err, "unknown series")
	}
	_, err := web.loader(now).Load("csv:missing.csv")
	wantErr(t, err, "cannot read")
	if len(web.requests) != 0 {
		t.Errorf("made requests: %v", web.requests)
	}
}
