// Package backtest simulates a plan month by month over a stretch of market
// history, summarizes the outcome, and repeats it over every such stretch.
//
// The plan's as-of month is laid over a historical start month, and history
// supplies what the plan can't know: each security's return, house prices,
// rents and inflation. Amounts stay in the plan's dollars at the start and then
// follow the inflation that actually happened, so the question answered is
// "what if the coming years unfolded like the ones from <start>?"
//
// Each month, in order:
//  1. Income and expenses are paid at the price level the month opens with
//     (income after its tax rate).
//  2. Properties collect rent, pay their costs and mortgage, and reprice with
//     their house price index; a property due to be sold is sold at the new price.
//  3. Securities earn the month's return, and holdings with a payout schedule
//     pay out, which counts as income after its tax rate.
//  4. The net cash flow moves: a surplus first repays any shortfall and is then
//     invested by the target weights; a deficit is withdrawn from securities
//     (in the withdrawal order, then in proportion to what's left, taking from
//     holdings with a payout schedule last).
//  5. If it's a rebalancing month, securities are reset to the target weights.
//
// Securities never go below zero. A deficit they can't cover accumulates as an
// unfunded shortfall, without interest, and counts against net worth; real
// estate is not sold to cover it.
package backtest

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/mustard-money/financial-planning/calendar"
	"github.com/mustard-money/financial-planning/market"
	"github.com/mustard-money/financial-planning/plan"
)

// UninvestedName is the holding a surplus goes to when the plan has no
// securities. It earns nothing.
const UninvestedName = "Uninvested cash"

// Deficits smaller than this are rounding error, not a shortfall.
const epsilon = 1e-6

// MonthResult is one simulated month. Flows are totals for the month;
// balances are at its end.
type MonthResult struct {
	Month            calendar.Month // the historical month
	Income           float64        // after tax, including payouts
	Taxes            float64
	Payouts          float64 // paid out of holdings on a schedule, before tax
	Expenses         float64
	PropertyCashFlow float64 // rent less costs and mortgage payments
	SaleProceeds     float64 // from properties sold this month, after costs and the loan
	Contributed      float64 // surplus invested in securities
	Withdrawn        float64 // taken from securities to cover a deficit
	PortfolioReturn  float64 // the securities' return, before the month's cash flow
	// Holdings is the value of each security, in Result.HoldingNames order.
	Holdings        []float64
	RealEstateValue float64
	MortgageBalance float64
	Unfunded        float64 // spending the securities couldn't cover, to date
	PriceLevel      float64 // 1.0 at the start
}

func (r MonthResult) Securities() float64 { return sum(r.Holdings) }

func (r MonthResult) RealEstateEquity() float64 { return r.RealEstateValue - r.MortgageBalance }

func (r MonthResult) NetWorth() float64 {
	return r.Securities() + r.RealEstateEquity() - r.Unfunded
}

// RealNetWorth is net worth in dollars of the start.
func (r MonthResult) RealNetWorth() float64 { return r.NetWorth() / r.PriceLevel }

// Result is one run of a plan over a stretch of history.
type Result struct {
	Plan                   *plan.Plan
	Start                  calendar.Month
	HoldingNames           []string
	InitialHoldings        []float64
	InitialRealEstateValue float64
	InitialMortgageBalance float64
	Months                 []MonthResult
}

func (r *Result) InitialSecurities() float64 { return sum(r.InitialHoldings) }

func (r *Result) InitialRealEstateEquity() float64 {
	return r.InitialRealEstateValue - r.InitialMortgageBalance
}

func (r *Result) InitialNetWorth() float64 {
	return r.InitialSecurities() + r.InitialRealEstateEquity()
}

// Simulate runs the plan over the given number of months of history
// beginning at start.
func Simulate(p *plan.Plan, data market.Data, start calendar.Month, months int) (*Result, error) {
	if months < 1 {
		return nil, errors.New("a backtest needs at least one month")
	}
	first, last, err := data.Window(p.SeriesIDs())
	if err != nil {
		return nil, err
	}
	end := start.Add(months - 1)
	if start < first || end > last {
		return nil, fmt.Errorf("the market data covers %s to %s, not %s to %s", first, last, start, end)
	}
	// The period's returns for a series, indexed by plan month.
	returns := func(id string) []float64 {
		s := data[id]
		offset := start.Sub(s.Start)
		return s.Returns[offset : offset+months]
	}

	holdings := p.Holdings
	if len(holdings) == 0 {
		holdings = []plan.Holding{{Name: UninvestedName}}
	}
	names := make([]string, len(holdings))
	values := make([]float64, len(holdings))
	holdingReturns := make([][]float64, len(holdings))
	// For holdings with a payout schedule: the share paid out each month,
	// which compounds to the yearly rate.
	payoutShare := make([]float64, len(holdings))
	reserved := make([]bool, len(holdings))
	for i, h := range holdings {
		if h.Payout != nil {
			payoutShare[i] = 1 - math.Pow(1-h.Payout.Rate, 1.0/12)
			reserved[i] = true
		}
		names[i] = h.Name
		values[i] = h.Value
		if h.Shares != nil {
			price := data[h.Series].LastLevel
			if price <= 0 {
				return nil, fmt.Errorf("%s has no price to value %s's shares with", h.Series, h.Name)
			}
			values[i] = *h.Shares * price
		}
		if h.Series != "" {
			holdingReturns[i] = returns(h.Series)
		}
	}
	weights := targetWeights(p, names, values)
	order := make([]int, len(p.WithdrawalOrder))
	for i, name := range p.WithdrawalOrder {
		order[i] = slices.Index(names, name)
	}

	inflation := returns(p.InflationIndex)
	properties := make([]*propertyState, len(p.Properties))
	for i := range p.Properties {
		spec := &p.Properties[i]
		state := &propertyState{spec: spec, value: spec.Value, rentLevel: 1, prices: returns(spec.PriceIndex), rents: inflation}
		if spec.Mortgage != nil {
			state.balance = spec.Mortgage.Balance
		}
		if spec.RentIndex != "" {
			state.rents = returns(spec.RentIndex)
		}
		properties[i] = state
	}
	realEstate := func() (value, balance float64) {
		for _, prop := range properties {
			value += prop.value
			balance += prop.balance
		}
		return value, balance
	}

	result := &Result{
		Plan:            p,
		Start:           start,
		HoldingNames:    names,
		InitialHoldings: slices.Clone(values),
		Months:          make([]MonthResult, 0, months),
	}
	result.InitialRealEstateValue, result.InitialMortgageBalance = realEstate()

	priceLevel, unfunded := 1.0, 0.0
	for k := range months {
		row := MonthResult{Month: start.Add(k)}

		gross := 0.0
		for _, s := range p.Incomes {
			amount := s.Amount(k, priceLevel)
			gross += amount
			row.Taxes += amount * s.TaxRate
		}
		row.Income = gross - row.Taxes
		for _, s := range p.Expenses {
			row.Expenses += s.Amount(k, priceLevel)
		}

		for _, prop := range properties {
			flow, proceeds := prop.step(k, priceLevel)
			row.PropertyCashFlow += flow
			row.SaleProceeds += proceeds
		}

		before := sum(values)
		for i, r := range holdingReturns {
			if r != nil {
				values[i] *= 1 + r[k]
			}
		}
		if before > 0 {
			row.PortfolioReturn = sum(values)/before - 1
		}
		for i, h := range holdings {
			if h.Payout == nil || k < h.Payout.Start {
				continue
			}
			paid := values[i] * payoutShare[i]
			values[i] -= paid
			row.Payouts += paid
			row.Taxes += paid * h.Payout.TaxRate
			row.Income += paid * (1 - h.Payout.TaxRate)
		}

		net := row.Income - row.Expenses + row.PropertyCashFlow + row.SaleProceeds
		if net >= 0 {
			repaid := min(unfunded, net)
			unfunded -= repaid
			row.Contributed = net - repaid
			for i, w := range weights {
				values[i] += row.Contributed * w
			}
		} else {
			short := withdraw(values, -net, order, reserved)
			row.Withdrawn = -net - short
			unfunded += short
		}

		if p.RebalanceMonths > 0 && (k+1)%p.RebalanceMonths == 0 {
			total := sum(values)
			for i, w := range weights {
				values[i] = total * w
			}
		}

		priceLevel *= 1 + inflation[k]
		row.Holdings = slices.Clone(values)
		row.RealEstateValue, row.MortgageBalance = realEstate()
		row.Unfunded = unfunded
		row.PriceLevel = priceLevel
		result.Months = append(result.Months, row)
	}
	return result, nil
}

// targetWeights is each holding's share of the target mix: the plan's, or
// else the starting mix.
func targetWeights(p *plan.Plan, names []string, values []float64) []float64 {
	weights := make([]float64, len(names))
	switch {
	case p.TargetWeights != nil:
		for i, name := range names {
			weights[i] = p.TargetWeights[name]
		}
	case sum(values) > 0:
		copy(weights, values)
	default:
		for i := range weights {
			weights[i] = 1
		}
	}
	total := sum(weights)
	for i := range weights {
		weights[i] /= total
	}
	return weights
}

// withdraw takes amount out of values in place: from the holdings in order
// first, then from the rest in proportion to their size, and from the
// reserved ones only once the others are empty. It returns what couldn't be
// covered.
func withdraw(values []float64, amount float64, order []int, reserved []bool) float64 {
	for _, i := range order {
		take := min(values[i], amount)
		values[i] -= take
		amount -= take
	}
	for _, last := range []bool{false, true} {
		available := 0.0
		for i, v := range values {
			if reserved[i] == last && !slices.Contains(order, i) {
				available += v
			}
		}
		if amount <= 0 || available <= 0 {
			continue
		}
		take := min(available, amount)
		keep := 1 - take/available
		for i := range values {
			if reserved[i] == last && !slices.Contains(order, i) {
				values[i] *= keep
			}
		}
		amount -= take
	}
	if amount <= epsilon {
		return 0
	}
	return amount
}

type propertyState struct {
	spec      *plan.Property
	value     float64
	balance   float64
	rentLevel float64
	sold      bool
	// The period's price and rent changes, indexed by plan month.
	prices, rents []float64
}

// step advances one month and returns the operating cash flow and any sale
// proceeds.
func (s *propertyState) step(planMonth int, priceLevel float64) (flow, proceeds float64) {
	if s.sold {
		return 0, 0
	}
	spec := s.spec
	rent := spec.MonthlyRent * s.rentLevel * (1 - spec.VacancyRate)
	costs := rent*spec.ManagementFeeRate + s.value*spec.AnnualCostRate/12 + spec.MonthlyCosts*priceLevel
	payment := 0.0
	if s.balance > 0 {
		interest := s.balance * spec.Mortgage.Rate / 12
		payment = min(spec.Mortgage.Payment, s.balance+interest)
		s.balance = max(0, s.balance+interest-payment)
	}

	s.value *= 1 + s.prices[planMonth]
	s.rentLevel *= 1 + s.rents[planMonth]

	if spec.SaleMonth != nil && *spec.SaleMonth == planMonth {
		proceeds = s.value*(1-spec.SaleCostRate) - s.balance
		s.value, s.balance = 0, 0
		s.sold = true
	}
	return rent - costs - payment, proceeds
}

func sum(values []float64) float64 {
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total
}
