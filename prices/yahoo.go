package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	yahooURL    = "https://query1.finance.yahoo.com"
	historyPath = "/v8/finance/chart/%s?period1=%d&period2=%d&interval=1d&events=div%%2Csplits"
	// Yahoo serves at most 100 years of daily data in one request, and
	// answers 422 to a longer span for some tickers. Its oldest daily series
	// (the S&P 500, from 1927) fits in 99.
	historyYears = 99
)

// errNoData means Yahoo has nothing for the ticker: it doesn't list it, or
// lists it without prices.
var errNoData = errors.New("no data")

// errUnavailable means Yahoo kept refusing or failing; trying later may work.
var errUnavailable = errors.New("unavailable")

// bar is one trading day.
type bar struct {
	Date     string  // YYYY-MM-DD, on the exchange's calendar
	Close    float64 // adjusted for splits
	AdjClose float64 // adjusted for splits and dividends
}

// history downloads every daily close Yahoo has for a ticker, oldest first.
// Throttling (429), server errors and network failures are retried with
// growing pauses; anything else is returned at once.
func (d *downloader) history(ctx context.Context, ticker string) ([]bar, error) {
	now := d.now()
	address := d.yahooURL + fmt.Sprintf(historyPath, url.PathEscape(ticker), now.AddDate(-historyYears, 0, 0).Unix(), now.Unix())
	pause := d.backoff
	for attempt := 1; ; attempt++ {
		if err := d.waitForTurn(ctx); err != nil {
			return nil, err
		}
		body, status, err := d.get(ctx, address)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, ctx.Err()
		case err == nil && status == http.StatusOK:
			return parseHistory(body, d.now())
		case err == nil && status == http.StatusNotFound:
			return nil, errNoData
		case err == nil && status != http.StatusTooManyRequests && status < 500:
			return nil, fmt.Errorf("HTTP %d", status)
		}
		if err == nil {
			err = fmt.Errorf("HTTP %d", status)
		}
		if attempt == d.attempts {
			return nil, fmt.Errorf("%w after %d tries: %v", errUnavailable, attempt, err)
		}
		select {
		case <-time.After(pause):
			pause *= 3
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// get fetches a URL and returns its body and status.
func (d *downloader) get(ctx context.Context, address string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, 0, err
	}
	// Yahoo answers Go's default user agent with 429.
	if strings.HasPrefix(address, d.yahooURL) {
		req.Header.Set("User-Agent", "Mozilla/5.0")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

// parseHistory reads the daily closes from Yahoo's chart API. A bar for a
// trading day still in progress holds the latest price, not a close, so it is
// left out.
func parseHistory(body []byte, now time.Time) ([]bar, error) {
	var response struct {
		Chart struct {
			Result []struct {
				Meta struct {
					GMTOffset            int64 `json:"gmtoffset"`
					CurrentTradingPeriod struct {
						Regular struct {
							Start int64 `json:"start"`
							End   int64 `json:"end"`
						} `json:"regular"`
					} `json:"currentTradingPeriod"`
				} `json:"meta"`
				Timestamp  []int64 `json:"timestamp"`
				Indicators struct {
					Quote []struct {
						Close []*float64 `json:"close"`
					} `json:"quote"`
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
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, errors.New("unexpected response from Yahoo Finance")
	}
	if response.Chart.Error != nil {
		return nil, fmt.Errorf("Yahoo Finance: %s", response.Chart.Error.Description)
	}
	if len(response.Chart.Result) == 0 {
		return nil, errors.New("unexpected response from Yahoo Finance")
	}
	result := response.Chart.Result[0]
	if len(result.Timestamp) == 0 || len(result.Indicators.Quote) == 0 {
		return nil, errNoData
	}
	closes := result.Indicators.Quote[0].Close
	// Some instruments have no separate adjusted series.
	adjusted := closes
	if len(result.Indicators.AdjClose) > 0 {
		adjusted = result.Indicators.AdjClose[0].AdjClose
	}
	session := result.Meta.CurrentTradingPeriod.Regular
	inSession := now.Unix() < session.End

	var bars []bar
	for i, stamp := range result.Timestamp {
		if i >= len(closes) || closes[i] == nil || *closes[i] <= 0 {
			continue
		}
		if inSession && stamp >= session.Start {
			continue
		}
		b := bar{
			// Bars are stamped in the morning of the exchange's day.
			Date:     time.Unix(stamp+result.Meta.GMTOffset, 0).UTC().Format(time.DateOnly),
			Close:    *closes[i],
			AdjClose: *closes[i],
		}
		if i < len(adjusted) && adjusted[i] != nil {
			b.AdjClose = *adjusted[i]
		}
		// Yahoo can repeat the latest day; the later bar is the fuller one.
		if n := len(bars); n > 0 && bars[n-1].Date == b.Date {
			bars[n-1] = b
		} else {
			bars = append(bars, b)
		}
	}
	if len(bars) == 0 {
		return nil, errNoData
	}
	return bars, nil
}
