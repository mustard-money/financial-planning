// Package plan defines what a backtest runs: income, expenses, securities,
// real estate and the policy for managing them.
//
// Plans are JSON (camelCase, like the customer-data API). Income and expense
// entries use the same field names as that API's items, so items can be pasted
// in; the item fields the backtester has no use for are ignored. Anything else
// unknown is an error, so a typo doesn't silently change the result.
package plan

import (
	"math"
	"slices"

	"github.com/mustard-money/financial-planning/calendar"
)

const (
	DefaultInflationIndex = "fred:CPIAUCSL"
	// DefaultPriceIndex is the S&P Case-Shiller US national home price index.
	DefaultPriceIndex   = "fred:CSUSHPINSA"
	DefaultSaleCostRate = 0.06
)

// Plan is a household's finances as of one month, and how they are managed.
type Plan struct {
	Name string
	// AsOf is plan month 0: when the amounts and values are quoted.
	AsOf           calendar.Month
	Incomes        []Stream
	Expenses       []Stream
	Holdings       []Holding
	Properties     []Property
	InflationIndex string
	// RebalanceMonths is how often holdings are reset to the target weights; 0 is never.
	RebalanceMonths int
	// TargetWeights is by holding name; nil means the starting mix.
	TargetWeights map[string]float64
	// WithdrawalOrder names the holdings to draw down first.
	WithdrawalOrder []string
}

// SeriesIDs lists the market series the plan refers to, sorted.
func (p *Plan) SeriesIDs() []string {
	ids := []string{p.InflationIndex}
	for _, h := range p.Holdings {
		if h.Series != "" {
			ids = append(ids, h.Series)
		}
	}
	for _, prop := range p.Properties {
		ids = append(ids, prop.PriceIndex)
		if prop.RentIndex != "" {
			ids = append(ids, prop.RentIndex)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// Stream is a recurring income or expense, in dollars of the plan's as-of month.
type Stream struct {
	Name    string
	Monthly float64 // averaged over the frequency
	// Start and End are the first and last plan months it applies to (0 is
	// the as-of month); nil means from the beginning, or with no end.
	Start, End       *int
	InflationIndexed bool
	GrowthRate       float64 // yearly, on top of inflation when indexed
	TaxRate          float64 // share of income lost to tax; 0 for expenses
}

// Amount is what the stream pays or costs in a plan month, before tax.
func (s Stream) Amount(planMonth int, priceLevel float64) float64 {
	if s.Start != nil && planMonth < *s.Start {
		return 0
	}
	if s.End != nil && planMonth > *s.End {
		return 0
	}
	amount := s.Monthly * math.Pow(1+s.GrowthRate, float64(planMonth)/12)
	if s.InflationIndexed {
		amount *= priceLevel
	}
	return amount
}

// Holding is a security, held as a dollar value or a number of shares.
type Holding struct {
	Name   string
	Series string   // empty earns nothing
	Value  float64  // dollars; unused when Shares is set
	Shares *float64 // priced at the series' latest level
	// Payout, if set, pays part of the holding out as income on a schedule,
	// like withdrawals from a retirement account. Such a holding is kept for
	// its payouts: deficits are taken from it only after the other holdings
	// run out.
	Payout *Payout
}

// Payout is a schedule of withdrawals from a holding.
type Payout struct {
	Start   int     // the first plan month it pays in
	Rate    float64 // share of the balance paid out over a year
	TaxRate float64 // share of each payment lost to tax
}

// Mortgage is a fixed-rate loan.
type Mortgage struct {
	Balance float64
	Rate    float64 // yearly
	Payment float64 // monthly principal and interest
}

// Property is an investment property.
type Property struct {
	Name              string
	Value             float64
	PriceIndex        string
	MonthlyRent       float64
	RentIndex         string  // empty follows inflation
	VacancyRate       float64 // share of rent lost to vacancy
	ManagementFeeRate float64 // share of collected rent
	AnnualCostRate    float64 // taxes, insurance and upkeep, as a yearly share of the value
	MonthlyCosts      float64 // other costs in as-of dollars, rising with inflation
	Mortgage          *Mortgage
	SaleMonth         *int // plan month it is sold at the end of; nil is never
	SaleCostRate      float64
}
