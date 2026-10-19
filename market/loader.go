package market

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mustard-money/financial-planning/calendar"
)

// Series ids:
//
//	yahoo:VTI          a ticker's dividend- and split-adjusted monthly closes
//	fred:CPIAUCSL      a FRED index (prices, house prices, rents)
//	fred-yield:TB3MS   a FRED interest rate in percent a year, earned monthly
//	cash               short for fred-yield:TB3MS (3-month Treasury bills)
//	csv:funds/x.csv    a local file, relative to the plan
//	db:VTI             a ticker's month-end closes in the prices database
//	a|b                series a, extended back in time with b where a has no data
const (
	Cash       = "cash"
	cashSeries = "fred-yield:TB3MS"

	fredURL = "https://fred.stlouisfed.org/graph/fredgraph.csv?id=%s"
	// period1 is 1900, so the whole history comes back.
	yahooURL = "https://query1.finance.yahoo.com/v8/finance/chart/%s?period1=-2208988800&period2=%d&interval=1mo&events=div%%2Csplits"

	defaultMaxAge = 7 * 24 * time.Hour
)

var remoteName = regexp.MustCompile(`^[A-Za-z0-9^.=_-]{1,32}$`)

// Loader loads series by id, downloading and caching the remote ones.
type Loader struct {
	CacheDir string // where downloads are kept
	CSVDir   string // what csv: paths are relative to
	// Prices is where db: series are read from; nil means there are none.
	Prices  *PricesDB
	Offline bool // use only what is cached, however old
	Refresh bool // download even if the cached copy is recent
	// MaxAge is how long a download is reused; zero means a week.
	MaxAge time.Duration
	// Now and Fetch are replaced in tests; zero values mean the clock and HTTP.
	Now   time.Time
	Fetch func(url string) (string, error)
}

// Load returns the series with the given id.
func (l *Loader) Load(id string) (*Series, error) {
	parts := strings.Split(id, "|")
	series, err := l.loadOne(parts[0])
	if err != nil {
		return nil, err
	}
	for _, fallback := range parts[1:] {
		earlier, err := l.loadOne(fallback)
		if err != nil {
			return nil, err
		}
		if series, err = series.WithEarlier(earlier); err != nil {
			return nil, err
		}
	}
	named := *series
	named.ID = id
	return &named, nil
}

// LoadAll loads every one of the series.
func (l *Loader) LoadAll(ids []string) (Data, error) {
	data := Data{}
	for _, id := range ids {
		series, err := l.Load(id)
		if err != nil {
			return nil, err
		}
		data[id] = series
	}
	return data, nil
}

func (l *Loader) loadOne(id string) (*Series, error) {
	full := id
	if id == Cash {
		full = cashSeries
	}
	kind, name, _ := strings.Cut(full, ":")
	if kind == "csv" && name != "" {
		path := filepath.Join(l.CSVDir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: cannot read %s", id, path)
		}
		return ParseSeriesCSV(id, string(body))
	}
	if !slices.Contains([]string{"yahoo", "fred", "fred-yield", "db"}, kind) || !remoteName.MatchString(name) {
		return nil, fmt.Errorf("unknown series %q: expected yahoo:TICKER, fred:ID, fred-yield:ID, db:TICKER, csv:PATH or cash", id)
	}
	if kind == "db" {
		if l.Prices == nil {
			return nil, fmt.Errorf("%s needs a prices database (-db)", id)
		}
		levels, err := l.Prices.monthEnds(name)
		if err != nil {
			return nil, err
		}
		return FromLevels(id, levels)
	}
	if kind == "yahoo" {
		levels, err := l.remote("yahoo", name, fmt.Sprintf(yahooURL, url.PathEscape(name), l.now().Unix()), parseYahooChart)
		if err != nil {
			return nil, err
		}
		return FromLevels(id, levels)
	}
	values, err := l.remote("fred", name, fmt.Sprintf(fredURL, name), parseFREDCSV)
	if err != nil {
		return nil, err
	}
	if kind == "fred" {
		return FromLevels(id, values)
	}
	return FromYields(id, values)
}

type cacheEntry struct {
	FetchedAt time.Time `json:"fetchedAt"`
	URL       string    `json:"url"`
	Body      string    `json:"body"`
}

// remote returns the parsed data for a remote series, from the cache while it
// is fresh. A download is parsed before it is cached, so an error page never
// replaces good data, and a stale copy is used if the download fails.
func (l *Loader) remote(source, name, address string, parse func(body string, fetched time.Time) (map[calendar.Month]float64, error)) (map[calendar.Month]float64, error) {
	path := filepath.Join(l.CacheDir, source, name+".json")
	cached := readCache(path)
	maxAge := l.MaxAge
	if maxAge == 0 {
		maxAge = defaultMaxAge
	}
	if cached != nil && (l.Offline || (!l.Refresh && l.now().Sub(cached.FetchedAt) <= maxAge)) {
		return parse(cached.Body, cached.FetchedAt)
	}
	if l.Offline {
		return nil, fmt.Errorf("%s:%s is not in the cache; run without -offline to download it", source, name)
	}

	fetch := l.Fetch
	if fetch == nil {
		fetch = httpGet
	}
	body, err := fetch(address)
	var values map[calendar.Month]float64
	if err == nil {
		values, err = parse(body, l.now())
	}
	if err != nil {
		if cached == nil {
			return nil, fmt.Errorf("could not download %s:%s: %w", source, name, err)
		}
		slog.Warn("could not refresh market data; using the cached copy",
			"series", source+":"+name, "error", err, "cached", cached.FetchedAt.Format(time.DateOnly))
		return parse(cached.Body, cached.FetchedAt)
	}

	entry, err := json.Marshal(cacheEntry{FetchedAt: l.now(), URL: address, Body: body})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("cannot cache market data: %w", err)
	}
	if err := os.WriteFile(path, entry, 0o644); err != nil {
		return nil, fmt.Errorf("cannot cache market data: %w", err)
	}
	return values, nil
}

func (l *Loader) now() time.Time {
	if l.Now.IsZero() {
		return time.Now().UTC()
	}
	return l.Now
}

func readCache(path string) *cacheEntry {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entry cacheEntry
	if json.Unmarshal(raw, &entry) != nil || entry.FetchedAt.IsZero() {
		return nil
	}
	return &entry
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func httpGet(address string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	// Yahoo turns away Go's default user agent (429), and FRED turns away a
	// Go client that sends any other (it resets the stream).
	if strings.Contains(req.URL.Host, "yahoo.com") {
		req.Header.Set("User-Agent", "Mozilla/5.0")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

// parseFREDCSV reads FRED's graph CSV (date, value); missing observations
// are skipped.
func parseFREDCSV(body string, _ time.Time) (map[calendar.Month]float64, error) {
	rows, err := readCSV(body)
	if err != nil || len(rows) == 0 || len(rows[0]) != 2 || !strings.Contains(strings.ToLower(rows[0][0]), "date") {
		return nil, errors.New("unexpected response from FRED")
	}
	values := map[calendar.Month]float64{}
	for _, row := range rows[1:] {
		if len(row) != 2 {
			continue
		}
		month, err := calendar.ParseMonth(row[0])
		if err != nil {
			continue
		}
		// "." marks a missing observation.
		if value, err := strconv.ParseFloat(row[1], 64); err == nil {
			values[month] = value
		}
	}
	return values, nil
}

// parseYahooChart reads month-end adjusted closes from Yahoo's chart API. The
// month being fetched is still in progress, so it is left out.
func parseYahooChart(body string, fetched time.Time) (map[calendar.Month]float64, error) {
	var response struct {
		Chart struct {
			Result []struct {
				Meta struct {
					GMTOffset int64 `json:"gmtoffset"`
				} `json:"meta"`
				Timestamp  []int64 `json:"timestamp"`
				Indicators struct {
					AdjClose []struct {
						AdjClose []*float64 `json:"adjclose"`
					} `json:"adjclose"`
				} `json:"indicators"`
			} `json:"result"`
			Error *struct {
				Description string `json:"description"`
			} `json:"error"`
		} `json:"chart"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		return nil, errors.New("unexpected response from Yahoo Finance")
	}
	if response.Chart.Error != nil {
		return nil, fmt.Errorf("Yahoo Finance: %s", response.Chart.Error.Description)
	}
	if len(response.Chart.Result) == 0 || len(response.Chart.Result[0].Indicators.AdjClose) == 0 {
		return nil, errors.New("unexpected response from Yahoo Finance")
	}
	result := response.Chart.Result[0]
	closes := result.Indicators.AdjClose[0].AdjClose
	current := calendar.MonthOf(fetched.UTC())
	levels := map[calendar.Month]float64{}
	for i, stamp := range result.Timestamp {
		if i >= len(closes) || closes[i] == nil || *closes[i] <= 0 {
			continue
		}
		// Bars are stamped at midnight in the exchange's time zone.
		month := calendar.MonthOf(time.Unix(stamp+result.Meta.GMTOffset, 0).UTC())
		if month < current {
			levels[month] = *closes[i]
		}
	}
	return levels, nil
}

// ParseSeriesCSV reads a local series: a header of month,level or
// month,return, then rows. Months are YYYY-MM (a day is ignored). Levels are
// prices or index values; returns are fractions (0.01 is +1%) and must be
// consecutive months.
func ParseSeriesCSV(id, body string) (*Series, error) {
	rows, err := readCSV(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	kind := ""
	if len(rows) > 0 && len(rows[0]) == 2 && strings.ToLower(strings.TrimSpace(rows[0][0])) == "month" {
		kind = strings.ToLower(strings.TrimSpace(rows[0][1]))
	}
	if kind != "level" && kind != "return" {
		return nil, fmt.Errorf("%s: the first line must be month,level or month,return", id)
	}
	values := map[calendar.Month]float64{}
	for i, row := range rows[1:] {
		if len(row) != 2 {
			return nil, fmt.Errorf("%s: line %d is not a month and a number", id, i+2)
		}
		month, monthErr := calendar.ParseMonth(strings.TrimSpace(row[0]))
		value, valueErr := strconv.ParseFloat(strings.TrimSpace(row[1]), 64)
		if monthErr != nil || valueErr != nil {
			return nil, fmt.Errorf("%s: line %d is not a month and a number", id, i+2)
		}
		if _, dup := values[month]; dup {
			return nil, fmt.Errorf("%s: %s appears twice", id, month)
		}
		values[month] = value
	}
	if kind == "level" {
		return FromLevels(id, values)
	}
	months := slices.Sorted(maps.Keys(values))
	if len(months) == 0 {
		return nil, fmt.Errorf("%s has no data", id)
	}
	if months[len(months)-1].Sub(months[0])+1 != len(months) {
		return nil, fmt.Errorf("%s: returns must cover consecutive months", id)
	}
	returns := make([]float64, len(months))
	for i, m := range months {
		returns[i] = values[m]
	}
	return &Series{ID: id, Start: months[0], Returns: returns}, nil
}

// readCSV reads rows of any width, skipping blank lines.
func readCSV(body string) ([][]string, error) {
	reader := csv.NewReader(strings.NewReader(body))
	reader.FieldsPerRecord = -1
	return reader.ReadAll()
}
