package scenario

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/mustard-money/financial-planning/backtest"
	"github.com/mustard-money/financial-planning/calendar"
	"github.com/mustard-money/financial-planning/market"
)

// Result is a scenario run over every stretch of market history its length
// fits in. Money is in today's dollars unless a field says otherwise.
type Result struct {
	ScenarioID string `json:"scenarioId"`
	Name       string `json:"name"`
	Years      int    `json:"years"`
	AsOf       string `json:"asOf"` // the month the scenario starts from

	// Holdings says what each investment was backtested as.
	Holdings []Holding `json:"holdings"`
	// Warnings are things the user should know about how it was run.
	Warnings []string `json:"warnings"`
	History  History  `json:"history"`

	// Periods is how many historical periods the scenario was run over. It
	// is 0 when history is shorter than the scenario; Reason then says why,
	// and the fields below are empty.
	Periods int    `json:"periods"`
	Reason  string `json:"reason,omitempty"`

	FirstStart string `json:"firstStart,omitempty"`
	LastStart  string `json:"lastStart,omitempty"`
	// SuccessRate is the share of periods in which the investments never
	// ran out.
	SuccessRate      float64 `json:"successRate"`
	StartInvestments float64 `json:"startInvestments"`
	// EndInvestments is the spread of the investments at the end, across
	// the periods.
	EndInvestments *Spread `json:"endInvestments,omitempty"`
	// Yearly is that spread at the end of each year, for charting.
	Yearly []YearSpread `json:"yearly,omitempty"`
	// The periods that ended lowest, in the middle and highest.
	Worst  *Path `json:"worst,omitempty"`
	Median *Path `json:"median,omitempty"`
	Best   *Path `json:"best,omitempty"`
}

// History is the market data the scenario could be run against.
type History struct {
	From  string  `json:"from"`
	To    string  `json:"to"`
	Years float64 `json:"years"`
	// LimitedBy is the holding whose history starts last, which decides how
	// far back a period can begin.
	LimitedBy string `json:"limitedBy,omitempty"`
}

// Spread is where a value fell across the periods.
type Spread struct {
	Worst float64 `json:"worst"`
	P10   float64 `json:"p10"`
	P25   float64 `json:"p25"`
	P50   float64 `json:"p50"`
	P75   float64 `json:"p75"`
	P90   float64 `json:"p90"`
	Best  float64 `json:"best"`
}

// YearSpread is the spread of the investments at the end of one year.
type YearSpread struct {
	Year int `json:"year"`
	Spread
}

// Path is the scenario over one historical period.
type Path struct {
	Start string `json:"start"`
	End   string `json:"end"`
	// EndInvestments is in today's dollars.
	EndInvestments float64 `json:"endInvestments"`
	// ShortfallYear is the first year the investments ran out, if they did.
	ShortfallYear *int `json:"shortfallYear"`
	// Return and MaxDrawdown describe the investments themselves: their
	// yearly return with cash flows left out, and their deepest fall.
	Return      float64    `json:"return"`
	MaxDrawdown float64    `json:"maxDrawdown"`
	Inflation   float64    `json:"inflation"`
	Rows        []PathYear `json:"rows"`
}

// PathYear is one year of a path. Flows are in the dollars of the time, like
// the website's projection; InvestmentsReal is in today's.
type PathYear struct {
	Year            int     `json:"year"`
	Income          float64 `json:"income"` // after tax, including withdrawals
	Withdrawals     float64 `json:"withdrawals"`
	Taxes           float64 `json:"taxes"`
	Expenses        float64 `json:"expenses"`
	Investments     float64 `json:"investments"` // below zero once they've run out
	InvestmentsReal float64 `json:"investmentsReal"`
}

// Backtest runs the request against history. The loader supplies returns for
// the holdings from the prices database, and inflation.
func Backtest(req *Request, loader *market.Loader, now time.Time) (*Result, error) {
	if loader.Prices == nil {
		return nil, fmt.Errorf("a scenario backtest needs a prices database")
	}
	today, err := req.today(now)
	if err != nil {
		return nil, err
	}
	b, err := build(req, loader.Prices, today)
	if err != nil {
		return nil, err
	}
	data, err := loader.LoadAll(b.plan.SeriesIDs())
	if err != nil {
		return nil, err
	}
	first, last, err := data.Window(b.plan.SeriesIDs())
	if err != nil {
		return nil, err
	}

	years := req.Scenario.Parameters.Years
	months := years * 12
	result := &Result{
		ScenarioID: req.Scenario.ID,
		Name:       b.plan.Name,
		Years:      years,
		AsOf:       b.plan.AsOf.String(),
		Holdings:   b.holdings,
		Warnings:   append([]string{}, b.warnings...),
		History: History{
			From:      first.String(),
			To:        last.String(),
			Years:     math.Round(float64(last.Sub(first)+1)/12*10) / 10,
			LimitedBy: limitedBy(b, data, first),
		},
	}
	for _, h := range b.holdings {
		result.StartInvestments += h.Value
	}
	result.StartInvestments = cents(result.StartInvestments)

	latestStart := last.Add(1 - months)
	if latestStart < first {
		result.Reason = fmt.Sprintf("The scenario runs %d years, but the price history for its holdings covers only %.1f (%s to %s).",
			years, result.History.Years, first, last)
		return result, nil
	}

	// One run per starting month, keeping each run's year-end balances.
	type run struct {
		summary  backtest.Summary
		result   *backtest.Result
		yearEnds []float64 // in today's dollars
	}
	var runs []run
	for start := first; start <= latestStart; start++ {
		r, err := backtest.Simulate(b.plan, data, start, months)
		if err != nil {
			return nil, err
		}
		yearEnds := make([]float64, years)
		for y := range years {
			yearEnds[y] = r.Months[(y+1)*12-1].RealNetWorth()
		}
		runs = append(runs, run{backtest.Summarize(r), r, yearEnds})
	}

	covered := 0
	ends := make([]float64, len(runs))
	for i, r := range runs {
		ends[i] = r.summary.EndNetWorthReal
		if r.summary.Covered() {
			covered++
		}
	}
	result.Periods = len(runs)
	result.FirstStart, result.LastStart = first.String(), latestStart.String()
	result.SuccessRate = float64(covered) / float64(len(runs))
	end := spread(ends)
	result.EndInvestments = &end
	for y := range years {
		at := make([]float64, len(runs))
		for i, r := range runs {
			at[i] = r.yearEnds[y]
		}
		result.Yearly = append(result.Yearly, YearSpread{Year: y + 1, Spread: spread(at)})
	}

	// The worst, the best, and the run that ended nearest the median.
	byEnd := make([]int, len(runs))
	for i := range byEnd {
		byEnd[i] = i
	}
	slices.SortStableFunc(byEnd, func(a, b int) int {
		switch {
		case ends[a] < ends[b]:
			return -1
		case ends[a] > ends[b]:
			return 1
		}
		return 0
	})
	pick := func(i int) *Path { return path(runs[i].result, runs[i].summary, years) }
	result.Worst = pick(byEnd[0])
	result.Median = pick(byEnd[(len(byEnd)-1)/2])
	result.Best = pick(byEnd[len(byEnd)-1])
	return result, nil
}

// limitedBy names the holding whose history starts at the first month a
// period can begin, or the inflation data if that is what starts last.
func limitedBy(b *built, data market.Data, first calendar.Month) string {
	for i, h := range b.plan.Holdings {
		if s, ok := data[h.Series]; ok && s.Start == first {
			return fmt.Sprintf("%s (%s)", b.holdings[i].Name, b.holdings[i].Ticker)
		}
	}
	return "inflation data"
}

func path(r *backtest.Result, s backtest.Summary, years int) *Path {
	p := &Path{
		Start:          s.Start.String(),
		End:            s.End.String(),
		EndInvestments: cents(s.EndNetWorthReal),
		Return:         s.PortfolioReturn,
		MaxDrawdown:    s.MaxDrawdown,
		Inflation:      s.Inflation,
	}
	if s.ShortfallMonth != nil {
		year := s.ShortfallMonth.Sub(s.Start)/12 + 1
		p.ShortfallYear = &year
	}
	for y := range years {
		row := PathYear{Year: y + 1}
		for _, m := range r.Months[y*12 : (y+1)*12] {
			row.Income += m.Income
			row.Withdrawals += m.Payouts
			row.Taxes += m.Taxes
			row.Expenses += m.Expenses
		}
		last := r.Months[(y+1)*12-1]
		row.Income, row.Withdrawals, row.Taxes, row.Expenses = cents(row.Income), cents(row.Withdrawals), cents(row.Taxes), cents(row.Expenses)
		row.Investments, row.InvestmentsReal = cents(last.NetWorth()), cents(last.RealNetWorth())
		p.Rows = append(p.Rows, row)
	}
	return p
}

func spread(values []float64) Spread {
	at := func(percent float64) float64 { return cents(backtest.Percentile(values, percent)) }
	return Spread{Worst: at(0), P10: at(10), P25: at(25), P50: at(50), P75: at(75), P90: at(90), Best: at(100)}
}

func cents(v float64) float64 {
	return math.Round(v*100) / 100
}
