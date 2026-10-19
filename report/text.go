// Package report renders backtest results as text, CSV and JSON.
package report

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mustard-money/financial-planning/backtest"
	"github.com/mustard-money/financial-planning/calendar"
	"github.com/mustard-money/financial-planning/market"
	"github.com/mustard-money/financial-planning/plan"
)

// RunText is the report for one run: balances, returns, cash flows and a
// year-by-year table.
func RunText(result *backtest.Result, summary backtest.Summary) string {
	last := result.Months[len(result.Months)-1]
	lines := []string{
		result.Plan.Name,
		fmt.Sprintf("Backtest over %s to %s (%s): the plan's %s is treated as %s",
			summary.Start, summary.End, duration(summary.Months), result.Plan.AsOf, summary.Start),
		"",
	}
	lines = append(lines, table([][]string{
		{"", "Start", "End", "End, in start dollars"},
		{"Net worth", money(summary.StartNetWorth), money(summary.EndNetWorth), money(summary.EndNetWorthReal)},
		{"  Securities", money(summary.StartSecurities), money(summary.EndSecurities), money(summary.EndSecurities / last.PriceLevel)},
		{"  Real estate equity", money(summary.StartRealEstateEquity), money(summary.EndRealEstateEquity), money(summary.EndRealEstateEquity / last.PriceLevel)},
		{"  Unfunded shortfall", money(0), money(-last.Unfunded), money(-last.Unfunded / last.PriceLevel)},
	}, true)...)

	lines = append(lines, "", "Securities (time-weighted, so deposits and withdrawals don't count as return)")
	lines = append(lines, table([][]string{
		{"  Return a year", fmt.Sprintf("%s, or %s after inflation", percent(summary.PortfolioReturn), percent(summary.PortfolioReturnReal))},
		{"  Volatility a year", percent(summary.PortfolioVolatility)},
		{"  Deepest drawdown", percent(summary.MaxDrawdown) + drawdownSpan(summary)},
	}, false)...)
	lines = append(lines, "Inflation a year: "+percent(summary.Inflation), "", "Cash flows, added up over the period")

	flows := [][]string{
		{"  Income after tax", money(summary.Income)},
		{"  Taxes on income", money(summary.Taxes)},
		{"  Expenses", money(summary.Expenses)},
		{"  Real estate, net of costs and mortgage", money(summary.PropertyCashFlow)},
	}
	if summary.SaleProceeds != 0 {
		flows = append(flows, []string{"  Property sales", money(summary.SaleProceeds)})
	}
	flows = append(flows, []string{"  Invested", money(summary.Contributed)}, []string{"  Withdrawn", money(summary.Withdrawn)})
	lines = append(lines, table(flows, true)...)

	lines = append(lines, "")
	if summary.Covered() {
		lines = append(lines, "Every month's spending was covered.")
	} else {
		lines = append(lines, fmt.Sprintf("The securities ran out in %s: spending went uncovered, by as much as %s.",
			summary.ShortfallMonth, money(summary.MaxUnfunded)))
	}
	lines = append(lines, "")
	lines = append(lines, yearlyTable(result)...)
	return strings.Join(lines, "\n") + "\n"
}

func yearlyTable(result *backtest.Result) []string {
	rows := [][]string{{"Year", "Net worth", "Securities", "Real estate equity", "Income", "Expenses", "Real estate net", "Securities return"}}
	months := result.Months
	for len(months) > 0 {
		year := months[0].Month.Year()
		n := 0
		income, expenses, property, growth := 0.0, 0.0, 0.0, 1.0
		for n < len(months) && months[n].Month.Year() == year {
			income += months[n].Income
			expenses += months[n].Expenses
			property += months[n].PropertyCashFlow
			growth *= 1 + months[n].PortfolioReturn
			n++
		}
		label := strconv.Itoa(year)
		if n != 12 {
			label = fmt.Sprintf("%d (%d mo)", year, n)
		}
		last := months[n-1]
		rows = append(rows, []string{
			label, money(last.NetWorth()), money(last.Securities()), money(last.RealEstateEquity()),
			money(income), money(expenses), money(property), percent(growth - 1),
		})
		months = months[n:]
	}
	return table(rows, true)
}

// RollingText is the report for every period of one length: how often
// spending was covered and the spread of outcomes.
func RollingText(rolling *backtest.Rolling) string {
	periods := rolling.Periods
	first, last := periods[0], periods[len(periods)-1]
	covered := 0
	var failed []calendar.Month
	deepest := periods[0]
	for _, p := range periods {
		if p.Covered() {
			covered++
		} else {
			failed = append(failed, p.Start)
		}
		if p.MaxDrawdown < deepest.MaxDrawdown {
			deepest = p
		}
	}
	worst, best := rolling.Worst(), rolling.Best()

	lines := []string{
		rolling.Plan.Name,
		fmt.Sprintf("Every period of %s starting from %s to %s (%d starts, the last ending %s)",
			duration(rolling.Months), first.Start, last.Start, len(periods), last.End),
		"",
		fmt.Sprintf("Every month's spending was covered in %d of %d periods (%s).", covered, len(periods), percent(rolling.SuccessRate())),
	}
	if len(failed) > 0 {
		lines = append(lines, fmt.Sprintf("The securities ran out in periods starting %s.", monthRanges(failed)))
	}
	lines = append(lines, "", fmt.Sprintf("Net worth at the end, in start dollars (it starts at %s)", money(first.StartNetWorth)))

	spread := [][]string{{"  Worst", money(worst.EndNetWorthReal)}}
	notes := []string{fmt.Sprintf("starting %s", worst.Start)}
	for _, p := range backtest.Percentiles {
		label := fmt.Sprintf("  %vth percentile", p)
		if p == 50 {
			label = "  Median"
		}
		spread = append(spread, []string{label, money(rolling.EndNetWorthReal(p))})
		notes = append(notes, "")
	}
	spread = append(spread, []string{"  Best", money(best.EndNetWorthReal)})
	notes = append(notes, fmt.Sprintf("starting %s", best.Start))
	for i, row := range table(spread, true) {
		lines = append(lines, strings.TrimRight(row+"  "+notes[i], " "))
	}

	lines = append(lines,
		"",
		fmt.Sprintf("Securities return a year: %s worst, %s median, %s best",
			percent(rolling.PortfolioReturn(0)), percent(rolling.PortfolioReturn(50)), percent(rolling.PortfolioReturn(100))),
		"Deepest drawdown: "+percent(deepest.MaxDrawdown)+drawdownSpan(deepest),
	)
	return strings.Join(lines, "\n") + "\n"
}

// CoverageText lists the months each of the plan's series covers and the
// months a backtest can therefore run over.
func CoverageText(p *plan.Plan, data market.Data) (string, error) {
	ids := p.SeriesIDs()
	first, last, err := data.Window(ids)
	if err != nil {
		return "", err
	}
	rows := [][]string{{"Series", "From", "To"}}
	for _, id := range ids {
		rows = append(rows, []string{id, data[id].Start.String(), data[id].End().String()})
	}
	lines := append(table(rows, false), "",
		fmt.Sprintf("A backtest can run from %s to %s (%s).", first, last, duration(last.Sub(first)+1)))
	return strings.Join(lines, "\n") + "\n", nil
}

func drawdownSpan(s backtest.Summary) string {
	if s.DrawdownPeak == nil {
		return ""
	}
	return fmt.Sprintf(" (from the end of %s to the end of %s)", s.DrawdownPeak, s.DrawdownTrough)
}

// monthRanges writes sorted months as ranges: "1999-01 to 2000-03, 2007-06".
func monthRanges(months []calendar.Month) string {
	var ranges []string
	for i := 0; i < len(months); {
		j := i
		for j+1 < len(months) && months[j+1].Sub(months[j]) == 1 {
			j++
		}
		if i == j {
			ranges = append(ranges, months[i].String())
		} else {
			ranges = append(ranges, fmt.Sprintf("%s to %s", months[i], months[j]))
		}
		i = j + 1
	}
	return strings.Join(ranges, ", ")
}

// money formats dollars rounded to whole numbers, with thousands separators.
func money(v float64) string {
	digits := strconv.FormatFloat(math.Abs(v), 'f', 0, 64)
	var b strings.Builder
	if v < 0 && digits != "0" {
		b.WriteByte('-')
	}
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	return b.String()
}

func percent(v float64) string {
	return strconv.FormatFloat(v*100, 'f', 1, 64) + "%"
}

func duration(months int) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit
		}
		return fmt.Sprintf("%d %ss", n, unit)
	}
	years, rest := months/12, months%12
	switch {
	case years == 0:
		return plural(rest, "month")
	case rest == 0:
		return plural(years, "year")
	}
	return plural(years, "year") + " " + plural(rest, "month")
}

// table aligns rows into columns. Columns after the first are right-aligned
// when numeric.
func table(rows [][]string, numeric bool) []string {
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	lines := make([]string, len(rows))
	for r, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell))
			if numeric && i > 0 {
				cells[i] = pad + cell
			} else {
				cells[i] = cell + pad
			}
		}
		lines[r] = strings.TrimRight(strings.Join(cells, "  "), " ")
	}
	return lines
}
