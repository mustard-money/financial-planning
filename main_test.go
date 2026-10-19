package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mustard-money/financial-planning/calendar"
	"github.com/mustard-money/financial-planning/pricestest"
)

const testPlan = `{
	"name": "Test household",
	"asOf": "2026-10",
	"inflationIndex": "csv:cpi.csv",
	"income": %s,
	"expenses": [{"name": "Costs", "amount": %d}],
	"securities": [{"name": "Fund", "series": "csv:fund.csv", "value": 10000}],
	"realEstate": [{"name": "Rental", "value": 100000, "priceIndex": "csv:hpi.csv", "monthlyRent": 500}]
}`

var (
	// Each month: 2400 pay after tax + 500 rent - 2000 costs = 900 invested.
	saver = fmt.Sprintf(testPlan, `[{"name": "Pay", "amount": 3000, "taxRate": 0.2}]`, 2000)
	// With no pay and 5000 of costs, 10000 lasts two months.
	spender = fmt.Sprintf(testPlan, `[]`, 5000)
)

// cli is a directory holding three years of market data from 2000-01: prices
// flat, the fund up 1% a month.
type cli struct {
	t   *testing.T
	dir string
}

func newCLI(t *testing.T) *cli {
	c := &cli{t: t, dir: t.TempDir()}
	c.write("cpi.csv", seriesCSV("return", 2000, 36, "0"))
	c.write("hpi.csv", seriesCSV("level", 1999, 48, "100"))
	c.write("fund.csv", seriesCSV("return", 2000, 36, "0.01"))
	return c
}

func seriesCSV(kind string, startYear, months int, value string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "month,%s\n", kind)
	for i := range months {
		fmt.Fprintf(&b, "%d-%02d,%s\n", startYear+i/12, i%12+1, value)
	}
	return b.String()
}

// write saves a file in the directory and returns its path.
func (c *cli) write(name, content string) string {
	c.t.Helper()
	path := filepath.Join(c.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		c.t.Fatal(err)
	}
	return path
}

func (c *cli) run(args ...string) (code int, stdout, stderr string) {
	var out, errOut strings.Builder
	code = run(append(args, "-cache-dir", filepath.Join(c.dir, "cache")), &out, &errOut)
	return code, out.String(), errOut.String()
}

// ok runs a command that should succeed and returns what it printed.
func (c *cli) ok(args ...string) string {
	c.t.Helper()
	code, stdout, stderr := c.run(args...)
	if code != 0 || stderr != "" {
		c.t.Fatalf("%v exited %d: %s", args, code, stderr)
	}
	return stdout
}

func wantContains(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(text, part) {
			t.Errorf("missing %q in:\n%s", part, text)
		}
	}
}

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestCoverage(t *testing.T) {
	c := newCLI(t)
	out := c.ok("coverage", c.write("plan.json", saver))
	wantContains(t, out, "csv:fund.csv  2000-01  2002-12", "A backtest can run from 2000-01 to 2002-12 (3 years).")
}

func TestRunText(t *testing.T) {
	c := newCLI(t)
	out := c.ok("run", c.write("plan.json", saver))
	wantContains(t, out,
		"Test household\n",
		"Backtest over 2000-01 to 2002-12 (3 years): the plan's 2026-10 is treated as 2000-01",
		"Every month's spending was covered.",
		"  Unfunded shortfall        0        0                      0\n",
	)
	for _, pattern := range []string{`Net worth +110,000 +`, `Income after tax +86,400\n`, `Return a year +12\.7%, or 12\.7% after inflation\n`, `\n2002 +`} {
		if !regexp.MustCompile(pattern).MatchString(out) {
			t.Errorf("no match for %q in:\n%s", pattern, out)
		}
	}
}

func TestRunPeriodFlags(t *testing.T) {
	c := newCLI(t)
	plan := c.write("plan.json", saver)
	// Flags may come before or after the plan, with one dash or two.
	wantContains(t, c.ok("run", plan, "--start", "2001-01", "--years", "1"), "Backtest over 2001-01 to 2001-12 (1 year)")
	wantContains(t, c.ok("run", "-start=2001-01", "-years=1", plan), "Backtest over 2001-01 to 2001-12 (1 year)")
	wantContains(t, c.ok("run", plan, "-end", "2000-06"), "Backtest over 2000-01 to 2000-06 (6 months)")
	wantContains(t, c.ok("run", "-start", "2002-12", plan, "-end", "2002-12"), "Backtest over 2002-12 to 2002-12 (1 month)")
}

func TestRunJSONAndCSV(t *testing.T) {
	c := newCLI(t)
	csvPath := filepath.Join(c.dir, "out.csv")
	out := c.ok("run", c.write("plan.json", saver), "-years", "1", "-json", "-csv", csvPath)

	var result struct {
		Name    string
		Summary struct {
			Months         int
			Covered        bool
			ShortfallMonth *string
		}
		Holdings []string
		Months   []struct {
			Month       string
			Contributed float64
			Holdings    []float64
		}
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Name != "Test household" || result.Summary.Months != 12 || !result.Summary.Covered || result.Summary.ShortfallMonth != nil {
		t.Errorf("summary: %+v", result)
	}
	if len(result.Months) != 12 || result.Months[0].Month != "2000-01" || result.Months[0].Contributed != 900 || result.Months[0].Holdings[0] != 11000 {
		t.Errorf("months: %+v", result.Months)
	}
	wantContains(t, out, `"shortfallMonth": null`, `"holdings": [`+"\n    \"Fund\"")

	rows := readCSV(t, csvPath)
	if len(rows) != 13 {
		t.Fatalf("%d CSV rows, want a header and 12 months", len(rows))
	}
	row := map[string]string{}
	for i, name := range rows[0] {
		row[name] = rows[1][i]
	}
	for name, want := range map[string]string{"month": "2000-01", "planMonth": "2026-10", "holding:Fund": "11000.00", "netWorth": "111000.00", "portfolioReturn": "0.010000"} {
		if row[name] != want {
			t.Errorf("CSV %s = %q, want %q", name, row[name], want)
		}
	}
}

func TestRunReportsAShortfall(t *testing.T) {
	c := newCLI(t)
	out := c.ok("run", c.write("plan.json", spender))
	wantContains(t, out, "The securities ran out in 2000-03: spending went uncovered, by as much as ")
}

func TestRolling(t *testing.T) {
	c := newCLI(t)
	plan := c.write("plan.json", saver)
	out := c.ok("rolling", plan, "-years", "1", "-step", "12")
	wantContains(t, out,
		"Every period of 1 year starting from 2000-01 to 2002-01 (3 starts, the last ending 2002-12)",
		"Every month's spending was covered in 3 of 3 periods (100.0%).",
		"  Median ",
	)

	csvPath := filepath.Join(c.dir, "periods.csv")
	var result struct {
		Months      int
		SuccessRate float64
		Periods     []struct{ Start string }
	}
	if err := json.Unmarshal([]byte(c.ok("rolling", plan, "-years", "2", "-json", "-csv", csvPath)), &result); err != nil {
		t.Fatal(err)
	}
	if result.Months != 24 || result.SuccessRate != 1 || len(result.Periods) != 13 || result.Periods[12].Start != "2001-01" {
		t.Errorf("got %+v", result)
	}
	if rows := readCSV(t, csvPath); len(rows) != 14 || rows[1][0] != "2000-01" || rows[1][2] != "true" {
		t.Errorf("CSV rows: %v", rows)
	}
}

func TestRollingNamesThePeriodsThatRanShort(t *testing.T) {
	c := newCLI(t)
	out := c.ok("rolling", c.write("plan.json", spender), "-years", "1")
	wantContains(t, out,
		"Every month's spending was covered in 0 of 25 periods (0.0%).",
		"The securities ran out in periods starting 2000-01 to 2002-01.",
	)
}

func TestErrorsExitWith2AndAMessage(t *testing.T) {
	c := newCLI(t)
	plan := c.write("plan.json", saver)
	remote := c.write("remote.json", strings.Replace(saver, "csv:cpi.csv", "fred:CPIAUCSL", 1))
	typo := c.write("typo.json", strings.Replace(saver, `"income"`, `"incomes"`, 1))
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"run", plan, "-start", "1990-01"}, "error: the market data covers 2000-01 to 2002-12, not 1990-01 to 2002-12"},
		{[]string{"run", plan, "-start", "2001-01", "-end", "2000-01"}, "would end (2000-01) before it starts (2001-01)"},
		{[]string{"run", typo}, `error: plan: unknown field "incomes"`},
		{[]string{"run", filepath.Join(c.dir, "missing.json")}, "error: cannot read the plan"},
		{[]string{"run", remote, "-offline"}, "error: fred:CPIAUCSL is not in the cache"},
		{[]string{"rolling", plan, "-years", "4"}, "error: no 48-month period fits"},
		{[]string{"rolling", plan}, "error: -years must be at least 1"},
		{[]string{"rolling", plan, "-years", "1", "-step", "0"}, "error: -step must be at least 1"},
		{[]string{"run", plan, "-end", "2000-06", "-years", "1"}, "error: -end and -years can't be used together"},
		{[]string{"run", plan, "-offline", "-refresh"}, "error: -offline and -refresh can't be used together"},
		{[]string{"run", plan, "-start", "June"}, "expected a month like 2001-06"},
		{[]string{"run"}, "error: expected one plan file"},
		{[]string{"run", plan, plan}, "error: expected one plan file"},
		{[]string{"coverage", plan, "-json"}, "flag provided but not defined: -json"},
		{[]string{"simulate", plan}, `error: unknown command "simulate"`},
	} {
		code, stdout, stderr := c.run(tc.args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, tc.message) {
			t.Errorf("%v exited %d printing %q; stderr %q should mention %q", tc.args[:1], code, stdout, stderr, tc.message)
		}
	}
}

func TestUsage(t *testing.T) {
	var out, errOut strings.Builder
	if code := run(nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "Usage:") {
		t.Errorf("no arguments: exit %d, %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"-h"}, &out, &errOut); code != 0 || !strings.Contains(errOut.String(), "backtest rolling [flags] plan.json") {
		t.Errorf("-h: exit %d, %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"run", "-h"}, &out, &errOut); code != 0 || !strings.Contains(errOut.String(), "-years") {
		t.Errorf("run -h: exit %d, %q", code, errOut.String())
	}
}

const scenarioRequest = `{
	"today": "2026-10-04", "taxRate": 0.25,
	"scenario": {"id": "s1", "name": "Retire early", "itemIds": ["pay", "rent", "fund", "cash"], "events": [],
		"parameters": {"years": %d, "returnRate": 0.07, "inflation": 0.03, "incomeGrowth": 0.03, "incomeAdjustment": 0, "expenseAdjustment": 0}},
	"items": [
		{"id": "pay", "category": "income", "name": "Pay", "amount": 4000, "frequency": "monthly"},
		{"id": "rent", "category": "expenses", "name": "Rent", "amount": 2000, "frequency": "monthly"},
		{"id": "fund", "category": "investments", "name": "S&P 500 fund", "amount": 100000, "account": "taxable", "ticker": "%s"},
		{"id": "cash", "category": "investments", "name": "Savings", "amount": 20000, "account": "bank"}
	]
}`

// scenarioCLI is a directory with a prices database of flat index funds from
// 2000-01 and one stock, and cached inflation and Treasury bill data, so the
// scenario command runs without the network.
func scenarioCLI(t *testing.T) (c *cli, db string) {
	c = &cli{t: t, dir: t.TempDir()}
	from, months := calendar.NewMonth(2000, 1), 320
	fund := pricestest.Flat(from, months, 0)
	fund.Fund = true
	stock := pricestest.Flat(from, months, 0.01)
	stock.Stock = true
	db = filepath.Join(c.dir, "prices.db")
	pricestest.Write(t, db, time.Now(), map[string]pricestest.Ticker{"SPY": fund, "BIL": fund, "ACME": stock})

	var fred strings.Builder
	fred.WriteString("observation_date,X\n")
	for year := 1990; year <= 2026; year++ {
		for month := 1; month <= 12; month++ {
			fmt.Fprintf(&fred, "%d-%02d-01,100\n", year, month)
		}
	}
	entry, _ := json.Marshal(map[string]any{"fetchedAt": time.Now().UTC(), "url": "", "body": fred.String()})
	for _, series := range []string{"CPIAUCSL", "TB3MS"} {
		c.write(filepath.Join("cache", "fred", series+".json"), string(entry))
	}
	return c, db
}

func TestScenario(t *testing.T) {
	c, db := scenarioCLI(t)
	out := c.ok("scenario", c.write("request.json", fmt.Sprintf(scenarioRequest, 20, "SPY")), "-db", db, "-offline")
	wantContains(t, out,
		"Retire early\n",
		"A 20-year scenario from 2026-10, run over every 20-year period starting from 2000-02 to ",
		"  S&P 500 fund  100,000  SPY  the ticker it names\n",
		"  Savings        20,000  BIL  stands in for cash (1-3 month Treasury bills); earlier history from Treasury bill yields\n",
		"The investments never ran out in ",
		// 3000 after tax less 2000 of rent is 1000 a month: 240,000 on top of 120,000.
		"Investments after 20 years, in today's dollars (they start at 120,000)\n  Worst            360,000  starting 2000-02\n",
		"\n20    360,000  360,000  360,000  360,000  360,000\n",
		"the holding with the shortest history is S&P 500 fund (SPY).",
	)
	if strings.Contains(out, "Notes") {
		t.Errorf("unexpected notes:\n%s", out)
	}
}

func TestScenarioJSONAndNotes(t *testing.T) {
	c, db := scenarioCLI(t)
	out := c.ok("scenario", "-json", "-offline", "-db", db, c.write("request.json", fmt.Sprintf(scenarioRequest, 20, "ACME")))
	var result struct {
		ScenarioID string
		Periods    int
		Holdings   []struct{ Ticker, Source string }
		Warnings   []string
		Worst      struct {
			Rows []struct{ Investments float64 }
		}
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.ScenarioID != "s1" || result.Periods < 80 || result.Holdings[0].Ticker != "ACME" || result.Holdings[0].Source != "ticker" || len(result.Worst.Rows) != 20 {
		t.Errorf("result: %+v", result)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "ACME is one company's stock") {
		t.Errorf("warnings: %q", result.Warnings)
	}
	wantContains(t, out, `"tracks": "US large-company stocks (S&P 500)"`)

	text := c.ok("scenario", "-offline", "-db", db, c.write("request.json", fmt.Sprintf(scenarioRequest, 20, "ACME")))
	wantContains(t, text, "\nNotes\n  - S&P 500 fund: ACME is one company's stock")
}

func TestScenarioLongerThanHistory(t *testing.T) {
	c, db := scenarioCLI(t)
	out := c.ok("scenario", c.write("request.json", fmt.Sprintf(scenarioRequest, 40, "SPY")), "-db", db, "-offline")
	wantContains(t, out, "A 40-year scenario from 2026-10\n", "The scenario runs 40 years, but the price history for its holdings covers only 26.7 (2000-02 to 2026-09).")
}

func TestScenarioErrors(t *testing.T) {
	c, db := scenarioCLI(t)
	request := c.write("request.json", fmt.Sprintf(scenarioRequest, 20, "SPY"))
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"scenario", request, "-db", filepath.Join(c.dir, "missing.db")}, "error: there is no prices database at"},
		{[]string{"scenario", request, "-db", request}, "is not a prices database"},
		{[]string{"scenario", filepath.Join(c.dir, "nope.json"), "-db", db}, "error: cannot read the request"},
		{[]string{"scenario", c.write("bad.json", `{"scenario": {"parameters": {"years": 99}}}`), "-db", db}, "error: request: scenario.parameters.years must be between 1 and 60"},
		{[]string{"scenario", "-db", db}, "error: expected one request file"},
		{[]string{"scenario", request, "-db", db, "-csv", "out.csv"}, "flag provided but not defined: -csv"},
	} {
		code, stdout, stderr := c.run(tc.args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, tc.message) {
			t.Errorf("%v exited %d printing %q; stderr %q should mention %q", tc.args[1:], code, stdout, stderr, tc.message)
		}
	}
}

func TestPlansCanUseThePricesDatabase(t *testing.T) {
	c, db := scenarioCLI(t)
	c.write("cpi.csv", seriesCSV("return", 2000, 320, "0"))
	plan := c.write("plan.json", `{"asOf": "2026-10", "inflationIndex": "csv:cpi.csv",
		"securities": [{"name": "Stock", "series": "db:ACME", "value": 1000}]}`)
	wantContains(t, c.ok("coverage", plan, "-db", db), "db:ACME      2000-02  2026-09")
	wantContains(t, c.ok("run", plan, "-db", db, "-years", "1"), "Return a year      12.7%")

	code, _, stderr := c.run("run", plan)
	if code != 2 || !strings.Contains(stderr, "db:ACME needs a prices database (-db)") {
		t.Errorf("without -db: exit %d, %q", code, stderr)
	}
}
