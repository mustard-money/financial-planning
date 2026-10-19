package backtest

import (
	"strings"
	"testing"

	"github.com/mustard-money/financial-planning/market"
	"github.com/mustard-money/financial-planning/plan"
)

// spender holds 1000 and spends 100 a month, over a flat first year and then
// 10% a month: the money can't last through the flat year, but once the
// returns arrive it earns what it spends.
func spender(t *testing.T) (*plan.Plan, market.Data) {
	returns := make([]float64, 24)
	for i := 12; i < 24; i++ {
		returns[i] = 0.10
	}
	p := mustPlan(t, `"securities": [{"name": "A", "series": "a", "value": 1000}], "expenses": [{"name": "Costs", "amount": 100}]`)
	return p, data(0, series("a", returns...))
}

func TestRollCoversEveryStartThatFits(t *testing.T) {
	p, d := spender(t)
	rolling, err := Roll(p, d, 12, 1)
	if err != nil {
		t.Fatal(err)
	}
	periods := rolling.Periods
	if len(periods) != 13 || rolling.Months != 12 {
		t.Fatalf("%d periods of %d months, want 13 of 12", len(periods), rolling.Months)
	}
	if periods[0].Start != month(2000, 1) || periods[0].End != month(2000, 12) {
		t.Errorf("first period %v to %v", periods[0].Start, periods[0].End)
	}
	if periods[12].Start != month(2001, 1) || periods[12].End != month(2001, 12) {
		t.Errorf("last period %v to %v", periods[12].Start, periods[12].End)
	}
}

func TestRollOutcomesDependOnTheStart(t *testing.T) {
	p, d := spender(t)
	rolling, err := Roll(p, d, 12, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, last := rolling.Periods[0], rolling.Periods[12]
	if first.Covered() || *first.ShortfallMonth != month(2000, 11) {
		t.Errorf("starting 2000-01: shortfall %v, want 2000-11", first.ShortfallMonth)
	}
	if !last.Covered() || !near(last.EndNetWorth, 1000) {
		t.Errorf("starting 2001-01: covered %v, ends at %v", last.Covered(), last.EndNetWorth)
	}
	if rolling.Worst().Start != month(2000, 1) || rolling.Best().Start != month(2001, 1) {
		t.Errorf("worst starts %v, best starts %v", rolling.Worst().Start, rolling.Best().Start)
	}
	covered := 0
	for _, period := range rolling.Periods {
		if period.Covered() {
			covered++
		}
	}
	if covered == 0 || covered == 13 || !near(rolling.SuccessRate(), float64(covered)/13) {
		t.Errorf("%d of 13 covered, success rate %v", covered, rolling.SuccessRate())
	}
	if rolling.EndNetWorthReal(0) != rolling.Worst().EndNetWorthReal || rolling.EndNetWorthReal(100) != rolling.Best().EndNetWorthReal {
		t.Errorf("the 0th and 100th percentiles should be the worst and best")
	}
	if rolling.PortfolioReturn(0) != 0 || !(rolling.PortfolioReturn(100) > 2) {
		t.Errorf("returns from %v to %v", rolling.PortfolioReturn(0), rolling.PortfolioReturn(100))
	}
}

func TestRollStep(t *testing.T) {
	p, d := spender(t)
	rolling, err := Roll(p, d, 12, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(rolling.Periods) != 3 || rolling.Periods[1].Start != month(2000, 7) || rolling.Periods[2].Start != month(2001, 1) {
		t.Errorf("got %d periods: %+v", len(rolling.Periods), rolling.Periods)
	}
	if whole, err := Roll(p, d, 24, 1); err != nil || len(whole.Periods) != 1 {
		t.Errorf("a period the length of the data: %v", err)
	}
}

func TestRollErrors(t *testing.T) {
	p, d := spender(t)
	for months, message := range map[int]string{25: "no 25-month period fits", 0: "no 0-month period fits"} {
		if _, err := Roll(p, d, months, 1); err == nil || !strings.Contains(err.Error(), message) {
			t.Errorf("%d months: %v", months, err)
		}
	}
	if _, err := Roll(p, d, 12, 0); err == nil || !strings.Contains(err.Error(), "step must be at least 1") {
		t.Errorf("a zero step: %v", err)
	}
}

func TestPercentileInterpolates(t *testing.T) {
	values := []float64{40, 10, 30, 20}
	for percent, want := range map[float64]float64{0: 10, 10: 13, 50: 25, 100: 40} {
		if got := Percentile(values, percent); !near(got, want) {
			t.Errorf("percentile(%v) = %v, want %v", percent, got, want)
		}
	}
	if values[0] != 40 {
		t.Errorf("the values were reordered: %v", values)
	}
	if got := Percentile([]float64{7}, 90); got != 7 {
		t.Errorf("percentile of one value = %v", got)
	}
}
