package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// A run gives up when this many tickers in a row could not be reached:
// Yahoo is throttling or down, and carrying on would only fail the rest.
const maxConsecutiveUnavailable = 20

// downloader saves each ticker's daily closes in the store.
type downloader struct {
	client       *http.Client
	yahooURL     string
	directoryURL string
	store        *store
	// maxAge is how recent a ticker's last download must be to be left alone.
	maxAge time.Duration
	// attempts is how many times a request is tried; backoff is the pause
	// after the first failure, tripling after each further one.
	attempts int
	backoff  time.Duration
	// turns paces requests: each one waits for a value. nil means no pacing.
	turns <-chan time.Time
	now   func() time.Time
}

func newDownloader(s *store, maxAge time.Duration, perSecond float64) *downloader {
	return &downloader{
		client:       &http.Client{Timeout: 60 * time.Second},
		yahooURL:     yahooURL,
		directoryURL: directoryURL,
		store:        s,
		maxAge:       maxAge,
		attempts:     4,
		backoff:      15 * time.Second,
		turns:        time.Tick(time.Duration(float64(time.Second) / perSecond)),
		now:          time.Now,
	}
}

func (d *downloader) waitForTurn(ctx context.Context) error {
	if d.turns == nil {
		return ctx.Err()
	}
	select {
	case <-d.turns:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// tally counts how a run's tickers ended up.
type tally struct {
	Saved   int // downloaded and stored
	Fresh   int // already downloaded recently, so left alone
	NoData  int // Yahoo has no prices for it
	Failed  int // anything else
	Pending int // not reached before the run stopped
}

// run downloads every ticker with the given number of workers, reporting
// progress as it goes. It stops early, with an error, when ctx is cancelled,
// Yahoo stops answering or the database can't be written.
//
// Each ticker is stored as it completes, along with how its download went, so
// a later run picks up where this one stopped: it skips the tickers settled
// within maxAge and does the rest.
func (d *downloader) run(ctx context.Context, tickers []string, workers int, progress io.Writer) (tally, error) {
	earlier, err := d.store.downloads(ctx)
	if err != nil {
		return tally{}, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var (
		mu          sync.Mutex
		counts      tally
		unavailable int // in a row
		done        int
	)
	queue := make(chan string)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for ticker := range queue {
				last, tried := earlier[ticker]
				status, unreachable, err := d.download(ctx, ticker, last, tried)

				mu.Lock()
				done++
				switch {
				case err != nil:
					counts.Pending++
					if ctx.Err() == nil {
						cancel(fmt.Errorf("cannot write to the database: %w", err))
					}
				case status == statusSaved:
					counts.Saved++
				case status == statusNoData:
					counts.NoData++
				case status == statusFailed:
					counts.Failed++
				default:
					counts.Fresh++
				}
				switch {
				case unreachable:
					unavailable++
					if unavailable == maxConsecutiveUnavailable {
						cancel(fmt.Errorf("Yahoo Finance stopped answering (%d tickers in a row failed); run again later to continue", unavailable))
					}
				case err == nil && status != "":
					// Any answer, even "no data", shows Yahoo is still there.
					unavailable = 0
				}
				if done%250 == 0 {
					fmt.Fprintf(progress, "%d of %d: %d saved, %d already up to date, %d without data, %d failed\n",
						done, len(tickers), counts.Saved, counts.Fresh, counts.NoData, counts.Failed)
				}
				mu.Unlock()
			}
		})
	}

	sent := 0
feed:
	for _, ticker := range tickers {
		select {
		case queue <- ticker:
			sent++
		case <-ctx.Done():
			break feed
		}
	}
	close(queue)
	wg.Wait()

	counts.Pending += len(tickers) - sent
	return counts, context.Cause(ctx)
}

// download brings one ticker up to date and records how it went. It returns
// the ticker's new status, or "" when its last download is recent enough to
// keep, and whether Yahoo could not be reached. The error is for what stops
// the whole run: it was cancelled, or the database can't be written.
func (d *downloader) download(ctx context.Context, ticker string, last download, tried bool) (status string, unreachable bool, err error) {
	// A failed download is always tried again; a ticker Yahoo had no data
	// for is settled, like a saved one, until it ages out.
	if tried && last.Status != statusFailed && d.maxAge > 0 && d.now().Sub(last.At) < d.maxAge {
		return "", false, nil
	}
	bars, err := d.history(ctx, ticker)
	switch {
	case ctx.Err() != nil:
		return "", false, ctx.Err()
	case err == nil:
		return statusSaved, false, d.store.savePrices(ctx, ticker, bars, d.now())
	case errors.Is(err, errNoData):
		return statusNoData, false, d.store.saveOutcome(ctx, ticker, statusNoData, "", d.now())
	}
	return statusFailed, errors.Is(err, errUnavailable), d.store.saveOutcome(ctx, ticker, statusFailed, err.Error(), d.now())
}
