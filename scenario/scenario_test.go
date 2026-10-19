package scenario

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mustard-money/financial-planning/calendar"
	"github.com/mustard-money/financial-planning/market"
	"github.com/mustard-money/financial-planning/plan"
	"github.com/mustard-money/financial-planning/pricestest"
)

var today = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func ptr[T any](v T) *T { return &v }

// fakePrices has the listed tickers; the ones marked true are stocks.
type fakePrices map[string]bool

func (f fakePrices) Has(ticker string) (bool, error) {
	_, ok := f[ticker]
	return ok, nil
}

func (f fakePrices) IsStock(ticker string) (bool, error) { return f[ticker], nil }

// etfs is a database with every proxy ETF and none of the older funds.
func etfs(extra ...string) fakePrices {
	prices := fakePrices{}
	for _, t := range []string{"SPY", "BIL", "SHV", "TIP", "MUB", "IEF", "AGG", "EEM", "EFA", "VNQ", "GLD", "AOR", "QQQ", "IWM", "VTI"} {
		prices[t] = false
	}
	for _, t := range extra {
		prices[t] = false
	}
	return prices
}

// request parses a request for a 20-year scenario including every item, with
// the given items and events (JSON arrays).
func request(t *testing.T, items, events string) *Request {
	t.Helper()
	var ids []string
	var decoded []struct{ ID string }
	if err := json.Unmarshal([]byte(items), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, item := range decoded {
		ids = append(ids, item.ID)
	}
	idList, _ := json.Marshal(ids)
	req, err := Parse([]byte(`{
		"today": "2026-10-04", "taxRate": 0.25,
		"scenario": {"id": "s1", "name": "Test", "itemIds": ` + string(idList) + `, "events": ` + events + `,
			"parameters": {"years": 20, "returnRate": 0.07, "inflation": 0.03, "incomeGrowth": 0.0506, "incomeAdjustment": 0.1, "expenseAdjustment": -0.5}},
		"items": ` + items + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func mustBuild(t *testing.T, req *Request, prices Prices) *built {
	t.Helper()
	b, err := build(req, prices, today)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProxyForAccountAndName(t *testing.T) {
	for _, tc := range []struct{ account, name, want string }{
		{"bank", "Chase", "BIL"},
		{"bank", "S&P 500 savings", "BIL"}, // the account decides before the name
		{"cd", "12-month CD", "SHV"},
		{"rothIra", "Roth IRA", "SPY"},
		{"", "My investments", "SPY"},
		{"taxable", "Vanguard S&P 500 index", "SPY"},
		{"taxable", "Total Stock Market Index", "VTI"},
		{"traditional401k", "Total Bond Market", "AGG"},
		{"taxable", "International bond fund", "AGG"},
		{"taxable", "Total International Stock", "EFA"},
		{"taxable", "Emerging markets", "EEM"},
		{"taxable", "Treasury bills", "BIL"},
		{"taxable", "Treasury fund", "IEF"},
		{"taxable", "TIPS", "TIP"},
		{"taxable", "Muni bonds", "MUB"},
		{"taxable", "REIT index", "VNQ"},
		{"taxable", "Gold", "GLD"},
		{"taxable", "Goldman Sachs account", "SPY"}, // "gold" is not a word here
		{"traditional401k", "Target Date 2050", "AOR"},
		{"taxable", "Nasdaq 100", "QQQ"},
		{"taxable", "Small-cap value", "IWM"},
		{"hsa", "Health savings account", "SPY"},
		{"taxable", "Money market", "BIL"},
	} {
		if got := proxyFor(tc.account, tc.name).Ticker; got != tc.want {
			t.Errorf("proxyFor(%q, %q) = %s, want %s", tc.account, tc.name, got, tc.want)
		}
	}
}

func TestResolve(t *testing.T) {
	prices := etfs("VFINX", "VBMFX", "AAPL", "VOO")
	prices["AAPL"] = true
	for _, tc := range []struct {
		name, ticker, account string
		want                  Holding
		series, warning       string
	}{
		// No ticker: the ETF for what it is, with the older fund behind it.
		{"Roth IRA", "", "rothIra",
			Holding{Ticker: "SPY", Source: "proxy", Tracks: stocks.Tracks, Earlier: []string{"VFINX"}}, "db:SPY|db:VFINX", ""},
		{"Bond fund", "", "taxable",
			Holding{Ticker: "AGG", Source: "proxy", Tracks: "US investment-grade bonds", Earlier: []string{"VBMFX"}}, "db:AGG|db:VBMFX", ""},
		// An older fund the database lacks is left out.
		{"Small-cap fund", "", "taxable",
			Holding{Ticker: "IWM", Source: "proxy", Tracks: "US small-company stocks (Russell 2000)", Earlier: []string{}}, "db:IWM", ""},
		// Cash goes back on Treasury bill yields, which need no ticker.
		{"Savings", "", "bank",
			Holding{Ticker: "BIL", Source: "proxy", Tracks: bank.Tracks, Earlier: []string{"cash"}}, "db:BIL|cash", ""},
		// A named ticker is used, with the proxy standing in before it existed.
		{"Brokerage", " voo ", "taxable",
			Holding{Ticker: "VOO", Source: "ticker", Tracks: stocks.Tracks, Earlier: []string{"SPY", "VFINX"}}, "db:VOO|db:SPY|db:VFINX", ""},
		{"Brokerage", "SPY", "taxable",
			Holding{Ticker: "SPY", Source: "ticker", Tracks: stocks.Tracks, Earlier: []string{"VFINX"}}, "db:SPY|db:VFINX", ""},
		{"Apple shares", "AAPL", "taxable",
			Holding{Ticker: "AAPL", Source: "ticker", Tracks: stocks.Tracks, Earlier: []string{"SPY", "VFINX"}}, "db:AAPL|db:SPY|db:VFINX", "AAPL is one company's stock"},
		// A ticker without prices falls back to the proxy, and says so.
		{"Startup", "ACME", "taxable",
			Holding{Ticker: "SPY", Source: "proxy", Tracks: stocks.Tracks, Earlier: []string{"VFINX"}}, "db:SPY|db:VFINX", "there is no price history for ACME"},
		{"Odd", "../x", "taxable",
			Holding{Ticker: "SPY", Source: "proxy", Tracks: stocks.Tracks, Earlier: []string{"VFINX"}}, "db:SPY|db:VFINX", "there is no price history for ../X"},
	} {
		got, warning, err := resolve(Holding{Name: tc.name}, tc.ticker, tc.account, prices)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got.Ticker != tc.want.Ticker || got.Source != tc.want.Source || got.Tracks != tc.want.Tracks || !slices.Equal(got.Earlier, tc.want.Earlier) {
			t.Errorf("%s (%q): got %+v", tc.name, tc.ticker, got)
		}
		if got.series != tc.series {
			t.Errorf("%s (%q): series %q, want %q", tc.name, tc.ticker, got.series, tc.series)
		}
		if (tc.warning == "") != (warning == "") || !strings.Contains(warning, tc.warning) {
			t.Errorf("%s (%q): warning %q, want one mentioning %q", tc.name, tc.ticker, warning, tc.warning)
		}
	}

	_, _, err := resolve(Holding{Name: "Gold"}, "", "taxable", fakePrices{"SPY": false})
	if err == nil || !strings.Contains(err.Error(), "the prices database has no GLD, which stands in for gold") {
		t.Errorf("a missing proxy: %v", err)
	}
}

func TestBuildIncomeAndExpenses(t *testing.T) {
	b := mustBuild(t, request(t, `[
		{"id": "pay", "category": "income", "name": "Pay", "amount": 1000, "frequency": "biweekly", "endYear": 2030},
		{"id": "later", "category": "income", "name": "Pension", "amount": 500, "startYear": 2040, "startMonth": 3},
		{"id": "rent", "category": "expenses", "name": "Rent", "amount": 3000, "frequency": "monthly"},
		{"id": "tax", "category": "expenses", "name": "Property tax", "amount": 12000, "frequency": "yearly"}
	]`, `[]`), etfs())

	p := b.plan
	if p.AsOf != calendar.NewMonth(2026, 10) || p.InflationIndex != plan.DefaultInflationIndex || p.Name != "Test" {
		t.Errorf("plan: %+v", p)
	}
	pay, pension := p.Incomes[0], p.Incomes[1]
	// A 10% raise, taxed at 25%, growing 5.06% a year against 3% inflation: 2% above inflation.
	if !near(pay.Monthly, 1000*26.0/12*1.1) || pay.TaxRate != 0.25 || !pay.InflationIndexed || !near(pay.GrowthRate, 0.02) {
		t.Errorf("pay: %+v", pay)
	}
	// It ends in December 2030, 50 months after October 2026.
	if pay.Start != nil || *pay.End != 50 {
		t.Errorf("pay runs %v to %v", pay.Start, *pay.End)
	}
	if *pension.Start != 161 || pension.End != nil || !near(pension.Monthly, 550) {
		t.Errorf("pension: %+v from %d", pension, *pension.Start)
	}
	// Expenses are halved and follow inflation, untaxed.
	if len(p.Expenses) != 2 || !near(p.Expenses[0].Monthly, 1500) || !near(p.Expenses[1].Monthly, 500) || !p.Expenses[0].InflationIndexed || p.Expenses[0].TaxRate != 0 {
		t.Errorf("expenses: %+v", p.Expenses)
	}
}

func TestBuildLeavesOutItemsTheScenarioDoesNotInclude(t *testing.T) {
	req := request(t, `[
		{"id": "a", "category": "expenses", "name": "A", "amount": 1},
		{"id": "b", "category": "expenses", "name": "B", "amount": 2},
		{"id": "c", "category": "investments", "name": "C", "amount": 3}
	]`, `[]`)
	req.Scenario.ItemIDs = []string{"b"}
	b := mustBuild(t, req, etfs())
	if len(b.plan.Expenses) != 1 || b.plan.Expenses[0].Name != "B" || len(b.holdings) != 1 || b.holdings[0].ItemID != newSavings {
		t.Errorf("expenses %+v, holdings %+v", b.plan.Expenses, b.holdings)
	}
}

func TestBuildEvents(t *testing.T) {
	b := mustBuild(t, request(t, `[{"id": "pay", "category": "income", "name": "Pay", "amount": 1000, "frequency": "monthly"}]`, `[
		{"id": "e1", "kind": "oneTimeCost", "name": "Down payment", "year": 2, "amount": 120000},
		{"id": "e2", "kind": "recurringCost", "name": "Childcare", "year": 3, "amount": 24000, "years": 2},
		{"id": "e3", "kind": "oneTimeIncome", "name": "Bonus", "year": 1, "amount": 12000}
	]`), etfs())
	type span struct {
		name       string
		monthly    float64
		start, end int
	}
	spans := func(streams []plan.Stream) []span {
		var out []span
		for _, s := range streams {
			if s.Start != nil {
				out = append(out, span{s.Name, s.Monthly, *s.Start, *s.End})
			}
		}
		return out
	}
	// An event's yearly amount is spread over the months of its year.
	if got, want := spans(b.plan.Expenses), []span{{"Down payment", 10000, 12, 23}, {"Childcare", 2000, 24, 47}}; !slices.Equal(got, want) {
		t.Errorf("costs %+v, want %+v", got, want)
	}
	if got, want := spans(b.plan.Incomes), []span{{"Bonus", 1000, 0, 11}}; !slices.Equal(got, want) {
		t.Errorf("one-time income %+v, want %+v", got, want)
	}
	// One-time income is taxed and follows inflation, but doesn't get the raise or the growth.
	bonus := b.plan.Incomes[1]
	if bonus.TaxRate != 0.25 || bonus.GrowthRate != 0 || !bonus.InflationIndexed {
		t.Errorf("bonus: %+v", bonus)
	}
}

func TestIncomePausesCutMonthsOutOfIncome(t *testing.T) {
	b := mustBuild(t, request(t, `[
		{"id": "pay", "category": "income", "name": "Pay", "amount": 1000, "frequency": "monthly"},
		{"id": "short", "category": "income", "name": "Contract", "amount": 500, "frequency": "monthly", "endYear": 2027, "endMonth": 3}
	]`, `[
		{"id": "e1", "kind": "incomePause", "name": "Leave", "year": 2, "amount": 0, "months": 6},
		{"id": "e2", "kind": "incomePause", "name": "Sabbatical", "year": 2, "amount": 0, "months": 3},
		{"id": "e3", "kind": "oneTimeIncome", "name": "Bonus", "year": 2, "amount": 12000}
	]`), etfs())
	var got []string
	for _, s := range b.plan.Incomes {
		got = append(got, s.Name+" "+strings.Trim(strings.Join(strings.Fields(jsonOf(t, []*int{s.Start, s.End})), ""), "[]"))
	}
	// Both pauses start in year 2, so they run back to back: months 12-17 and
	// 18-20. Pay stops for all nine; the contract ended before either; and
	// one-time income isn't paused.
	want := []string{"Pay 0,11", "Pay 21,239", "Contract 0,5", "Bonus 12,23"}
	if !slices.Equal(got, want) {
		t.Errorf("income pieces %q, want %q", got, want)
	}
	// Every piece keeps the amount, so growth still counts from the start.
	if !near(b.plan.Incomes[1].Monthly, b.plan.Incomes[0].Monthly) {
		t.Errorf("pieces differ: %v and %v", b.plan.Incomes[0].Monthly, b.plan.Incomes[1].Monthly)
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out.String())
}

func TestBuildInvestments(t *testing.T) {
	req := request(t, `[
		{"id": "pay", "category": "income", "name": "Job", "amount": 1000, "frequency": "monthly",
			"benefits": {"retirement": {"contributionRate": 0.1, "roth": false, "balance": {"amount": 50000}},
				"hsa": {"employeeAnnual": 1000, "balance": {"amount": 1000, "asOfYear": 2025, "asOfMonth": 10}},
				"plan457": {"contributionRate": 0.05, "roth": true}}},
		{"id": "vti", "category": "investments", "name": "Brokerage", "amount": 1000, "account": "taxable", "ticker": "VTI"},
		{"id": "old", "category": "investments", "name": "Old fund", "amount": 1000, "account": "taxable", "asOfYear": 2024, "asOfMonth": 10, "returnRate": 0.1},
		{"id": "ira", "category": "investments", "name": "IRA", "amount": 4000, "account": "traditionalIra", "withdrawalStartYear": 2030, "withdrawalRate": 0.04},
		{"id": "roth", "category": "investments", "name": "Roth", "amount": 2000, "account": "rothIra", "withdrawalStartYear": 2020, "withdrawalStartMonth": 6, "withdrawalRate": 0.05},
		{"id": "cash", "category": "investments", "name": "Savings", "amount": 500, "account": "bank"}
	]`, `[]`)
	req.Profile.ExpectedReturn = ptr(0.05)
	b := mustBuild(t, req, etfs())

	type row struct {
		id, name, ticker string
		value            float64
	}
	var got []row
	for _, h := range b.holdings {
		got = append(got, row{h.ItemID, h.Name, h.Ticker, math.Round(h.Value*100) / 100})
	}
	want := []row{
		// A job's workplace balances start invested. The dated one grows a
		// year at the profile's 5%; a plan without a balance adds nothing.
		{"pay-retirement", "Job 401(k)", "SPY", 50000},
		{"pay-hsa", "Job HSA", "SPY", 1050},
		{"vti", "Brokerage", "VTI", 1000},
		// Two years at its own 10%.
		{"old", "Old fund", "SPY", 1210},
		{"ira", "IRA", "SPY", 4000},
		{"roth", "Roth", "SPY", 2000},
		{"cash", "Savings", "BIL", 500},
	}
	if !slices.Equal(got, want) {
		t.Errorf("holdings\n\tgot  %+v\n\twant %+v", got, want)
	}

	// The plan holds them by item id, with their series.
	for i, h := range b.plan.Holdings {
		if h.Name != b.holdings[i].ItemID || h.Series != b.holdings[i].series || h.Value != b.holdings[i].Value {
			t.Errorf("plan holding %d: %+v", i, h)
		}
	}
	// Withdrawal plans: from January 2030 (39 months on), taxed for a
	// traditional account; already under way and untaxed for the Roth.
	ira, roth := b.plan.Holdings[4].Payout, b.plan.Holdings[5].Payout
	if ira == nil || *ira != (plan.Payout{Start: 39, Rate: 0.04, TaxRate: 0.25}) {
		t.Errorf("IRA payout: %+v", ira)
	}
	if roth == nil || *roth != (plan.Payout{Start: 0, Rate: 0.05}) {
		t.Errorf("Roth payout: %+v", roth)
	}
	// Savings go to the accounts that aren't being drawn down, by size.
	wantWeights := map[string]float64{"pay-retirement": 50000, "pay-hsa": 1050, "vti": 1000, "old": 1210, "cash": 500}
	for id, w := range wantWeights {
		if !near(b.plan.TargetWeights[id], w) {
			t.Errorf("weight of %s = %v, want %v", id, b.plan.TargetWeights[id], w)
		}
	}
	if len(b.plan.TargetWeights) != len(wantWeights) {
		t.Errorf("weights: %v", b.plan.TargetWeights)
	}
}

func TestBuildNeedsSomewhereToSave(t *testing.T) {
	// With every investment being drawn down, savings get a holding of their own.
	b := mustBuild(t, request(t, `[
		{"id": "ira", "category": "investments", "name": "IRA", "amount": 4000, "account": "traditionalIra", "withdrawalStartYear": 2030, "withdrawalRate": 0.04}
	]`, `[]`), etfs())
	if len(b.holdings) != 2 || b.holdings[1].ItemID != newSavings || b.holdings[1].Ticker != "SPY" || b.holdings[1].Value != 0 {
		t.Errorf("holdings: %+v", b.holdings)
	}
	if len(b.plan.TargetWeights) != 1 || b.plan.TargetWeights[newSavings] != 1 {
		t.Errorf("weights: %v", b.plan.TargetWeights)
	}
	// With holdings that are all empty, savings are split evenly.
	b = mustBuild(t, request(t, `[
		{"id": "a", "category": "investments", "name": "A", "amount": 0},
		{"id": "b", "category": "investments", "name": "B", "amount": 0, "account": "bank"}
	]`, `[]`), etfs())
	if b.plan.TargetWeights["a"] != 1 || b.plan.TargetWeights["b"] != 1 {
		t.Errorf("weights: %v", b.plan.TargetWeights)
	}
}

func TestBuildWarnsAboutTickersItCannotUse(t *testing.T) {
	prices := etfs("AAPL")
	prices["AAPL"] = true
	b := mustBuild(t, request(t, `[
		{"id": "a", "category": "investments", "name": "Startup", "amount": 1, "ticker": "ACME"},
		{"id": "b", "category": "investments", "name": "Apple", "amount": 1, "ticker": "AAPL"}
	]`, `[]`), prices)
	if len(b.warnings) != 2 || !strings.Contains(b.warnings[0], "Startup: there is no price history for ACME") || !strings.Contains(b.warnings[1], "Apple: AAPL is one company's stock") {
		t.Errorf("warnings: %q", b.warnings)
	}
}

func TestParseErrors(t *testing.T) {
	valid := `"scenario": {"parameters": {"years": 20}}`
	for body, want := range map[string]string{
		`{`: "request: unexpected end of JSON input",
		`{"scenario": {"parameters": {"years": 0}}}`:                                                               "years must be between 1 and 60",
		`{"scenario": {"parameters": {"years": 61}}}`:                                                              "years must be between 1 and 60",
		`{"scenario": {"parameters": {"years": 5, "inflation": -1}}}`:                                              "a rate below -100%",
		`{` + valid + `, "taxRate": 1.5}`:                                                                          "taxRate must be between 0 and 1",
		`{` + valid + `, "today": "October"}`:                                                                      "today must be a date",
		`{"scenario": {"parameters": {"years": 5}, "events": [{"kind": "lottery", "year": 1}]}}`:                   `kind "lottery" is not a kind of event`,
		`{"scenario": {"parameters": {"years": 5}, "events": [{"kind": "oneTimeCost"}]}}`:                          "events[0].year must be at least 1",
		`{"scenario": {"parameters": {"years": 5}, "events": [{"kind": "incomePause", "year": 1}]}}`:               "events[0].months must be at least 1",
		`{"scenario": {"parameters": {"years": 5}, "events": [{"kind": "recurringCost", "year": 1, "years": 0}]}}`: "events[0].years must be at least 1",
		`{` + valid + `, "items": [{"category": "pets"}]}`:                                                         `items[0].category "pets" is not a category`,
		`{` + valid + `, "items": [{"category": "income", "amount": -1}]}`:                                         "items[0].amount must be at least 0",
		`{` + valid + `, "items": [{"category": "income", "frequency": "daily"}]}`:                                 `items[0].frequency "daily" is not a frequency`,
		`{` + valid + `, "items": [{"category": "investments", "withdrawalRate": 2}]}`:                             "withdrawalRate must be above 0 and at most 1",
	} {
		if _, err := Parse([]byte(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s\n\terror = %v, want one mentioning %q", body, err, want)
		}
	}
	// Fields a backtest doesn't read are ignored, so records pass through as the API returns them.
	if _, err := Parse([]byte(`{` + valid + `, "household": [], "items": [{"category": "income", "incomeType": "w2", "createdAt": "2026-09-27T06:20:39.033Z"}]}`)); err != nil {
		t.Errorf("unknown fields: %v", err)
	}
}

func TestEstimatedValue(t *testing.T) {
	for _, tc := range []struct {
		year, month *int
		want        float64
	}{
		{nil, nil, 1000},
		{ptr(2025), ptr(10), 1100},                   // a year ago
		{ptr(2026), nil, 1000 * math.Pow(1.1, 0.75)}, // January: nine months
		{ptr(2026), ptr(10), 1000},                   // this month
		{ptr(2027), ptr(1), 1000},                    // the future
	} {
		if got := estimatedValue(1000, tc.year, tc.month, 0.10, today); !near(got, tc.want) {
			t.Errorf("as of %v-%v: %v, want %v", tc.year, tc.month, got, tc.want)
		}
	}
}

// history is a prices database and a loader that needs no network: every
// proxy ETF from the given month to 2026-09, flat except where set, with flat
// prices and Treasury bills at zero.
func history(t *testing.T, from calendar.Month, tickers map[string]pricestest.Ticker) *market.Loader {
	t.Helper()
	end := calendar.NewMonth(2026, 9)
	all := map[string]pricestest.Ticker{}
	for _, ticker := range []string{"SPY", "BIL", "SHV", "AGG"} {
		all[ticker] = pricestest.Flat(from, end.Sub(from), 0)
	}
	for name, ticker := range tickers {
		all[name] = ticker
	}
	path := filepath.Join(t.TempDir(), "prices.db")
	pricestest.Write(t, path, today, all)
	prices, err := market.OpenPricesDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { prices.Close() })

	fred := func(value string) string {
		var b strings.Builder
		b.WriteString("observation_date,X\n")
		for m := calendar.NewMonth(1950, 1); m <= end; m++ {
			b.WriteString(m.String() + "-01," + value + "\n")
		}
		return b.String()
	}
	return &market.Loader{
		CacheDir: t.TempDir(),
		Prices:   prices,
		Now:      today,
		Fetch: func(address string) (string, error) {
			if strings.Contains(address, "TB3MS") {
				return fred("0"), nil
			}
			return fred("100"), nil
		},
	}
}

func TestBacktestOverEveryPeriod(t *testing.T) {
	// Prices from 2000-01 give returns from 2000-02 to 2026-09: 320 months,
	// so a 20-year scenario has 81 starting months.
	loader := history(t, calendar.NewMonth(2000, 1), nil)
	req := request(t, `[
		{"id": "pay", "category": "income", "name": "Pay", "amount": 4000, "frequency": "monthly"},
		{"id": "rent", "category": "expenses", "name": "Rent", "amount": 4000, "frequency": "monthly"},
		{"id": "spy", "category": "investments", "name": "Index fund", "amount": 100000, "account": "taxable"},
		{"id": "cash", "category": "investments", "name": "Savings", "amount": 20000, "account": "bank"}
	]`, `[]`)
	req.Scenario.Parameters.IncomeGrowth = 0.03 // level with inflation
	result, err := Backtest(req, loader, today)
	if err != nil {
		t.Fatal(err)
	}
	if result.Periods != 81 || result.FirstStart != "2000-02" || result.LastStart != "2006-10" || result.Reason != "" {
		t.Errorf("periods %d from %s to %s (%s)", result.Periods, result.FirstStart, result.LastStart, result.Reason)
	}
	if result.ScenarioID != "s1" || result.Name != "Test" || result.Years != 20 || result.AsOf != "2026-10" || result.StartInvestments != 120000 {
		t.Errorf("result: %+v", result)
	}
	if h := result.History; h.From != "2000-02" || h.To != "2026-09" || h.Years != 26.7 || h.LimitedBy != "Index fund (SPY)" {
		t.Errorf("history: %+v", h)
	}
	// Income after the raise and tax is 4400 x 0.75 = 3300 a month, and rent
	// after the cut is 2000, so 1300 is saved each month: 312,000 over 20
	// years on top of 120,000, with flat markets and prices in every period.
	want := Spread{432000, 432000, 432000, 432000, 432000, 432000, 432000}
	if *result.EndInvestments != want || result.SuccessRate != 1 {
		t.Errorf("end %+v, success rate %v", *result.EndInvestments, result.SuccessRate)
	}
	if len(result.Yearly) != 20 || result.Yearly[0].Year != 1 || result.Yearly[0].P50 != 135600 || result.Yearly[19].P50 != 432000 {
		t.Errorf("yearly: %+v", result.Yearly)
	}
	row := result.Median.Rows[0]
	if len(result.Median.Rows) != 20 || row.Income != 39600 || row.Taxes != 13200 || row.Expenses != 24000 || row.Investments != 135600 || row.InvestmentsReal != 135600 {
		t.Errorf("first year of the median path: %+v", row)
	}
	if result.Worst.ShortfallYear != nil || len(result.Warnings) != 0 || result.Warnings == nil {
		t.Errorf("shortfall %v, warnings %v", result.Worst.ShortfallYear, result.Warnings)
	}
}

func TestBacktestOutcomesDependOnTheStart(t *testing.T) {
	// The index fund loses 40% in one month, 2001-06. A five-year scenario
	// that spends from savings runs out only if its period includes it.
	crash := pricestest.Flat(calendar.NewMonth(2000, 1), 320, 0)
	crash.Returns[16] = -0.40
	loader := history(t, calendar.NewMonth(2000, 1), map[string]pricestest.Ticker{"SPY": crash})
	req := request(t, `[
		{"id": "rent", "category": "expenses", "name": "Rent", "amount": 3200, "frequency": "monthly"},
		{"id": "spy", "category": "investments", "name": "Index fund", "amount": 100000}
	]`, `[]`)
	req.Scenario.Parameters.Years = 5
	result, err := Backtest(req, loader, today)
	if err != nil {
		t.Fatal(err)
	}
	// 1600 a month for 60 months is 96,000: covered from 100,000 unless the crash takes 40% first.
	if result.Periods != 261 || result.SuccessRate >= 1 || result.SuccessRate <= 0.9 {
		t.Errorf("%d periods, success rate %v", result.Periods, result.SuccessRate)
	}
	if result.Best.EndInvestments != 4000 || result.Best.ShortfallYear != nil || result.Best.MaxDrawdown != 0 {
		t.Errorf("best: %+v", result.Best)
	}
	if result.Worst.EndInvestments >= 0 || result.Worst.ShortfallYear == nil || !near(result.Worst.MaxDrawdown, -0.40) {
		t.Errorf("worst: ends %v, shortfall %v, drawdown %v", result.Worst.EndInvestments, result.Worst.ShortfallYear, result.Worst.MaxDrawdown)
	}
	if result.EndInvestments.Worst != result.Worst.EndInvestments || result.EndInvestments.Best != 4000 || result.EndInvestments.P50 != 4000 {
		t.Errorf("spread: %+v", result.EndInvestments)
	}
}

func TestBacktestWhenHistoryIsTooShort(t *testing.T) {
	loader := history(t, calendar.NewMonth(2015, 1), nil)
	req := request(t, `[{"id": "spy", "category": "investments", "name": "Index fund", "amount": 100000}]`, `[]`)
	result, err := Backtest(req, loader, today)
	if err != nil {
		t.Fatal(err)
	}
	if result.Periods != 0 || result.EndInvestments != nil || result.Worst != nil || len(result.Yearly) != 0 {
		t.Errorf("result: %+v", result)
	}
	if want := "The scenario runs 20 years, but the price history for its holdings covers only 11.7 (2015-02 to 2026-09)."; result.Reason != want {
		t.Errorf("reason %q, want %q", result.Reason, want)
	}
	// It still says what each investment would be backtested as.
	if len(result.Holdings) != 1 || result.Holdings[0].Ticker != "SPY" || result.StartInvestments != 100000 {
		t.Errorf("holdings: %+v", result.Holdings)
	}
}

func TestBacktestNeedsAPricesDatabase(t *testing.T) {
	req := request(t, `[]`, `[]`)
	if _, err := Backtest(req, &market.Loader{}, today); err == nil || !strings.Contains(err.Error(), "needs a prices database") {
		t.Errorf("error = %v", err)
	}
}

func TestResultJSON(t *testing.T) {
	loader := history(t, calendar.NewMonth(2000, 1), nil)
	req := request(t, `[{"id": "spy", "category": "investments", "name": "Index fund", "amount": 100000, "ticker": "SPY"}]`, `[]`)
	result, err := Backtest(req, loader, today)
	if err != nil {
		t.Fatal(err)
	}
	out := jsonOf(t, result)
	for _, part := range []string{
		`"scenarioId":"s1"`,
		`"holdings":[{"itemId":"spy","name":"Index fund","value":100000,"ticker":"SPY","source":"ticker","tracks":"US large-company stocks (S&P 500)","earlier":[]}]`,
		`"warnings":[]`,
		`"history":{"from":"2000-02","to":"2026-09","years":26.7,"limitedBy":"Index fund (SPY)"}`,
		`"periods":81`,
		`"successRate":1`,
		`"endInvestments":{"worst":100000,"p10":100000,`,
		`"yearly":[{"year":1,"worst":100000,`,
		`"worst":{"start":"2000-02","end":"2020-01","endInvestments":100000,"shortfallYear":null,`,
		`"rows":[{"year":1,"income":0,"withdrawals":0,"taxes":0,"expenses":0,"investments":100000,"investmentsReal":100000}`,
	} {
		if !strings.Contains(out, part) {
			t.Errorf("JSON lacks %s\n%s", part, out[:min(len(out), 900)])
		}
	}
}

func TestTheProxiesFileListsEveryProxyTicker(t *testing.T) {
	// prices/proxies.txt is what puts the older funds in the prices database.
	body, err := os.ReadFile("../prices/proxies.txt")
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, line := range strings.Split(string(body), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			listed = append(listed, line)
		}
	}
	want := ProxyTickers()
	slices.Sort(listed)
	slices.Sort(want)
	if !slices.Equal(listed, want) {
		t.Errorf("prices/proxies.txt lists %v, want %v", listed, want)
	}
}

func TestProxyTickersListsEveryFundOnce(t *testing.T) {
	tickers := ProxyTickers()
	for _, want := range []string{"SPY", "VFINX", "BIL", "SHV", "AGG", "VBMFX", "VTI", "VTSMX", "GLD"} {
		if !slices.Contains(tickers, want) {
			t.Errorf("missing %s in %v", want, tickers)
		}
	}
	seen := map[string]bool{}
	for _, ticker := range tickers {
		if seen[ticker] || ticker == "cash" {
			t.Errorf("%s shouldn't be listed (again)", ticker)
		}
		seen[ticker] = true
	}
}
