package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/mustard-money/financial-planning/calendar"
)

// Frequencies lists, in display order, how often a stream can recur.
var Frequencies = []string{"weekly", "biweekly", "semimonthly", "monthly", "quarterly", "semiannually", "yearly"}

// perMonth is occurrences per month on average; matches PerMonth in the
// website's src/data/frequencies.ts.
var perMonth = map[string]float64{
	"weekly":       52.0 / 12,
	"biweekly":     26.0 / 12,
	"semimonthly":  2,
	"monthly":      1,
	"quarterly":    1.0 / 3,
	"semiannually": 1.0 / 6,
	"yearly":       1.0 / 12,
}

// PerMonth is how many times a frequency occurs in a month on average, and
// whether it is a known frequency.
func PerMonth(frequency string) (float64, bool) {
	n, ok := perMonth[frequency]
	return n, ok
}

var rebalanceChoices = []string{"never", "monthly", "quarterly", "yearly"}

var rebalanceMonths = map[string]int{"never": 0, "monthly": 1, "quarterly": 3, "yearly": 12}

const minYear, maxYear = 1900, 2150

var noLimit = math.Inf(1)

// The JSON shapes. Pointer fields distinguish absent from zero so validation
// can report missing fields; the decoder rejects unknown ones.
type planInput struct {
	Name           *string         `json:"name"`
	AsOf           *string         `json:"asOf"`
	InflationIndex *string         `json:"inflationIndex"`
	Income         []streamInput   `json:"income"`
	Expenses       []streamInput   `json:"expenses"`
	Securities     []holdingInput  `json:"securities"`
	RealEstate     []propertyInput `json:"realEstate"`
	Policy         *policyInput    `json:"policy"`
}

type streamInput struct {
	Name       *string  `json:"name"`
	Amount     *float64 `json:"amount"`
	Frequency  *string  `json:"frequency"`
	StartYear  *int     `json:"startYear"`
	StartMonth *int     `json:"startMonth"`
	EndYear    *int     `json:"endYear"`
	EndMonth   *int     `json:"endMonth"`
	Indexation *string  `json:"indexation"`
	GrowthRate *float64 `json:"growthRate"`
	TaxRate    *float64 `json:"taxRate"`

	// Item fields from the customer-data API that don't affect a backtest.
	ID         json.RawMessage `json:"id"`
	Category   json.RawMessage `json:"category"`
	IncomeType json.RawMessage `json:"incomeType"`
	Benefits   json.RawMessage `json:"benefits"`
	CreatedAt  json.RawMessage `json:"createdAt"`
	UpdatedAt  json.RawMessage `json:"updatedAt"`
}

type holdingInput struct {
	Name   *string    `json:"name"`
	Symbol *string    `json:"symbol"`
	Proxy  stringList `json:"proxy"`
	Series *string    `json:"series"`
	Value  *float64   `json:"value"`
	Shares *float64   `json:"shares"`
}

type propertyInput struct {
	Name              *string        `json:"name"`
	Value             *float64       `json:"value"`
	PriceIndex        *string        `json:"priceIndex"`
	MonthlyRent       *float64       `json:"monthlyRent"`
	RentIndex         *string        `json:"rentIndex"`
	VacancyRate       *float64       `json:"vacancyRate"`
	ManagementFeeRate *float64       `json:"managementFeeRate"`
	AnnualCostRate    *float64       `json:"annualCostRate"`
	MonthlyCosts      *float64       `json:"monthlyCosts"`
	Mortgage          *mortgageInput `json:"mortgage"`
	Sale              *saleInput     `json:"sale"`
}

type mortgageInput struct {
	Balance         *float64 `json:"balance"`
	Rate            *float64 `json:"rate"`
	Payment         *float64 `json:"payment"`
	RemainingMonths *int     `json:"remainingMonths"`
}

type saleInput struct {
	Year     *int     `json:"year"`
	Month    *int     `json:"month"`
	CostRate *float64 `json:"costRate"`
}

type policyInput struct {
	Rebalance       *string            `json:"rebalance"`
	TargetWeights   map[string]float64 `json:"targetWeights"`
	WithdrawalOrder []string           `json:"withdrawalOrder"`
}

// stringList is a list of strings; a single string counts as a list of one.
type stringList []string

func (l *stringList) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var one string
	if json.Unmarshal(data, &one) == nil {
		*l = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return errors.New("proxy must be a string or a list of strings")
	}
	*l = many
	return nil
}

// Load reads and validates the plan in a JSON file.
func Load(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the plan: %w", err)
	}
	return Parse(data, time.Now())
}

// Parse validates a plan's JSON. asOf defaults to the month of today.
func Parse(data []byte, today time.Time) (*Plan, error) {
	var in planInput
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return nil, fmt.Errorf("plan: %s", strings.TrimPrefix(err.Error(), "json: "))
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("plan: unexpected content after the plan")
	}
	return in.validate(today)
}

func (in planInput) validate(today time.Time) (*Plan, error) {
	p := &Plan{Name: "Plan", AsOf: calendar.MonthOf(today), InflationIndex: DefaultInflationIndex}
	var err error
	if in.Name != nil {
		if p.Name, err = text(in.Name, "plan.name"); err != nil {
			return nil, err
		}
	}
	if in.AsOf != nil {
		if p.AsOf, err = calendar.ParseMonth(*in.AsOf); err != nil {
			return nil, errors.New("plan.asOf must be a month like 2026-10")
		}
	}
	if in.InflationIndex != nil {
		if p.InflationIndex, err = text(in.InflationIndex, "plan.inflationIndex"); err != nil {
			return nil, err
		}
	}
	for i, s := range in.Income {
		stream, err := s.validate(fmt.Sprintf("plan.income[%d]", i), p.AsOf, true)
		if err != nil {
			return nil, err
		}
		p.Incomes = append(p.Incomes, stream)
	}
	for i, s := range in.Expenses {
		stream, err := s.validate(fmt.Sprintf("plan.expenses[%d]", i), p.AsOf, false)
		if err != nil {
			return nil, err
		}
		p.Expenses = append(p.Expenses, stream)
	}
	var names []string
	for i, h := range in.Securities {
		holding, err := h.validate(fmt.Sprintf("plan.securities[%d]", i))
		if err != nil {
			return nil, err
		}
		if slices.Contains(names, holding.Name) {
			return nil, errors.New("plan.securities: names must be unique")
		}
		names = append(names, holding.Name)
		p.Holdings = append(p.Holdings, holding)
	}
	for i, r := range in.RealEstate {
		property, err := r.validate(fmt.Sprintf("plan.realEstate[%d]", i), p.AsOf)
		if err != nil {
			return nil, err
		}
		p.Properties = append(p.Properties, property)
	}
	if in.Policy != nil {
		if err := in.Policy.apply(p, names); err != nil {
			return nil, err
		}
	}
	if len(p.Holdings) == 0 && len(p.Properties) == 0 {
		return nil, errors.New("plan needs at least one security or property")
	}
	return p, nil
}

func (in streamInput) validate(path string, asOf calendar.Month, income bool) (Stream, error) {
	s := Stream{}
	var err error
	if s.Name, err = text(in.Name, path+".name"); err != nil {
		return s, err
	}
	amount, err := required(in.Amount, path+".amount", 0, noLimit)
	if err != nil {
		return s, err
	}
	frequency, err := choice(in.Frequency, path+".frequency", "monthly", Frequencies)
	if err != nil {
		return s, err
	}
	s.Monthly = amount * perMonth[frequency]
	if s.Start, err = planMonth(in.StartYear, in.StartMonth, path+".start", 1, asOf); err != nil {
		return s, err
	}
	if s.End, err = planMonth(in.EndYear, in.EndMonth, path+".end", 12, asOf); err != nil {
		return s, err
	}
	if s.Start != nil && s.End != nil && *s.End < *s.Start {
		return s, fmt.Errorf("%s: end must not be before start", path)
	}
	indexation, err := choice(in.Indexation, path+".indexation", "inflation", []string{"inflation", "none"})
	if err != nil {
		return s, err
	}
	s.InflationIndexed = indexation == "inflation"
	if s.GrowthRate, err = optional(in.GrowthRate, path+".growthRate", 0, -0.5, 0.5); err != nil {
		return s, err
	}
	if !income && in.TaxRate != nil {
		return s, fmt.Errorf("%s.taxRate is only for income", path)
	}
	if s.TaxRate, err = optional(in.TaxRate, path+".taxRate", 0, 0, 1); err != nil {
		return s, err
	}
	return s, nil
}

// planMonth turns a year and optional month into a plan month (months after
// as-of). path is the field prefix, e.g. "plan.income[0].start".
func planMonth(year, month *int, path string, defaultMonth int, asOf calendar.Month) (*int, error) {
	if year == nil {
		if month != nil {
			return nil, fmt.Errorf("%sMonth requires %sYear", path, path[strings.LastIndex(path, ".")+1:])
		}
		return nil, nil
	}
	if err := between(*year, path+"Year", minYear, maxYear); err != nil {
		return nil, err
	}
	m := defaultMonth
	if month != nil {
		if err := between(*month, path+"Month", 1, 12); err != nil {
			return nil, err
		}
		m = *month
	}
	offset := calendar.NewMonth(*year, m).Sub(asOf)
	return &offset, nil
}

func (in holdingInput) validate(path string) (Holding, error) {
	h := Holding{}
	var err error
	if h.Name, err = text(in.Name, path+".name"); err != nil {
		return h, err
	}
	if (in.Symbol == nil) == (in.Series == nil) {
		return h, fmt.Errorf("%s needs either symbol or series", path)
	}
	if (in.Value == nil) == (in.Shares == nil) {
		return h, fmt.Errorf("%s needs either value or shares", path)
	}
	if in.Symbol == nil {
		if len(in.Proxy) > 0 {
			return h, fmt.Errorf(`%s.proxy needs symbol; with series, write "series|earlier-series"`, path)
		}
		if in.Shares != nil {
			return h, fmt.Errorf("%s.shares needs symbol, which gives the share price", path)
		}
		if h.Series, err = text(in.Series, path+".series"); err != nil {
			return h, err
		}
	} else {
		for _, ticker := range append([]string{*in.Symbol}, in.Proxy...) {
			if strings.TrimSpace(ticker) == "" {
				return h, fmt.Errorf("%s: symbol and proxy must not be blank", path)
			}
			h.Series += "|yahoo:" + strings.TrimSpace(ticker)
		}
		h.Series = h.Series[1:]
	}
	if in.Shares != nil {
		shares, err := optional(in.Shares, path+".shares", 0, 0, noLimit)
		if err != nil {
			return h, err
		}
		h.Shares = &shares
		return h, nil
	}
	h.Value, err = optional(in.Value, path+".value", 0, 0, noLimit)
	return h, err
}

func (in propertyInput) validate(path string, asOf calendar.Month) (Property, error) {
	p := Property{PriceIndex: DefaultPriceIndex, SaleCostRate: DefaultSaleCostRate}
	var err error
	if p.Name, err = text(in.Name, path+".name"); err != nil {
		return p, err
	}
	if p.Value, err = required(in.Value, path+".value", 0, noLimit); err != nil {
		return p, err
	}
	if in.PriceIndex != nil {
		if p.PriceIndex, err = text(in.PriceIndex, path+".priceIndex"); err != nil {
			return p, err
		}
	}
	if in.RentIndex != nil {
		if p.RentIndex, err = text(in.RentIndex, path+".rentIndex"); err != nil {
			return p, err
		}
	}
	for _, field := range []struct {
		value *float64
		name  string
		max   float64
		into  *float64
	}{
		{in.MonthlyRent, "monthlyRent", noLimit, &p.MonthlyRent},
		{in.VacancyRate, "vacancyRate", 1, &p.VacancyRate},
		{in.ManagementFeeRate, "managementFeeRate", 1, &p.ManagementFeeRate},
		{in.AnnualCostRate, "annualCostRate", 1, &p.AnnualCostRate},
		{in.MonthlyCosts, "monthlyCosts", noLimit, &p.MonthlyCosts},
	} {
		if *field.into, err = optional(field.value, path+"."+field.name, 0, 0, field.max); err != nil {
			return p, err
		}
	}
	if in.Mortgage != nil {
		if p.Mortgage, err = in.Mortgage.validate(path + ".mortgage"); err != nil {
			return p, err
		}
	}
	if in.Sale != nil {
		if err := in.Sale.apply(&p, path+".sale", asOf); err != nil {
			return p, err
		}
	}
	return p, nil
}

// validate checks a fixed-rate loan, given its monthly payment or the months
// left on it.
func (in mortgageInput) validate(path string) (*Mortgage, error) {
	balance, err := required(in.Balance, path+".balance", 0, noLimit)
	if err != nil {
		return nil, err
	}
	rate, err := required(in.Rate, path+".rate", 0, 1)
	if err != nil {
		return nil, err
	}
	if (in.Payment == nil) == (in.RemainingMonths == nil) {
		return nil, fmt.Errorf("%s needs either payment or remainingMonths", path)
	}
	monthlyRate := rate / 12
	var payment float64
	if in.Payment == nil {
		if err := between(*in.RemainingMonths, path+".remainingMonths", 1, 600); err != nil {
			return nil, err
		}
		months := float64(*in.RemainingMonths)
		if monthlyRate == 0 {
			payment = balance / months
		} else {
			payment = balance * monthlyRate / (1 - math.Pow(1+monthlyRate, -months))
		}
	} else {
		if payment, err = optional(in.Payment, path+".payment", 0, 0, noLimit); err != nil {
			return nil, err
		}
		if balance > 0 && payment <= balance*monthlyRate {
			return nil, fmt.Errorf("%s.payment doesn't cover the interest, so the loan would never be repaid", path)
		}
	}
	return &Mortgage{Balance: balance, Rate: rate, Payment: payment}, nil
}

func (in saleInput) apply(p *Property, path string, asOf calendar.Month) error {
	if in.Year == nil {
		return fmt.Errorf("%s.year is required", path)
	}
	if err := between(*in.Year, path+".year", minYear, maxYear); err != nil {
		return err
	}
	month := 12
	if in.Month != nil {
		if err := between(*in.Month, path+".month", 1, 12); err != nil {
			return err
		}
		month = *in.Month
	}
	var err error
	if p.SaleCostRate, err = optional(in.CostRate, path+".costRate", DefaultSaleCostRate, 0, 1); err != nil {
		return err
	}
	offset := calendar.NewMonth(*in.Year, month).Sub(asOf)
	if offset < 0 {
		return fmt.Errorf("%s must not be before the plan's asOf month (%s)", path, asOf)
	}
	p.SaleMonth = &offset
	return nil
}

func (in policyInput) apply(p *Plan, names []string) error {
	rebalance, err := choice(in.Rebalance, "plan.policy.rebalance", "never", rebalanceChoices)
	if err != nil {
		return err
	}
	p.RebalanceMonths = rebalanceMonths[rebalance]
	for i, name := range in.WithdrawalOrder {
		if !slices.Contains(names, name) {
			return fmt.Errorf("plan.policy.withdrawalOrder: no security named %q", name)
		}
		if slices.Contains(in.WithdrawalOrder[:i], name) {
			return errors.New("plan.policy.withdrawalOrder lists a security twice")
		}
	}
	p.WithdrawalOrder = in.WithdrawalOrder
	if in.TargetWeights != nil {
		total := 0.0
		for name, weight := range in.TargetWeights {
			if !slices.Contains(names, name) {
				return fmt.Errorf("plan.policy.targetWeights: no security named %q", name)
			}
			if weight < 0 {
				return fmt.Errorf("plan.policy.targetWeights.%s must be at least 0", name)
			}
			total += weight
		}
		if total <= 0 {
			return errors.New("plan.policy.targetWeights must have a weight above zero")
		}
		p.TargetWeights = in.TargetWeights
	}
	return nil
}

// text checks a required string and trims it.
func text(v *string, path string) (string, error) {
	if v == nil {
		return "", fmt.Errorf("%s is required", path)
	}
	s := strings.TrimSpace(*v)
	if s == "" {
		return "", fmt.Errorf("%s must not be blank", path)
	}
	return s, nil
}

// optional checks a number against its bounds; def stands in when it is absent.
func optional(v *float64, path string, def, lo, hi float64) (float64, error) {
	if v == nil {
		return def, nil
	}
	if *v < lo {
		return 0, fmt.Errorf("%s must be at least %v", path, lo)
	}
	if *v > hi {
		return 0, fmt.Errorf("%s must be at most %v", path, hi)
	}
	return *v, nil
}

func required(v *float64, path string, lo, hi float64) (float64, error) {
	if v == nil {
		return 0, fmt.Errorf("%s is required", path)
	}
	return optional(v, path, 0, lo, hi)
}

func between(v int, path string, lo, hi int) error {
	if v < lo || v > hi {
		return fmt.Errorf("%s must be between %d and %d", path, lo, hi)
	}
	return nil
}

func choice(v *string, path, def string, allowed []string) (string, error) {
	if v == nil {
		return def, nil
	}
	if !slices.Contains(allowed, *v) {
		return "", fmt.Errorf("%s must be one of: %s", path, strings.Join(allowed, ", "))
	}
	return *v, nil
}
