package backtest

import (
	"errors"
	"fmt"
	"slices"

	"github.com/mustard-money/financial-planning/market"
	"github.com/mustard-money/financial-planning/plan"
)

// Percentiles are the points of the outcome spread that reports show.
var Percentiles = []float64{10, 25, 50, 75, 90}

// Rolling is a plan run over every historical period of one length.
//
// One run shows one path. Starting the same plan in each month history allows
// shows how much the outcome depends on when you start: how often the money
// lasted, and the spread of where it ended up.
//
// The periods overlap heavily (two starts a month apart share almost all
// their months), so they are far fewer independent trials than their count
// suggests.
type Rolling struct {
	Plan    *plan.Plan
	Months  int       // length of each period
	Periods []Summary // in order of start month
}

// Roll backtests every period of the given length, starting every step
// months from the earliest month the data allows.
func Roll(p *plan.Plan, data market.Data, months, step int) (*Rolling, error) {
	if step < 1 {
		return nil, errors.New("step must be at least 1")
	}
	first, last, err := data.Window(p.SeriesIDs())
	if err != nil {
		return nil, err
	}
	latestStart := last.Add(1 - months)
	if months < 1 || latestStart < first {
		return nil, fmt.Errorf("no %d-month period fits: the market data covers %s to %s", months, first, last)
	}
	rolling := &Rolling{Plan: p, Months: months}
	for start := first; start <= latestStart; start = start.Add(step) {
		result, err := Simulate(p, data, start, months)
		if err != nil {
			return nil, err
		}
		rolling.Periods = append(rolling.Periods, Summarize(result))
	}
	return rolling, nil
}

// SuccessRate is the share of periods in which every month's spending was covered.
func (r *Rolling) SuccessRate() float64 {
	covered := 0
	for _, p := range r.Periods {
		if p.Covered() {
			covered++
		}
	}
	return float64(covered) / float64(len(r.Periods))
}

// Worst is the period that ended with the least, after inflation.
func (r *Rolling) Worst() Summary {
	return slices.MinFunc(r.Periods, byEndNetWorthReal)
}

// Best is the period that ended with the most, after inflation.
func (r *Rolling) Best() Summary {
	return slices.MaxFunc(r.Periods, byEndNetWorthReal)
}

func byEndNetWorthReal(a, b Summary) int {
	switch {
	case a.EndNetWorthReal < b.EndNetWorthReal:
		return -1
	case a.EndNetWorthReal > b.EndNetWorthReal:
		return 1
	}
	return 0
}

// EndNetWorthReal is the given percentile of the periods' ending net worth,
// in start dollars.
func (r *Rolling) EndNetWorthReal(percent float64) float64 {
	return r.percentile(percent, func(s Summary) float64 { return s.EndNetWorthReal })
}

// PortfolioReturn is the given percentile of the periods' yearly securities return.
func (r *Rolling) PortfolioReturn(percent float64) float64 {
	return r.percentile(percent, func(s Summary) float64 { return s.PortfolioReturn })
}

func (r *Rolling) percentile(percent float64, value func(Summary) float64) float64 {
	values := make([]float64, len(r.Periods))
	for i, p := range r.Periods {
		values[i] = value(p)
	}
	return Percentile(values, percent)
}

// Percentile is the value percent of the way through the sorted values,
// interpolated between neighbours.
func Percentile(values []float64, percent float64) float64 {
	ordered := slices.Sorted(slices.Values(values))
	position := float64(len(ordered)-1) * percent / 100
	below := int(position)
	above := min(below+1, len(ordered)-1)
	return ordered[below] + (ordered[above]-ordered[below])*(position-float64(below))
}
