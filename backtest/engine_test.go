package backtest

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mustard-money/financial-planning/calendar"
	"github.com/mustard-money/financial-planning/market"
	"github.com/mustard-money/financial-planning/plan"
)

var start = calendar.NewMonth(2000, 1)

func month(year, m int) calendar.Month { return calendar.NewMonth(year, m) }

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func nearAll(got, want []float64) bool { return slices.EqualFunc(got, want, near) }

// mustPlan parses a plan as of the start month, with inflation read from the
// series "cpi".
func mustPlan(t *testing.T, fields string) *plan.Plan {
	t.Helper()
	p, err := plan.Parse([]byte(`{"asOf": "2000-01", "inflationIndex": "cpi", `+fields+`}`), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func series(id string, returns ...float64) *market.Series {
	return &market.Series{ID: id, Start: start, Returns: returns}
}

// flat is two years of the same return every month.
func flat(id string, monthlyReturn float64) *market.Series {
	returns := make([]float64, 24)
	for i := range returns {
		returns[i] = monthlyReturn
	}
	return series(id, returns...)
}

// data is the given series plus two years of constant inflation as "cpi".
func data(inflation float64, all ...*market.Series) market.Data {
	d := market.Data{"cpi": flat("cpi", inflation)}
	for _, s := range all {
		d[s.ID] = s
	}
	return d
}

func mustSimulate(t *testing.T, p *plan.Plan, d market.Data, months int) *Result {
	t.Helper()
	result, err := Simulate(p, d, start, months)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// column picks one number from each month.
func column(result *Result, pick func(MonthResult) float64) []float64 {
	values := make([]float64, len(result.Months))
	for i, r := range result.Months {
		values[i] = pick(r)
	}
	return values
}

func TestHoldingsEarnTheirSeriesReturns(t *testing.T) {
	p := mustPlan(t, `"securities": [{"name": "Stocks", "series": "s", "value": 1000}, {"name": "Bonds", "series": "b", "value": 500}]`)
	result := mustSimulate(t, p, data(0, flat("s", 0.02), flat("b", -0.01)), 2)
	first, second := result.Months[0], result.Months[1]
	if !slices.Equal(result.HoldingNames, []string{"Stocks", "Bonds"}) || result.InitialNetWorth() != 1500 {
		t.Errorf("holdings %v start at %v", result.HoldingNames, result.InitialNetWorth())
	}
	if !nearAll(first.Holdings, []float64{1020, 495}) || !near(first.PortfolioReturn, 15.0/1500) {
		t.Errorf("first month: %+v", first)
	}
	if !near(second.NetWorth(), 1000*1.02*1.02+500*0.99*0.99) {
		t.Errorf("net worth after two months = %v", second.NetWorth())
	}
	if first.Month != month(2000, 1) || second.Month != month(2000, 2) {
		t.Errorf("months %v, %v", first.Month, second.Month)
	}
}

func TestSharesAreValuedAtTheLatestPrice(t *testing.T) {
	p := mustPlan(t, `"securities": [{"name": "Stocks", "symbol": "VTI", "shares": 10}]`)
	prices := flat("yahoo:VTI", 0)
	prices.LastLevel = 50
	if result := mustSimulate(t, p, data(0, prices), 1); result.InitialSecurities() != 500 {
		t.Errorf("10 shares at 50 are worth %v", result.InitialSecurities())
	}
	_, err := Simulate(p, data(0, flat("yahoo:VTI", 0)), start, 1)
	if err == nil || !strings.Contains(err.Error(), "no price to value Stocks's shares") {
		t.Errorf("without a price: %v", err)
	}
}

func TestASurplusIsInvestedByTheStartingMix(t *testing.T) {
	p := mustPlan(t, `
		"securities": [{"name": "A", "series": "a", "value": 600}, {"name": "B", "series": "b", "value": 400}],
		"income": [{"name": "Pay", "amount": 1000, "taxRate": 0.25}],
		"expenses": [{"name": "Costs", "amount": 650}]`)
	row := mustSimulate(t, p, data(0, flat("a", 0), flat("b", 0)), 1).Months[0]
	if row.Income != 750 || row.Taxes != 250 || row.Expenses != 650 || row.Contributed != 100 || row.Withdrawn != 0 {
		t.Errorf("flows: %+v", row)
	}
	if !nearAll(row.Holdings, []float64{660, 440}) {
		t.Errorf("holdings %v, want 660 and 440", row.Holdings)
	}
}

func TestASurplusIsInvestedByTargetWeightsWhenGiven(t *testing.T) {
	p := mustPlan(t, `
		"securities": [{"name": "A", "series": "a", "value": 600}, {"name": "B", "series": "b", "value": 400}],
		"income": [{"name": "Pay", "amount": 100}],
		"policy": {"targetWeights": {"B": 3, "A": 1}}`)
	row := mustSimulate(t, p, data(0, flat("a", 0), flat("b", 0)), 1).Months[0]
	if !nearAll(row.Holdings, []float64{625, 475}) {
		t.Errorf("holdings %v, want 625 and 475", row.Holdings)
	}
}

func TestThePortfolioReturnLeavesOutTheCashFlow(t *testing.T) {
	p := mustPlan(t, `"securities": [{"name": "A", "series": "a", "value": 1000}], "income": [{"name": "Pay", "amount": 500}]`)
	row := mustSimulate(t, p, data(0, flat("a", 0.10)), 1).Months[0]
	if !near(row.PortfolioReturn, 0.10) || !near(row.Securities(), 1600) {
		t.Errorf("return %v, securities %v", row.PortfolioReturn, row.Securities())
	}
}

func TestADeficitIsWithdrawnInProportion(t *testing.T) {
	p := mustPlan(t, `
		"securities": [{"name": "A", "series": "a", "value": 600}, {"name": "B", "series": "b", "value": 400}],
		"expenses": [{"name": "Costs", "amount": 100}]`)
	row := mustSimulate(t, p, data(0, flat("a", 0), flat("b", 0)), 1).Months[0]
	if row.Withdrawn != 100 || !nearAll(row.Holdings, []float64{540, 360}) {
		t.Errorf("withdrew %v leaving %v", row.Withdrawn, row.Holdings)
	}
}

func TestWithdrawalOrderThenShortfallThenRepayment(t *testing.T) {
	p := mustPlan(t, `
		"securities": [{"name": "Stocks", "series": "s", "value": 100}, {"name": "Cash", "series": "c", "value": 150}],
		"expenses": [{"name": "Costs", "amount": 100}],
		"income": [{"name": "Late job", "amount": 130, "startYear": 2000, "startMonth": 4}],
		"policy": {"withdrawalOrder": ["Cash"]}`)
	result := mustSimulate(t, p, data(0, flat("s", 0), flat("c", 0)), 5)
	for i, want := range [][]float64{{100, 50}, {50, 0}, {0, 0}} {
		if !nearAll(result.Months[i].Holdings, want) {
			t.Errorf("month %d holdings %v, want %v", i, result.Months[i].Holdings, want)
		}
	}
	if got := column(result, func(r MonthResult) float64 { return r.Withdrawn }); !nearAll(got, []float64{100, 100, 50, 0, 0}) {
		t.Errorf("withdrawn %v", got)
	}
	// From April the job leaves 30 a month: it repays the 50 shortfall before anything is invested.
	if got := column(result, func(r MonthResult) float64 { return r.Unfunded }); !nearAll(got, []float64{0, 0, 50, 20, 0}) {
		t.Errorf("unfunded %v", got)
	}
	if got := column(result, func(r MonthResult) float64 { return r.Contributed }); !nearAll(got, []float64{0, 0, 0, 0, 10}) {
		t.Errorf("contributed %v", got)
	}
	if got := result.Months[2].NetWorth(); got != -50 {
		t.Errorf("net worth with a 50 shortfall = %v", got)
	}

	summary := Summarize(result)
	if summary.Covered() || *summary.ShortfallMonth != month(2000, 3) || summary.MaxUnfunded != 50 {
		t.Errorf("summary: shortfall %v, max %v", summary.ShortfallMonth, summary.MaxUnfunded)
	}
}

func TestRebalancingRestoresTheTargetMix(t *testing.T) {
	securities := `"securities": [{"name": "A", "series": "a", "value": 500}, {"name": "B", "series": "b", "value": 500}]`
	d := data(0, flat("a", 0.10), flat("b", 0))

	drift := mustSimulate(t, mustPlan(t, securities), d, 3).Months
	if !near(drift[2].Holdings[0], 500*1.1*1.1*1.1) {
		t.Errorf("without rebalancing A = %v", drift[2].Holdings[0])
	}
	monthly := mustSimulate(t, mustPlan(t, securities+`, "policy": {"rebalance": "monthly"}`), d, 3).Months
	if !nearAll(monthly[0].Holdings, []float64{525, 525}) {
		t.Errorf("rebalanced monthly: %v", monthly[0].Holdings)
	}
	quarterly := mustSimulate(t, mustPlan(t, securities+`, "policy": {"rebalance": "quarterly"}`), d, 3).Months
	if !near(quarterly[1].Holdings[0], 605) || !near(quarterly[2].Holdings[0], quarterly[2].Holdings[1]) {
		t.Errorf("rebalanced quarterly: %v then %v", quarterly[1].Holdings, quarterly[2].Holdings)
	}
}

func TestThePeriodMustBeInsideTheData(t *testing.T) {
	p := mustPlan(t, `"securities": [{"name": "A", "series": "a", "value": 1}]`)
	late := &market.Series{ID: "a", Start: month(2000, 7), Returns: make([]float64, 12)}
	d := data(0, late)
	_, err := Simulate(p, d, start, 12)
	if err == nil || !strings.Contains(err.Error(), "covers 2000-07 to 2001-06, not 2000-01 to 2000-12") {
		t.Errorf("starting too early: %v", err)
	}
	if _, err := Simulate(p, d, month(2000, 7), 13); err == nil {
		t.Error("running past the data succeeded")
	}
	if _, err := Simulate(p, d, month(2000, 7), 0); err == nil {
		t.Error("a zero-month run succeeded")
	}
	result, err := Simulate(p, d, month(2000, 7), 12)
	if err != nil || len(result.Months) != 12 || result.Months[11].Month != month(2001, 6) {
		t.Errorf("the whole window: %v", err)
	}
}

func TestIndexedStreamsFollowThePriceLevel(t *testing.T) {
	p := mustPlan(t, `
		"securities": [{"name": "A", "series": "a", "value": 10000}],
		"expenses": [{"name": "Indexed", "amount": 100}, {"name": "Fixed", "amount": 100, "indexation": "none"}]`)
	months := mustSimulate(t, p, data(0.01, flat("a", 0)), 2).Months
	// A month's flows use the price level it opens with; its balances are deflated by the one it closes with.
	if !near(months[0].Expenses, 200) || !near(months[1].Expenses, 201) {
		t.Errorf("expenses %v then %v, want 200 then 201", months[0].Expenses, months[1].Expenses)
	}
	if !near(months[0].PriceLevel, 1.01) || !near(months[0].RealNetWorth(), 9800/1.01) {
		t.Errorf("price level %v, real net worth %v", months[0].PriceLevel, months[0].RealNetWorth())
	}

	summary := Summarize(mustSimulate(t, p, data(0.01, flat("a", 0.01)), 12))
	year := math.Pow(1.01, 12) - 1
	if !near(summary.Inflation, year) || !near(summary.PortfolioReturn, year) || !near(summary.PortfolioReturnReal, 0) {
		t.Errorf("inflation %v, return %v, real return %v", summary.Inflation, summary.PortfolioReturn, summary.PortfolioReturnReal)
	}
}

const rental = `"name": "Rental", "value": 1000, "priceIndex": "hpi", "monthlyRent": 200, "vacancyRate": 0.1,
	"managementFeeRate": 0.1, "annualCostRate": 0.12, "monthlyCosts": 2,
	"mortgage": {"balance": 500, "rate": 0.12, "payment": 105}`

func TestRentCostsMortgageAndAppreciation(t *testing.T) {
	result := mustSimulate(t, mustPlan(t, `"realEstate": [{`+rental+`}]`), data(0, flat("hpi", 0.01)), 2)
	first, second := result.Months[0], result.Months[1]
	if result.InitialNetWorth() != 500 {
		t.Errorf("starts at %v, want 500", result.InitialNetWorth())
	}
	// Rent 200 less 10% vacancy = 180; management 18; upkeep 1% of 1000 = 10; other costs 2; mortgage 105.
	if !near(first.PropertyCashFlow, 45) {
		t.Errorf("cash flow %v, want 45", first.PropertyCashFlow)
	}
	// 5 of the payment is interest, 100 principal.
	if !near(first.MortgageBalance, 400) || !near(first.RealEstateValue, 1010) {
		t.Errorf("balance %v, value %v", first.MortgageBalance, first.RealEstateValue)
	}
	// With no securities in the plan, the cash is kept uninvested.
	if !slices.Equal(result.HoldingNames, []string{UninvestedName}) || !near(first.Securities(), 45) {
		t.Errorf("%v hold %v", result.HoldingNames, first.Securities())
	}
	if !near(first.NetWorth(), 1010-400+45) {
		t.Errorf("net worth %v", first.NetWorth())
	}
	// Upkeep follows the value: 1% of 1010.
	if !near(second.PropertyCashFlow, 180-18-10.10-2-105) || !near(second.MortgageBalance, 299) {
		t.Errorf("second month: cash flow %v, balance %v", second.PropertyCashFlow, second.MortgageBalance)
	}
}

func TestRentFollowsInflationOrItsOwnIndex(t *testing.T) {
	property := `"name": "Rental", "value": 1000, "priceIndex": "hpi", "monthlyRent": 100`
	d := data(0.02, flat("hpi", 0), flat("rents", 0.10))
	withInflation := mustSimulate(t, mustPlan(t, `"realEstate": [{`+property+`}]`), d, 2).Months
	if !near(withInflation[1].PropertyCashFlow, 102) {
		t.Errorf("rent following inflation = %v, want 102", withInflation[1].PropertyCashFlow)
	}
	indexed := mustSimulate(t, mustPlan(t, `"realEstate": [{`+property+`, "rentIndex": "rents"}]`), d, 2).Months
	if !near(indexed[1].PropertyCashFlow, 110) {
		t.Errorf("rent following its index = %v, want 110", indexed[1].PropertyCashFlow)
	}
}

func TestTheLastMortgagePaymentIsWhatIsLeft(t *testing.T) {
	p := mustPlan(t, `"realEstate": [{"name": "Rental", "value": 1000, "priceIndex": "hpi",
		"mortgage": {"balance": 150, "rate": 0.12, "payment": 100}}]`)
	months := mustSimulate(t, p, data(0, flat("hpi", 0)), 3).Months
	// The first payment leaves 51.50 with its 1.50 of interest.
	final := 51.5 * 1.01
	if !near(months[0].PropertyCashFlow, -100) || !near(months[1].PropertyCashFlow, -final) || months[2].PropertyCashFlow != 0 {
		t.Errorf("payments: %v", column(&Result{Months: months}, func(r MonthResult) float64 { return r.PropertyCashFlow }))
	}
	if months[1].MortgageBalance != 0 {
		t.Errorf("balance after the last payment = %v", months[1].MortgageBalance)
	}
	// There is nothing to pay it from.
	if !near(months[2].Unfunded, 100+final) {
		t.Errorf("unfunded %v", months[2].Unfunded)
	}
}

func TestASalePaysOffTheLoanAndInvestsTheRest(t *testing.T) {
	p := mustPlan(t, `
		"realEstate": [{`+rental+`, "sale": {"year": 2000, "month": 2, "costRate": 0.1}}],
		"securities": [{"name": "A", "series": "a", "value": 0}]`)
	result := mustSimulate(t, p, data(0, flat("hpi", 0.01), flat("a", 0)), 3)
	months := result.Months
	// Sold at the end of February for 1020.10, less 10% costs and the 299 still owed.
	if !near(months[1].SaleProceeds, 1020.1*0.9-299) {
		t.Errorf("proceeds %v, want 619.09", months[1].SaleProceeds)
	}
	if months[1].RealEstateValue != 0 || months[1].MortgageBalance != 0 {
		t.Errorf("after the sale: value %v, balance %v", months[1].RealEstateValue, months[1].MortgageBalance)
	}
	if !near(months[1].Securities(), 45+44.9+619.09) {
		t.Errorf("securities %v", months[1].Securities())
	}
	if months[2].PropertyCashFlow != 0 || months[2].SaleProceeds != 0 {
		t.Errorf("the month after: %+v", months[2])
	}
	if got := Summarize(result).SaleProceeds; !near(got, 619.09) {
		t.Errorf("summary sale proceeds %v", got)
	}
}

func TestDrawdownAndVolatility(t *testing.T) {
	p := mustPlan(t, `"securities": [{"name": "A", "series": "a", "value": 1000}]`)
	summary := Summarize(mustSimulate(t, p, data(0, series("a", 0.10, -0.50, 0.20, 0)), 4))
	if !near(summary.MaxDrawdown, -0.50) || *summary.DrawdownPeak != month(2000, 1) || *summary.DrawdownTrough != month(2000, 2) {
		t.Errorf("drawdown %v from %v to %v", summary.MaxDrawdown, summary.DrawdownPeak, summary.DrawdownTrough)
	}
	// Four months are a third of a year.
	growth := 1.1 * 0.5 * 1.2
	if !near(summary.EndNetWorth, 1000*growth) || !near(summary.PortfolioReturn, math.Pow(growth, 3)-1) {
		t.Errorf("end %v, return %v", summary.EndNetWorth, summary.PortfolioReturn)
	}
	// The sample standard deviation of the four returns, times the square root of 12.
	if !near(summary.PortfolioVolatility, 0.3109126351029605*math.Sqrt(12)) {
		t.Errorf("volatility %v", summary.PortfolioVolatility)
	}
	if !summary.Covered() || summary.Start != start || summary.End != month(2000, 4) || summary.Months != 4 {
		t.Errorf("summary: %+v", summary)
	}
}

func TestTheDrawdownPeakIsNotMovedByALaterHigh(t *testing.T) {
	p := mustPlan(t, `"securities": [{"name": "A", "series": "a", "value": 1000}]`)
	summary := Summarize(mustSimulate(t, p, data(0, series("a", 0.10, -0.50, 3.0, 0.10)), 4))
	if *summary.DrawdownPeak != month(2000, 1) || *summary.DrawdownTrough != month(2000, 2) {
		t.Errorf("drawdown from %v to %v, want 2000-01 to 2000-02", summary.DrawdownPeak, summary.DrawdownTrough)
	}
}

func TestADrawdownFromTheStartPeaksTheMonthBefore(t *testing.T) {
	p := mustPlan(t, `"securities": [{"name": "A", "series": "a", "value": 1000}]`)
	summary := Summarize(mustSimulate(t, p, data(0, series("a", -0.10, -0.10, 0.50)), 3))
	if !near(summary.MaxDrawdown, -0.19) || *summary.DrawdownPeak != month(1999, 12) || *summary.DrawdownTrough != month(2000, 2) {
		t.Errorf("drawdown %v from %v to %v", summary.MaxDrawdown, summary.DrawdownPeak, summary.DrawdownTrough)
	}
}

func TestNoDrawdown(t *testing.T) {
	p := mustPlan(t, `"securities": [{"name": "A", "series": "a", "value": 1000}]`)
	summary := Summarize(mustSimulate(t, p, data(0, flat("a", 0.01)), 3))
	if summary.MaxDrawdown != 0 || summary.DrawdownPeak != nil || summary.DrawdownTrough != nil {
		t.Errorf("drawdown %v from %v to %v", summary.MaxDrawdown, summary.DrawdownPeak, summary.DrawdownTrough)
	}
	if !near(summary.PortfolioVolatility, 0) {
		t.Errorf("volatility %v", summary.PortfolioVolatility)
	}
}

// payoutPlan holds an account that pays out on a schedule and a pool that
// takes whatever is left over.
func payoutPlan(payout plan.Payout, account, pool float64, expenses ...plan.Stream) *plan.Plan {
	return &plan.Plan{
		AsOf:           start,
		InflationIndex: "cpi",
		Expenses:       expenses,
		Holdings: []plan.Holding{
			{Name: "IRA", Series: "a", Value: account, Payout: &payout},
			{Name: "Pool", Series: "b", Value: pool},
		},
		TargetWeights: map[string]float64{"Pool": 1},
	}
}

func TestPayoutsArePaidAsIncomeAfterTax(t *testing.T) {
	// Everything is paid out in the first month of the schedule.
	p := payoutPlan(plan.Payout{Start: 1, Rate: 1, TaxRate: 0.25}, 1200, 1000)
	result := mustSimulate(t, p, data(0, flat("a", 0), flat("b", 0)), 3)
	first, second, third := result.Months[0], result.Months[1], result.Months[2]
	if first.Payouts != 0 || !nearAll(first.Holdings, []float64{1200, 1000}) {
		t.Errorf("before the schedule starts: %+v", first)
	}
	if !near(second.Payouts, 1200) || !near(second.Taxes, 300) || !near(second.Income, 900) || !near(second.Contributed, 900) {
		t.Errorf("the payout month: %+v", second)
	}
	// What's left after tax is invested in the pool, not back in the account.
	if !nearAll(second.Holdings, []float64{0, 1900}) || third.Payouts != 0 {
		t.Errorf("holdings %v, then payouts %v", second.Holdings, third.Payouts)
	}
	if summary := Summarize(result); !near(summary.Payouts, 1200) || !near(summary.Taxes, 300) {
		t.Errorf("summary payouts %v, taxes %v", summary.Payouts, summary.Taxes)
	}
}

func TestAPayoutRateIsTheShareOfTheBalancePaidOverAYear(t *testing.T) {
	p := payoutPlan(plan.Payout{Rate: 0.5}, 1200, 0)
	result := mustSimulate(t, p, data(0, flat("a", 0), flat("b", 0)), 12)
	last := result.Months[11]
	if !nearAll(last.Holdings, []float64{600, 600}) {
		t.Errorf("after a year at 50%%: %v, want half paid out", last.Holdings)
	}
	// The payout doesn't count as the investments' return.
	if summary := Summarize(result); !near(summary.PortfolioReturn, 0) || !near(summary.Payouts, 600) {
		t.Errorf("return %v, payouts %v", summary.PortfolioReturn, summary.Payouts)
	}
}

func TestDeficitsComeFromAccountsWithAPayoutScheduleLast(t *testing.T) {
	later := plan.Payout{Start: 100, Rate: 0.04}
	p := payoutPlan(later, 500, 300, plan.Stream{Name: "Costs", Monthly: 200})
	result := mustSimulate(t, p, data(0, flat("a", 0), flat("b", 0)), 5)
	for i, want := range [][]float64{{500, 100}, {400, 0}, {200, 0}, {0, 0}, {0, 0}} {
		if !nearAll(result.Months[i].Holdings, want) {
			t.Errorf("month %d holdings %v, want %v", i, result.Months[i].Holdings, want)
		}
	}
	if got := column(result, func(r MonthResult) float64 { return r.Unfunded }); !nearAll(got, []float64{0, 0, 0, 0, 200}) {
		t.Errorf("unfunded %v", got)
	}
}
