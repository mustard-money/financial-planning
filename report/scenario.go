package report

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mustard-money/financial-planning/scenario"
)

// ScenarioText is the report for a scenario backtest: what each investment
// was backtested as, how often the money lasted, and the spread of outcomes.
func ScenarioText(r *scenario.Result) string {
	lines := []string{r.Name}
	if r.Periods > 0 {
		lines = append(lines, fmt.Sprintf("A %s scenario from %s, run over every %s period starting from %s to %s (%d periods)",
			years(r.Years), r.AsOf, years(r.Years), r.FirstStart, r.LastStart, r.Periods))
	} else {
		lines = append(lines, fmt.Sprintf("A %s scenario from %s", years(r.Years), r.AsOf))
	}

	lines = append(lines, "", "Each investment, and what it was backtested as")
	holdings := [][]string{}
	for _, h := range r.Holdings {
		how := "stands in for " + h.Tracks
		if h.Source == "ticker" {
			how = "the ticker it names"
		}
		if len(h.Earlier) > 0 {
			how += "; earlier history from " + strings.Join(earlierNames(h.Earlier), ", then ")
		}
		holdings = append(holdings, []string{"  " + h.Name, money(h.Value), h.Ticker, how})
	}
	// The amounts are numbers and the rest words, so only the amounts are
	// right-aligned.
	widest := 0
	for _, row := range holdings {
		widest = max(widest, len(row[1]))
	}
	for _, row := range holdings {
		row[1] = strings.Repeat(" ", widest-len(row[1])) + row[1]
	}
	lines = append(lines, table(holdings, false)...)

	if r.Periods == 0 {
		lines = append(lines, "", r.Reason)
		lines = append(lines, warnings(r)...)
		return strings.Join(lines, "\n") + "\n"
	}

	covered := int(r.SuccessRate*float64(r.Periods) + 0.5)
	lines = append(lines, "",
		fmt.Sprintf("The investments never ran out in %d of %d periods (%s).", covered, r.Periods, percent(r.SuccessRate)),
		"",
		fmt.Sprintf("Investments after %d years, in today's dollars (they start at %s)", r.Years, money(r.StartInvestments)))
	end := r.EndInvestments
	spread := [][]string{
		{"  Worst", money(end.Worst), "starting " + r.Worst.Start + shortfall(r.Worst)},
		{"  10th percentile", money(end.P10), ""},
		{"  25th percentile", money(end.P25), ""},
		{"  Median", money(end.P50), "starting " + r.Median.Start + shortfall(r.Median)},
		{"  75th percentile", money(end.P75), ""},
		{"  90th percentile", money(end.P90), ""},
		{"  Best", money(end.Best), "starting " + r.Best.Start},
	}
	notes := make([]string, len(spread))
	for i, row := range spread {
		notes[i], spread[i] = row[2], row[:2]
	}
	for i, row := range table(spread, true) {
		lines = append(lines, strings.TrimRight(row+"  "+notes[i], " "))
	}

	lines = append(lines, "", "Investments at the end of each year, in today's dollars")
	yearly := [][]string{{"Year", "Worst", "10th", "Median", "90th", "Best"}}
	for _, y := range r.Yearly {
		yearly = append(yearly, []string{strconv.Itoa(y.Year), money(y.Worst), money(y.P10), money(y.P50), money(y.P90), money(y.Best)})
	}
	lines = append(lines, table(yearly, true)...)

	lines = append(lines, "", fmt.Sprintf("Prices cover %s to %s (%.1f years); the holding with the shortest history is %s.",
		r.History.From, r.History.To, r.History.Years, r.History.LimitedBy))
	lines = append(lines, warnings(r)...)
	return strings.Join(lines, "\n") + "\n"
}

func warnings(r *scenario.Result) []string {
	if len(r.Warnings) == 0 {
		return nil
	}
	lines := []string{"", "Notes"}
	for _, w := range r.Warnings {
		lines = append(lines, "  - "+w)
	}
	return lines
}

func shortfall(p *scenario.Path) string {
	if p.ShortfallYear == nil {
		return ""
	}
	return fmt.Sprintf(" (ran out in year %d)", *p.ShortfallYear)
}

func earlierNames(earlier []string) []string {
	names := make([]string, len(earlier))
	for i, e := range earlier {
		names[i] = e
		if e == "cash" {
			names[i] = "Treasury bill yields"
		}
	}
	return names
}

func years(n int) string {
	return fmt.Sprintf("%d-year", n)
}
