package plan

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mustard-money/financial-planning/calendar"
)

const stocks = `{"name": "Stocks", "symbol": "VTI", "value": 1000}`

// parse reads a plan as of 2000-01 with the given fields.
func parse(fields string) (*Plan, error) {
	return Parse([]byte(`{"asOf": "2000-01", `+fields+`}`), time.Time{})
}

func mustParse(t *testing.T, fields string) *Plan {
	t.Helper()
	p, err := parse(fields)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func value(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestLoadExample(t *testing.T) {
	p, err := Load("../examples/household.json")
	if err != nil {
		t.Fatal(err)
	}
	if p.AsOf != calendar.NewMonth(2026, 10) || p.RebalanceMonths != 12 || !slices.Equal(p.WithdrawalOrder, []string{"Savings"}) {
		t.Errorf("got %+v", p)
	}
	want := []string{"cash", "fred:CPIAUCSL", "fred:CSUSHPINSA", "yahoo:BND|yahoo:VBMFX", "yahoo:VTI|yahoo:VFINX"}
	if got := p.SeriesIDs(); !slices.Equal(got, want) {
		t.Errorf("SeriesIDs() = %v, want %v", got, want)
	}
	if _, err := Load("missing.json"); err == nil || !strings.Contains(err.Error(), "cannot read the plan") {
		t.Errorf("a missing file: %v", err)
	}
}

func TestDefaults(t *testing.T) {
	p, err := Parse([]byte(`{"securities": [`+stocks+`]}`), time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Plan" || p.AsOf != calendar.NewMonth(2026, 10) || p.InflationIndex != "fred:CPIAUCSL" {
		t.Errorf("got %+v", p)
	}
	if p.RebalanceMonths != 0 || p.TargetWeights != nil || p.WithdrawalOrder != nil {
		t.Errorf("policy defaults: %+v", p)
	}
}

func TestStreamAmountsAreMonthlyAverages(t *testing.T) {
	p := mustParse(t, `"securities": [`+stocks+`],
		"income": [
			{"name": "Pay", "amount": 1000, "frequency": "biweekly", "taxRate": 0.2},
			{"name": "Bonus", "amount": 1200, "frequency": "yearly"}
		],
		"expenses": [{"name": " Rent ", "amount": 900}]`)
	if !near(p.Incomes[0].Monthly, 1000*26.0/12) || p.Incomes[0].TaxRate != 0.2 {
		t.Errorf("biweekly pay: %+v", p.Incomes[0])
	}
	if !near(p.Incomes[1].Monthly, 100) {
		t.Errorf("yearly bonus: %+v", p.Incomes[1])
	}
	if p.Expenses[0].Name != "Rent" || p.Expenses[0].Monthly != 900 || !p.Expenses[0].InflationIndexed {
		t.Errorf("rent: %+v", p.Expenses[0])
	}
}

func TestStreamDatesBecomePlanMonths(t *testing.T) {
	// As of 2000-01. A year alone means January for a start and December for an end.
	p := mustParse(t, `"securities": [`+stocks+`],
		"income": [
			{"name": "A", "amount": 1, "startYear": 2000, "startMonth": 4, "endYear": 2001},
			{"name": "B", "amount": 1, "startYear": 2001},
			{"name": "C", "amount": 1, "endYear": 1999, "endMonth": 6}
		]`)
	want := [][2]any{{3, 23}, {12, nil}, {nil, -7}}
	for i, s := range p.Incomes {
		if got := [2]any{value(s.Start), value(s.End)}; got != want[i] {
			t.Errorf("%s runs %v, want %v", s.Name, got, want[i])
		}
	}
}

func TestStreamAmountByPlanMonth(t *testing.T) {
	p := mustParse(t, `"securities": [`+stocks+`],
		"expenses": [
			{"name": "Indexed", "amount": 100, "startYear": 2000, "startMonth": 2, "endYear": 2000, "endMonth": 3},
			{"name": "Fixed", "amount": 100, "indexation": "none", "growthRate": 0.21}
		]`)
	indexed, fixed := p.Expenses[0], p.Expenses[1]
	for k, want := range []float64{0, 150, 150, 0} {
		if got := indexed.Amount(k, 1.5); !near(got, want) {
			t.Errorf("indexed.Amount(%d) = %v, want %v", k, got, want)
		}
	}
	// It grows 21% a year and ignores inflation.
	if got := fixed.Amount(24, 1.5); !near(got, 100*1.21*1.21) {
		t.Errorf("fixed.Amount(24) = %v", got)
	}
}

func TestItemsFromTheCustomerDataAPIAreAccepted(t *testing.T) {
	p := mustParse(t, `"securities": [`+stocks+`],
		"income": [{
			"id": "lz3k9abc12", "category": "income", "name": "Salary", "amount": 6500, "frequency": "monthly",
			"incomeType": "w2", "benefits": {"retirement": {"contributionRate": 0.1}}, "startYear": 2001,
			"createdAt": "2026-09-27T06:20:39.033Z", "updatedAt": "2026-09-27T06:20:39.100Z"
		}]`)
	s := p.Incomes[0]
	if s.Name != "Salary" || s.Monthly != 6500 || value(s.Start) != 12 {
		t.Errorf("got %+v", s)
	}
}

func TestHoldings(t *testing.T) {
	p := mustParse(t, `"securities": [
		{"name": "A", "symbol": "VTI", "proxy": ["VTSMX", "VFINX"], "shares": 10},
		{"name": "B", "symbol": "BND", "proxy": "VBMFX", "value": 7},
		{"name": "C", "series": "csv:fund.csv|yahoo:VFINX", "value": 5}
	]`)
	a, b, c := p.Holdings[0], p.Holdings[1], p.Holdings[2]
	if a.Series != "yahoo:VTI|yahoo:VTSMX|yahoo:VFINX" || a.Shares == nil || *a.Shares != 10 {
		t.Errorf("A: %+v", a)
	}
	if b.Series != "yahoo:BND|yahoo:VBMFX" || b.Value != 7 || b.Shares != nil {
		t.Errorf("B: %+v", b)
	}
	if c.Series != "csv:fund.csv|yahoo:VFINX" || c.Value != 5 {
		t.Errorf("C: %+v", c)
	}
}

func TestMortgagePaymentFromRemainingMonths(t *testing.T) {
	p := mustParse(t, `"realEstate": [
		{"name": "A", "value": 1, "mortgage": {"balance": 100000, "rate": 0.06, "remainingMonths": 360}},
		{"name": "B", "value": 1, "mortgage": {"balance": 1200, "rate": 0, "remainingMonths": 12}},
		{"name": "C", "value": 1, "mortgage": {"balance": 100000, "rate": 0.06, "payment": 700}}
	]`)
	for i, want := range []float64{599.5505, 100, 700} {
		if got := p.Properties[i].Mortgage.Payment; math.Abs(got-want) > 0.0001 {
			t.Errorf("%s pays %v a month, want %v", p.Properties[i].Name, got, want)
		}
	}
	if a := p.Properties[0]; a.PriceIndex != "fred:CSUSHPINSA" || a.RentIndex != "" || a.SaleMonth != nil {
		t.Errorf("defaults: %+v", a)
	}
}

func TestSale(t *testing.T) {
	p := mustParse(t, `"realEstate": [
		{"name": "A", "value": 1, "sale": {"year": 2001, "month": 3, "costRate": 0.05}},
		{"name": "B", "value": 1, "sale": {"year": 2000}}
	]`)
	a, b := p.Properties[0], p.Properties[1]
	if value(a.SaleMonth) != 14 || a.SaleCostRate != 0.05 {
		t.Errorf("A: %+v", a)
	}
	if value(b.SaleMonth) != 11 || b.SaleCostRate != 0.06 {
		t.Errorf("B: %+v", b)
	}
}

func TestPolicy(t *testing.T) {
	p := mustParse(t, `"securities": [`+stocks+`, {"name": "Bonds", "symbol": "BND", "value": 1}],
		"policy": {"rebalance": "quarterly", "targetWeights": {"Stocks": 3, "Bonds": 1}, "withdrawalOrder": ["Bonds"]}`)
	if p.RebalanceMonths != 3 || p.TargetWeights["Stocks"] != 3 || !slices.Equal(p.WithdrawalOrder, []string{"Bonds"}) {
		t.Errorf("got %+v", p)
	}
}

func TestInvalidPlans(t *testing.T) {
	securities := `"securities": [` + stocks + `], `
	for _, tc := range []struct{ fields, message string }{
		{`"name": "Empty"`, "at least one security or property"},
		{`"stocks": []`, `unknown field "stocks"`},
		{`"securities": [{"name": "A", "ticker": "X", "value": 1}]`, `unknown field "ticker"`},
		{`"securities": [{"name": "A", "symbol": "X", "value": "1"}]`, "cannot unmarshal string"},
		{`"securities": [` + stocks + `]} {"more": 1`, "unexpected content after the plan"},

		{`"securities": [{"name": "A", "value": 1}]`, "plan.securities[0] needs either symbol or series"},
		{`"securities": [{"name": "A", "symbol": "X", "series": "cash", "value": 1}]`, "needs either symbol or series"},
		{`"securities": [{"name": "A", "symbol": "X"}]`, "plan.securities[0] needs either value or shares"},
		{`"securities": [{"name": "A", "series": "cash", "shares": 1}]`, "shares needs symbol"},
		{`"securities": [{"name": "A", "series": "cash", "proxy": "X", "value": 1}]`, "proxy needs symbol"},
		{`"securities": [{"name": "A", "symbol": "X", "proxy": 5, "value": 1}]`, "proxy must be a string or a list of strings"},
		{`"securities": [{"name": "A", "symbol": "X", "value": -1}]`, "plan.securities[0].value must be at least 0"},
		{`"securities": [{"name": " ", "symbol": "X", "value": 1}]`, "plan.securities[0].name must not be blank"},
		{`"securities": [` + stocks + `, ` + stocks + `]`, "names must be unique"},

		{securities + `"income": [{"name": "A"}]`, "plan.income[0].amount is required"},
		{securities + `"income": [{"amount": 1}]`, "plan.income[0].name is required"},
		{securities + `"income": [{"name": "A", "amount": -1}]`, "amount must be at least 0"},
		{securities + `"income": [{"name": "A", "amount": 1, "frequency": "daily"}]`, "frequency must be one of: weekly, biweekly"},
		{securities + `"income": [{"name": "A", "amount": 1, "indexation": "cpi"}]`, "indexation must be one of: inflation, none"},
		{securities + `"income": [{"name": "A", "amount": 1, "startMonth": 2}]`, "plan.income[0].startMonth requires startYear"},
		{securities + `"income": [{"name": "A", "amount": 1, "startYear": 1850}]`, "startYear must be between 1900 and 2150"},
		{securities + `"income": [{"name": "A", "amount": 1, "endYear": 2001, "endMonth": 13}]`, "endMonth must be between 1 and 12"},
		{securities + `"income": [{"name": "A", "amount": 1, "startYear": 2002, "endYear": 2001}]`, "end must not be before start"},
		{securities + `"income": [{"name": "A", "amount": 1, "taxRate": 1.5}]`, "taxRate must be at most 1"},
		{securities + `"income": [{"name": "A", "amount": 1, "growthRate": 0.6}]`, "growthRate must be at most 0.5"},
		{securities + `"expenses": [{"name": "A", "amount": 1, "taxRate": 0.1}]`, "plan.expenses[0].taxRate is only for income"},

		{`"realEstate": [{"name": "A"}]`, "plan.realEstate[0].value is required"},
		{`"realEstate": [{"name": "A", "value": 1, "vacancyRate": 2}]`, "vacancyRate must be at most 1"},
		{`"realEstate": [{"name": "A", "value": 1, "mortgage": {"balance": 100000, "rate": 0.06, "payment": 500}}]`, "doesn't cover the interest"},
		{`"realEstate": [{"name": "A", "value": 1, "mortgage": {"balance": 1, "rate": 0.06}}]`, "needs either payment or remainingMonths"},
		{`"realEstate": [{"name": "A", "value": 1, "mortgage": {"rate": 0.06, "payment": 1}}]`, "mortgage.balance is required"},
		{`"realEstate": [{"name": "A", "value": 1, "mortgage": {"balance": 1, "rate": 0.06, "remainingMonths": 0}}]`, "remainingMonths must be between 1 and 600"},
		{`"realEstate": [{"name": "A", "value": 1, "sale": {"year": 1999}}]`, "sale must not be before the plan's asOf month (2000-01)"},
		{`"realEstate": [{"name": "A", "value": 1, "sale": {"month": 3}}]`, "sale.year is required"},

		{securities + `"policy": {"rebalance": "daily"}`, "rebalance must be one of: never, monthly, quarterly, yearly"},
		{securities + `"policy": {"withdrawalOrder": ["Bonds"]}`, `withdrawalOrder: no security named "Bonds"`},
		{securities + `"policy": {"withdrawalOrder": ["Stocks", "Stocks"]}`, "lists a security twice"},
		{securities + `"policy": {"targetWeights": {"Bonds": 1}}`, `targetWeights: no security named "Bonds"`},
		{securities + `"policy": {"targetWeights": {"Stocks": 0}}`, "weight above zero"},
		{securities + `"policy": {"targetWeights": {"Stocks": -1}}`, "targetWeights.Stocks must be at least 0"},
	} {
		_, err := parse(tc.fields)
		if err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Errorf("%s\n\terror = %v, want one mentioning %q", tc.fields, err, tc.message)
		}
	}
	if _, err := Parse([]byte(`{"asOf": "October 2026", "securities": [`+stocks+`]}`), time.Time{}); err == nil || !strings.Contains(err.Error(), "asOf must be a month") {
		t.Errorf("a bad asOf: %v", err)
	}
}
