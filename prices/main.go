// Command prices downloads the daily closing price of every US-listed
// security from Yahoo Finance, as far back as Yahoo has it, into a SQLite
// database.
//
//	prices                       every symbol on a US exchange
//	prices -symbols tickers.txt  the tickers in a file
//
// A run can be interrupted and started again: each ticker is stored as it
// completes, and tickers downloaded recently are skipped.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, newDownloader))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, newDownloader func(s *store, maxAge time.Duration, perSecond float64) *downloader) int {
	flags := flag.NewFlagSet("prices", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, "Download the daily closing price of every US-listed security from Yahoo Finance into a SQLite database.\n\nUsage: prices [flags]\n")
		flags.PrintDefaults()
	}
	path := flags.String("db", filepath.Join("data", "prices.db"), "SQLite `file` the prices are saved in")
	symbols := flags.String("symbols", "", "download the tickers in this `file` (one per line, as Yahoo writes them) instead of every listed symbol")
	limit := flags.Int("limit", 0, "stop after this many tickers, for a trial run (0 means all)")
	maxAge := flags.Duration("max-age", 20*time.Hour, "skip tickers downloaded more recently than this (0 downloads everything again)")
	workers := flags.Int("workers", 4, "downloads to run at once")
	rate := flags.Float64("rate", 4, "most requests to make per second")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	switch {
	case flags.NArg() > 0:
		fmt.Fprintf(stderr, "error: unexpected argument %q\n", flags.Arg(0))
		return 2
	case *workers < 1 || *rate <= 0 || *limit < 0 || *maxAge < 0:
		fmt.Fprintln(stderr, "error: -workers must be at least 1, -rate above 0, and -limit and -max-age not negative")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	var tickers []string
	if *symbols != "" {
		var err error
		if tickers, err = readTickers(*symbols); err != nil {
			return fail(err)
		}
	}
	s, err := openStore(*path)
	if err != nil {
		return fail(err)
	}
	defer s.Close()
	d := newDownloader(s, *maxAge, *rate)
	if *symbols == "" {
		listings, err := d.listedSymbols(ctx)
		if err == nil {
			err = s.saveListings(ctx, listings)
		}
		if err != nil {
			return fail(err)
		}
		for _, l := range listings {
			tickers = append(tickers, l.Ticker)
		}
	}
	if *limit > 0 && *limit < len(tickers) {
		tickers = tickers[:*limit]
	}
	fmt.Fprintf(stderr, "Downloading %d tickers into %s\n", len(tickers), *path)

	counts, runErr := d.run(ctx, tickers, *workers, stderr)

	fmt.Fprintf(stdout, "%d saved, %d already up to date, %d without data, %d failed", counts.Saved, counts.Fresh, counts.NoData, counts.Failed)
	if counts.Pending > 0 {
		fmt.Fprintf(stdout, ", %d not reached", counts.Pending)
	}
	fmt.Fprintln(stdout)
	if counts.NoData+counts.Failed > 0 {
		fmt.Fprintf(stdout, "To list the tickers that weren't saved: sqlite3 %s \"SELECT ticker, status, detail FROM downloads WHERE status <> 'saved'\"\n", *path)
	}
	switch {
	case ctx.Err() != nil:
		fmt.Fprintln(stderr, "Interrupted; run again to continue where this left off.")
		return 1
	case runErr != nil:
		return fail(runErr)
	}
	return 0
}
