// Command backtest runs a plan of income, expenses, securities and investment
// real estate, or a Mustard scenario, against market history.
//
//	backtest run plan.json          one run over a stretch of history
//	backtest rolling plan.json      every stretch of history of one length
//	backtest coverage plan.json     which months the plan's market data covers
//	backtest scenario request.json  a user's scenario over every stretch of history
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mustard-money/financial-planning/backtest"
	"github.com/mustard-money/financial-planning/calendar"
	"github.com/mustard-money/financial-planning/market"
	"github.com/mustard-money/financial-planning/plan"
	"github.com/mustard-money/financial-planning/report"
	"github.com/mustard-money/financial-planning/scenario"
)

const usage = `Backtest a plan of income, expenses, securities and investment real estate
against market history.

Usage:
  backtest run [flags] plan.json        run the plan over one stretch of history
  backtest rolling [flags] plan.json    run the plan over every stretch of history of one length
  backtest coverage [flags] plan.json   show which months the plan's market data covers
  backtest scenario [flags] request.json
                                        run a user's scenario, with its items, over every
                                        stretch of history it fits in ("-" reads the request
                                        from standard input)

"backtest <command> -h" lists a command's flags. The plan's and the request's formats are
in the README.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// options are the flags of all commands; each command registers the ones it has.
type options struct {
	command  string
	plan     string // the plan file, or the request file for a scenario
	db       string // the prices database; empty for none
	cacheDir string
	offline  bool
	refresh  bool
	json     bool
	csv      string
	start    *calendar.Month
	end      *calendar.Month
	years    int
	step     int
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err == nil {
		err = execute(opts, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	return 0
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		fmt.Fprint(stderr, usage)
		if len(args) == 0 {
			return options{}, errors.New("a command is required: run, rolling, coverage or scenario")
		}
		return options{}, flag.ErrHelp
	}
	opts := options{command: args[0], step: 1}
	flags := flag.NewFlagSet("backtest "+opts.command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	input, db := "plan", ""
	if opts.command == "scenario" {
		input, db = "request", filepath.Join("data", "prices.db")
	}
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: backtest %s [flags] %s.json\n", opts.command, input)
		flags.PrintDefaults()
	}
	month := func(into **calendar.Month) func(string) error {
		return func(s string) error {
			m, err := calendar.ParseMonth(s)
			*into = &m
			return err
		}
	}

	switch opts.command {
	case "run":
		flags.Func("start", "first month, `YYYY-MM` (default: the earliest the data allows)", month(&opts.start))
		flags.Func("end", "last month, `YYYY-MM` (default: the latest the data allows)", month(&opts.end))
		flags.IntVar(&opts.years, "years", 0, "how many years to run, instead of -end")
	case "rolling":
		flags.IntVar(&opts.years, "years", 0, "length of each period, in years (required)")
		flags.IntVar(&opts.step, "step", 1, "`months` between starts")
	case "coverage", "scenario":
	default:
		return opts, fmt.Errorf("unknown command %q: expected run, rolling, coverage or scenario", opts.command)
	}
	if opts.command != "coverage" {
		flags.BoolVar(&opts.json, "json", false, "print the result as JSON instead of text")
	}
	if opts.command == "run" || opts.command == "rolling" {
		flags.StringVar(&opts.csv, "csv", "", "also write the result as CSV to this `file`")
	}
	flags.StringVar(&opts.db, "db", db, "the prices command's SQLite `file`, for db: series")
	flags.StringVar(&opts.cacheDir, "cache-dir", defaultCacheDir(), "where downloaded market data is kept")
	flags.BoolVar(&opts.offline, "offline", false, "use only market data that is already downloaded")
	flags.BoolVar(&opts.refresh, "refresh", false, "download market data again even if it is recent")

	// The flag package stops at the first argument that isn't a flag; keep
	// going so that flags may come after the plan as well as before it.
	var plans []string
	for rest := args[1:]; ; {
		if err := flags.Parse(rest); err != nil {
			return opts, err
		}
		if flags.NArg() == 0 {
			break
		}
		plans = append(plans, flags.Arg(0))
		rest = flags.Args()[1:]
	}
	if len(plans) != 1 {
		return opts, fmt.Errorf("expected one %s file", input)
	}
	opts.plan = plans[0]

	switch {
	case opts.offline && opts.refresh:
		return opts, errors.New("-offline and -refresh can't be used together")
	case opts.end != nil && opts.years != 0:
		return opts, errors.New("-end and -years can't be used together")
	case opts.years < 0, opts.command == "rolling" && opts.years == 0:
		return opts, errors.New("-years must be at least 1")
	case opts.step < 1:
		return opts, errors.New("-step must be at least 1")
	}
	return opts, nil
}

func defaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "mustard", "market-data")
}

func execute(opts options, stdout io.Writer) error {
	loader := &market.Loader{
		CacheDir: opts.cacheDir,
		CSVDir:   filepath.Dir(opts.plan),
		Offline:  opts.offline,
		Refresh:  opts.refresh,
	}
	if opts.db != "" {
		prices, err := market.OpenPricesDB(opts.db)
		if err != nil {
			return err
		}
		defer prices.Close()
		loader.Prices = prices
	}
	if opts.command == "scenario" {
		return executeScenario(opts, loader, stdout)
	}

	p, err := plan.Load(opts.plan)
	if err != nil {
		return err
	}
	data, err := loader.LoadAll(p.SeriesIDs())
	if err != nil {
		return err
	}

	var text string
	var result any
	switch opts.command {
	case "coverage":
		text, err = report.CoverageText(p, data)
		if err != nil {
			return err
		}
	case "run":
		first, last, err := data.Window(p.SeriesIDs())
		if err != nil {
			return err
		}
		start, end := first, last
		if opts.start != nil {
			start = *opts.start
		}
		if opts.years > 0 {
			end = start.Add(opts.years*12 - 1)
		} else if opts.end != nil {
			end = *opts.end
		}
		if end < start {
			return fmt.Errorf("the backtest would end (%s) before it starts (%s)", end, start)
		}
		run, err := backtest.Simulate(p, data, start, end.Sub(start)+1)
		if err != nil {
			return err
		}
		summary := backtest.Summarize(run)
		if err := writeCSV(opts.csv, func(w io.Writer) error { return report.WriteRunCSV(w, run) }); err != nil {
			return err
		}
		text, result = report.RunText(run, summary), report.RunJSON(run, summary)
	case "rolling":
		rolling, err := backtest.Roll(p, data, opts.years*12, opts.step)
		if err != nil {
			return err
		}
		if err := writeCSV(opts.csv, func(w io.Writer) error { return report.WriteRollingCSV(w, rolling) }); err != nil {
			return err
		}
		text, result = report.RollingText(rolling), report.RollingJSON(rolling)
	}

	return write(stdout, opts.json, text, result)
}

// executeScenario backtests the scenario in a request file.
func executeScenario(opts options, loader *market.Loader, stdout io.Writer) error {
	var data []byte
	var err error
	if opts.plan == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(opts.plan)
	}
	if err != nil {
		return fmt.Errorf("cannot read the request: %w", err)
	}
	req, err := scenario.Parse(data)
	if err != nil {
		return err
	}
	result, err := scenario.Backtest(req, loader, time.Now())
	if err != nil {
		return err
	}
	return write(stdout, opts.json, report.ScenarioText(result), result)
}

// write prints a result as text, or as JSON when asked.
func write(stdout io.Writer, asJSON bool, text string, result any) error {
	if asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false) // keep "S&P" readable
		return encoder.Encode(result)
	}
	_, err := io.WriteString(stdout, text)
	return err
}

// writeCSV writes to the file at path, if one was asked for.
func writeCSV(path string, write func(io.Writer) error) error {
	if path == "" {
		return nil
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(file); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
