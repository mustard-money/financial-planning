package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Nasdaq Trader publishes every security listed on a US exchange in two
// pipe-delimited files, refreshed through each trading day.
const directoryURL = "https://www.nasdaqtrader.com/dynamic/SymDir"

// listing is one exchange-listed security.
type listing struct {
	Ticker   string // as Yahoo Finance writes it: BRK-B
	Symbol   string // as the exchange writes it: BRK.B
	Name     string
	Exchange string
	ETF      bool
}

// exchanges names the codes in the directory's Exchange column.
var exchanges = map[string]string{
	"A": "NYSE American",
	"N": "NYSE",
	"P": "NYSE Arca",
	"Z": "Cboe BZX",
	"V": "IEX",
	"M": "NYSE Chicago",
}

// tickerPattern is what a ticker must look like, which also keeps it safe to
// use as a file name.
var tickerPattern = regexp.MustCompile(`^[A-Za-z0-9^.=_-]{1,32}$`)

// listedSymbols downloads the directory of US-listed securities, sorted by
// ticker and without the exchanges' test issues.
func (d *downloader) listedSymbols(ctx context.Context) ([]listing, error) {
	var all []listing
	for _, file := range []string{"nasdaqlisted.txt", "otherlisted.txt"} {
		body, status, err := d.get(ctx, d.directoryURL+"/"+file)
		if err == nil && status != 200 {
			err = fmt.Errorf("HTTP %d", status)
		}
		if err != nil {
			return nil, fmt.Errorf("could not download the symbol directory (%s): %w", file, err)
		}
		rows, err := parseDirectory(string(body))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		for _, row := range rows {
			// The Nasdaq file calls the column Symbol; the other exchanges' file, ACT Symbol.
			l := listing{Symbol: row["Symbol"], Exchange: "NASDAQ", Name: row["Security Name"], ETF: row["ETF"] == "Y"}
			if l.Symbol == "" {
				l.Symbol, l.Exchange = row["ACT Symbol"], row["Exchange"]
				if name, ok := exchanges[l.Exchange]; ok {
					l.Exchange = name
				}
			}
			l.Ticker = yahooTicker(l.Symbol)
			// The ticker becomes a file name, so it must look like one.
			if row["Test Issue"] != "N" || !tickerPattern.MatchString(l.Ticker) {
				continue
			}
			all = append(all, l)
		}
	}
	slices.SortFunc(all, func(a, b listing) int { return strings.Compare(a.Ticker, b.Ticker) })
	return slices.CompactFunc(all, func(a, b listing) bool { return a.Ticker == b.Ticker }), nil
}

// parseDirectory reads a pipe-delimited directory file into one map per row,
// keyed by the header. The file ends with a "File Creation Time" line.
func parseDirectory(body string) ([]map[string]string, error) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	header := strings.Split(lines[0], "|")
	if !slices.Contains(header, "Security Name") || !slices.Contains(header, "Test Issue") {
		return nil, fmt.Errorf("unexpected symbol directory format")
	}
	var rows []map[string]string
	for _, line := range lines[1:] {
		fields := strings.Split(line, "|")
		if len(fields) != len(header) || strings.HasPrefix(line, "File Creation Time") {
			continue
		}
		row := make(map[string]string, len(header))
		for i, name := range header {
			row[name] = strings.TrimSpace(fields[i])
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// suffixes maps the exchanges' suffix for a kind of security to Yahoo's.
var suffixes = map[string]string{
	"U": "UN", // units
	"W": "WT", // warrants
	"R": "RI", // rights
	"V": "WI", // when issued
}

// yahooTicker converts an exchange symbol to Yahoo Finance's notation:
// share classes BRK.B -> BRK-B, preferred shares BAC$B -> BAC-PB, and units,
// warrants and rights AAC.U -> AAC-UN.
func yahooTicker(symbol string) string {
	if root, series, ok := strings.Cut(symbol, "$"); ok {
		return root + "-P" + strings.ReplaceAll(series, ".", "-")
	}
	root, suffix, ok := strings.Cut(symbol, ".")
	if !ok {
		return symbol
	}
	if mapped, known := suffixes[suffix]; known {
		suffix = mapped
	}
	return root + "-" + strings.ReplaceAll(suffix, ".", "-")
}

// readTickers reads a file of tickers in Yahoo's notation, one per line.
// Blank lines and lines starting with # are skipped.
func readTickers(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var tickers []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		ticker := strings.ToUpper(strings.TrimSpace(scanner.Text()))
		if ticker == "" || strings.HasPrefix(ticker, "#") {
			continue
		}
		if !tickerPattern.MatchString(ticker) {
			return nil, fmt.Errorf("%s: %q is not a ticker", path, ticker)
		}
		if !slices.Contains(tickers, ticker) {
			tickers = append(tickers, ticker)
		}
	}
	return tickers, scanner.Err()
}
