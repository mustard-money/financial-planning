package scenario

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/mustard-money/financial-planning/calendar"
	"github.com/mustard-money/financial-planning/plan"
)

// newSavings is the holding a surplus is invested in when every investment
// in the scenario is being drawn down on a schedule, or there are none.
const newSavings = "new-savings"

// Accounts whose withdrawals follow a plan, and the ones among them whose
// withdrawals are taxed as income. They match RetirementAccounts and
// PreTaxAccounts in the website's src/data/effective-dates.ts.
var (
	retirementAccounts = []string{
		"traditionalIra", "rothIra", "traditional401k", "roth401k",
		"traditional403b", "roth403b", "traditional457b", "roth457b",
	}
	preTaxAccounts = []string{"traditionalIra", "traditional401k", "traditional403b", "traditional457b"}
)

// built is a scenario turned into a plan the backtest engine runs.
type built struct {
	plan     *plan.Plan
	holdings []Holding
	warnings []string
}

// build turns the request into a plan as of today's month.
//
// It follows the website's projection (src/scenarios/engine.ts) wherever
// history doesn't replace an assumption:
//   - Income is the chosen income items, adjusted, in the months each is in
//     effect, less the months lost to income pauses. It keeps its real
//     growth: the scenario's income growth over its inflation.
//   - Expenses are the chosen expense items, adjusted, rising with inflation,
//     plus the life events' costs.
//   - Investments start at today's estimated value. Those with a withdrawal
//     plan pay out on it; the rest take the yearly surplus or cover the
//     deficit.
//
// The differences: time runs in months from today, where the website runs
// in calendar years from January; a life event's amount is spread over the
// twelve months of its year; and tax is a flat rate.
func build(req *Request, prices Prices, today time.Time) (*built, error) {
	asOf := calendar.MonthOf(today)
	params := req.Scenario.Parameters
	horizon := params.Years * 12
	defaultReturn := DefaultExpectedReturn
	if req.Profile.ExpectedReturn != nil {
		defaultReturn = *req.Profile.ExpectedReturn
	}

	b := &built{plan: &plan.Plan{
		Name:           req.Scenario.Name,
		AsOf:           asOf,
		InflationIndex: plan.DefaultInflationIndex,
		TargetWeights:  map[string]float64{},
	}}
	if b.plan.Name == "" {
		b.plan.Name = "Scenario"
	}

	pauses := pauseWindows(req.Scenario.Events)
	realGrowth := (1+params.IncomeGrowth)/(1+params.Inflation) - 1

	for _, item := range req.Items {
		if !slices.Contains(req.Scenario.ItemIDs, item.ID) {
			continue
		}
		switch item.Category {
		case Income:
			stream := plan.Stream{
				Name:             item.Name,
				Monthly:          item.Amount * perMonth(item.Frequency) * (1 + params.IncomeAdjustment),
				Start:            planMonth(item.StartYear, item.StartMonth, 1, asOf),
				End:              planMonth(item.EndYear, item.EndMonth, 12, asOf),
				InflationIndexed: true,
				GrowthRate:       realGrowth,
				TaxRate:          req.TaxRate,
			}
			b.plan.Incomes = append(b.plan.Incomes, splitAround(stream, pauses, horizon)...)
			if err := b.addWorkplaceBalances(item, req, prices, defaultReturn, today); err != nil {
				return nil, err
			}
		case Expenses:
			b.plan.Expenses = append(b.plan.Expenses, plan.Stream{
				Name:             item.Name,
				Monthly:          item.Amount * perMonth(item.Frequency) * (1 + params.ExpenseAdjustment),
				InflationIndexed: true,
			})
		case Investments:
			h := Holding{ItemID: item.ID, Name: item.Name, Value: estimatedValue(item.Amount, item.AsOfYear, item.AsOfMonth, rateOr(item.ReturnRate, defaultReturn), today)}
			var payout *plan.Payout
			account := deref(item.Account)
			if slices.Contains(retirementAccounts, account) && item.WithdrawalStartYear != nil && item.WithdrawalRate != nil {
				start := max(0, *planMonth(item.WithdrawalStartYear, item.WithdrawalStartMonth, 1, asOf))
				payout = &plan.Payout{Start: start, Rate: *item.WithdrawalRate}
				if slices.Contains(preTaxAccounts, account) {
					payout.TaxRate = req.TaxRate
				}
			}
			if err := b.addHolding(h, deref(item.Ticker), account, payout, prices); err != nil {
				return nil, err
			}
		}
	}

	for _, e := range req.Scenario.Events {
		// A year's amount, spread over its twelve months.
		first := (e.Year - 1) * 12
		last := first + 11
		stream := plan.Stream{Name: e.Name, Monthly: e.Amount / 12, Start: &first, End: &last, InflationIndexed: true}
		switch e.Kind {
		case OneTimeCost:
			b.plan.Expenses = append(b.plan.Expenses, stream)
		case RecurringCost:
			last = first + *e.Years*12 - 1
			b.plan.Expenses = append(b.plan.Expenses, stream)
		case OneTimeIncome:
			stream.TaxRate = req.TaxRate
			b.plan.Incomes = append(b.plan.Incomes, stream)
		}
	}

	// A surplus goes to the holdings that aren't being drawn down, in
	// proportion to their size.
	pooled := 0.0
	for _, h := range b.plan.Holdings {
		if h.Payout == nil {
			b.plan.TargetWeights[h.Name] = h.Value
			pooled += h.Value
		}
	}
	if len(b.plan.TargetWeights) == 0 {
		h := Holding{ItemID: newSavings, Name: "New savings"}
		if err := b.addHolding(h, "", "", nil, prices); err != nil {
			return nil, err
		}
		b.plan.TargetWeights[newSavings] = 1
	} else if pooled == 0 {
		for name := range b.plan.TargetWeights {
			b.plan.TargetWeights[name] = 1
		}
	}
	return b, nil
}

// addHolding resolves what the holding is backtested as and adds it to the
// plan under its item id, which is unique where names may not be.
func (b *built) addHolding(h Holding, ticker, account string, payout *plan.Payout, prices Prices) error {
	h, warning, err := resolve(h, ticker, account, prices)
	if err != nil {
		return err
	}
	if warning != "" {
		b.warnings = append(b.warnings, warning)
	}
	b.holdings = append(b.holdings, h)
	b.plan.Holdings = append(b.plan.Holdings, plan.Holding{Name: h.ItemID, Series: h.series, Value: h.Value, Payout: payout})
	return nil
}

// addWorkplaceBalances adds the balances of the workplace accounts kept on a
// W-2 income, which the scenario starts with as investments. Unlike the
// website's estimate, a dated balance is grown to today without the
// contributions made since.
func (b *built) addWorkplaceBalances(item Item, req *Request, prices Prices, defaultReturn float64, today time.Time) error {
	if item.Benefits == nil {
		return nil
	}
	for _, account := range []struct {
		key, label string
		plan       *WorkplacePlan
	}{
		{"retirement", "401(k)", item.Benefits.Retirement},
		{"plan457", "457(b)", item.Benefits.Plan457},
		{"hsa", "HSA", item.Benefits.HSA},
	} {
		if account.plan == nil || account.plan.Balance == nil {
			continue
		}
		balance := account.plan.Balance
		h := Holding{
			ItemID: item.ID + "-" + account.key,
			Name:   fmt.Sprintf("%s %s", item.Name, account.label),
			Value:  estimatedValue(balance.Amount, balance.AsOfYear, balance.AsOfMonth, defaultReturn, today),
		}
		if err := b.addHolding(h, "", "", nil, prices); err != nil {
			return err
		}
	}
	return nil
}

// estimatedValue is today's value of an amount measured on an earlier date,
// grown at the expected return for the whole months since. It matches
// estimatedValue in the website's src/data/investment-value.ts, so a backtest
// starts from the balances the app shows.
func estimatedValue(amount float64, asOfYear, asOfMonth *int, rate float64, today time.Time) float64 {
	if asOfYear == nil {
		return amount
	}
	month := 1
	if asOfMonth != nil {
		month = *asOfMonth
	}
	months := calendar.MonthOf(today).Sub(calendar.NewMonth(*asOfYear, month))
	if months <= 0 {
		return amount
	}
	return amount * math.Pow(1+rate, float64(months)/12)
}

// pauseWindows returns the plan months each income pause covers, first and
// last. As on the website, pauses that would overlap run one after another.
func pauseWindows(events []Event) [][2]int {
	var pauses []Event
	for _, e := range events {
		if e.Kind == IncomePause {
			pauses = append(pauses, e)
		}
	}
	sort.SliceStable(pauses, func(i, j int) bool { return pauses[i].Year < pauses[j].Year })
	var windows [][2]int
	next := 0
	for _, e := range pauses {
		first := max((e.Year-1)*12, next)
		next = first + *e.Months
		windows = append(windows, [2]int{first, next - 1})
	}
	return windows
}

// splitAround cuts the paused months out of an income, returning the pieces
// that remain within the scenario. Each piece keeps the income's amount and
// growth, which are counted from plan month 0 whatever its dates.
func splitAround(stream plan.Stream, pauses [][2]int, horizon int) []plan.Stream {
	if len(pauses) == 0 {
		return []plan.Stream{stream}
	}
	first, last := 0, horizon-1
	if stream.Start != nil {
		first = max(first, *stream.Start)
	}
	if stream.End != nil {
		last = min(last, *stream.End)
	}
	var pieces []plan.Stream
	piece := func(from, to int) {
		if from <= to {
			p := stream
			p.Start, p.End = &from, &to
			pieces = append(pieces, p)
		}
	}
	for _, pause := range pauses {
		if pause[1] < first || pause[0] > last {
			continue
		}
		piece(first, pause[0]-1)
		first = pause[1] + 1
	}
	piece(first, last)
	return pieces
}

// planMonth turns a year and optional month into months after as-of, or nil
// without a year.
func planMonth(year, month *int, defaultMonth int, asOf calendar.Month) *int {
	if year == nil {
		return nil
	}
	m := defaultMonth
	if month != nil {
		m = *month
	}
	offset := calendar.NewMonth(*year, m).Sub(asOf)
	return &offset
}

// perMonth is how often a frequency occurs in a month; an item without one
// is monthly, as on the website.
func perMonth(frequency *string) float64 {
	if frequency == nil {
		return 1
	}
	n, _ := plan.PerMonth(*frequency)
	return n
}

func rateOr(rate *float64, fallback float64) float64 {
	if rate != nil {
		return *rate
	}
	return fallback
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
