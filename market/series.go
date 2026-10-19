// Package market holds the monthly return series a backtest draws on and
// loads them from FRED, Yahoo Finance and CSV files.
package market

import (
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/mustard-money/financial-planning/calendar"
)

// Series is the fractional change per month (0.01 is +1%) over consecutive
// months. Prices, price indexes and yields are all reduced to this, so the
// engine treats a stock fund, a house price index and inflation the same way.
type Series struct {
	ID      string
	Start   calendar.Month // the first month with a return
	Returns []float64
	// LastLevel is the latest price, for series built from prices; otherwise 0.
	LastLevel float64
}

// End is the last month with a return.
func (s *Series) End() calendar.Month {
	return s.Start.Add(len(s.Returns) - 1)
}

// FromLevels builds a series from prices or index levels, one per month.
//
// A month's return is its level over the previous month's, so the series
// starts the month after the first level. Months without a level (a skipped
// release, or a quarterly index) get the constant rate that joins the levels
// on either side.
func FromLevels(id string, levels map[calendar.Month]float64) (*Series, error) {
	months := slices.Sorted(maps.Keys(levels))
	if len(months) < 2 {
		return nil, fmt.Errorf("%s needs at least two months of data", id)
	}
	for _, m := range months {
		if levels[m] <= 0 {
			return nil, fmt.Errorf("%s has a level that is not above zero", id)
		}
	}
	var returns []float64
	for i, b := range months[1:] {
		a := months[i]
		gap := b.Sub(a)
		step := math.Pow(levels[b]/levels[a], 1/float64(gap)) - 1
		for range gap {
			returns = append(returns, step)
		}
	}
	return &Series{ID: id, Start: months[0].Add(1), Returns: returns, LastLevel: levels[months[len(months)-1]]}, nil
}

// FromYields builds a series from annualized yields in percent (4.5 is 4.5% a
// year). Each month earns a twelfth of that month's yield, compounded; months
// without a value reuse the one before.
func FromYields(id string, yields map[calendar.Month]float64) (*Series, error) {
	months := slices.Sorted(maps.Keys(yields))
	if len(months) == 0 {
		return nil, fmt.Errorf("%s has no data", id)
	}
	first, last := months[0], months[len(months)-1]
	returns := make([]float64, 0, last.Sub(first)+1)
	for m := first; m <= last; m++ {
		rate, ok := yields[m]
		if !ok {
			returns = append(returns, returns[len(returns)-1])
			continue
		}
		if rate <= -100 {
			return nil, fmt.Errorf("%s has a yield of %v%%", id, rate)
		}
		returns = append(returns, math.Pow(1+rate/100, 1.0/12)-1)
	}
	return &Series{ID: id, Start: first, Returns: returns}, nil
}

// WithEarlier extends the series back in time with another's returns. For a
// fund that is younger than the history wanted: its own returns are kept, and
// the months before it existed come from earlier.
func (s *Series) WithEarlier(earlier *Series) (*Series, error) {
	if earlier.Start >= s.Start {
		return s, nil
	}
	if earlier.End().Add(1) < s.Start {
		return nil, fmt.Errorf("%s ends at %s, before %s starts at %s", earlier.ID, earlier.End(), s.ID, s.Start)
	}
	head := earlier.Returns[:s.Start.Sub(earlier.Start)]
	return &Series{ID: s.ID, Start: earlier.Start, Returns: slices.Concat(head, s.Returns), LastLevel: s.LastLevel}, nil
}

// Data is the series a backtest reads, by id.
type Data map[string]*Series

// Window returns the first and last month every one of the series covers.
func (d Data) Window(ids []string) (first, last calendar.Month, err error) {
	if len(ids) == 0 {
		return 0, 0, fmt.Errorf("no series to backtest against")
	}
	var latestStart, earliestEnd *Series
	for _, id := range ids {
		s, ok := d[id]
		if !ok {
			return 0, 0, fmt.Errorf("no market data loaded for %s", id)
		}
		if latestStart == nil || s.Start > latestStart.Start {
			latestStart = s
		}
		if earliestEnd == nil || s.End() < earliestEnd.End() {
			earliestEnd = s
		}
	}
	first, last = latestStart.Start, earliestEnd.End()
	if last < first {
		return 0, 0, fmt.Errorf("the series share no months: %s starts at %s but %s ends at %s",
			latestStart.ID, first, earliestEnd.ID, last)
	}
	return first, last, nil
}
