package backtest

import (
	"math"

	"github.com/mustard-money/financial-planning/calendar"
)

// Summary is the statistics for one run.
type Summary struct {
	Start  calendar.Month
	End    calendar.Month
	Months int

	StartNetWorth         float64
	EndNetWorth           float64
	EndNetWorthReal       float64 // in dollars of the start
	StartSecurities       float64
	EndSecurities         float64
	StartRealEstateEquity float64
	EndRealEstateEquity   float64

	// The securities' time-weighted return: what the holdings earned, with the
	// effect of money moving in and out removed. Yearly rates.
	PortfolioReturn     float64
	PortfolioReturnReal float64
	PortfolioVolatility float64
	// The deepest fall from a high, as a negative fraction, and the month-ends
	// it ran between; the months are nil when there was no fall.
	MaxDrawdown    float64
	DrawdownPeak   *calendar.Month
	DrawdownTrough *calendar.Month

	Inflation float64 // yearly

	// Nominal totals over the whole period.
	Income           float64 // after tax, including payouts
	Taxes            float64
	Payouts          float64 // before tax
	Expenses         float64
	PropertyCashFlow float64
	SaleProceeds     float64
	Contributed      float64
	Withdrawn        float64

	// ShortfallMonth is the first month spending couldn't be covered, or nil.
	ShortfallMonth *calendar.Month
	MaxUnfunded    float64
}

// Covered reports whether every month's spending was covered.
func (s Summary) Covered() bool { return s.ShortfallMonth == nil }

// Summarize computes the statistics for a run.
func Summarize(result *Result) Summary {
	rows := result.Months
	last := rows[len(rows)-1]
	years := float64(len(rows)) / 12

	s := Summary{
		Start:                 result.Start,
		End:                   last.Month,
		Months:                len(rows),
		StartNetWorth:         result.InitialNetWorth(),
		EndNetWorth:           last.NetWorth(),
		EndNetWorthReal:       last.RealNetWorth(),
		StartSecurities:       result.InitialSecurities(),
		EndSecurities:         last.Securities(),
		StartRealEstateEquity: result.InitialRealEstateEquity(),
		EndRealEstateEquity:   last.RealEstateEquity(),
		Inflation:             annualized(last.PriceLevel, years),
	}

	// Balances are month-end, so the starting value is the end of the month before.
	index, peak := 1.0, 1.0
	peakMonth := result.Start.Add(-1)
	mean := 0.0
	for _, r := range rows {
		s.Income += r.Income
		s.Taxes += r.Taxes
		s.Payouts += r.Payouts
		s.Expenses += r.Expenses
		s.PropertyCashFlow += r.PropertyCashFlow
		s.SaleProceeds += r.SaleProceeds
		s.Contributed += r.Contributed
		s.Withdrawn += r.Withdrawn
		s.MaxUnfunded = max(s.MaxUnfunded, r.Unfunded)
		if r.Unfunded > 0 && s.ShortfallMonth == nil {
			s.ShortfallMonth = &r.Month
		}

		mean += r.PortfolioReturn / float64(len(rows))
		index *= 1 + r.PortfolioReturn
		if index >= peak {
			peak, peakMonth = index, r.Month
		} else if index/peak-1 < s.MaxDrawdown {
			s.MaxDrawdown = index/peak - 1
			from := peakMonth // peakMonth moves on; keep the peak this fall began at
			s.DrawdownPeak, s.DrawdownTrough = &from, &r.Month
		}
	}

	s.PortfolioReturn = annualized(index, years)
	s.PortfolioReturnReal = annualized(index/last.PriceLevel, years)
	if len(rows) > 1 {
		squares := 0.0
		for _, r := range rows {
			squares += (r.PortfolioReturn - mean) * (r.PortfolioReturn - mean)
		}
		s.PortfolioVolatility = math.Sqrt(squares/float64(len(rows)-1)) * math.Sqrt(12)
	}
	return s
}

// annualized is the yearly rate that compounds to growth (1.5 is +50%) over
// the given number of years.
func annualized(growth, years float64) float64 {
	if growth <= 0 {
		return -1
	}
	return math.Pow(growth, 1/years) - 1
}
