package report

import (
	"encoding/csv"
	"io"
	"math"
	"strconv"

	"github.com/mustard-money/financial-planning/backtest"
	"github.com/mustard-money/financial-planning/calendar"
)

// The JSON shapes. Money is rounded to cents; rates are left as computed.

type summaryJSON struct {
	Start                 string  `json:"start"`
	End                   string  `json:"end"`
	Months                int     `json:"months"`
	Covered               bool    `json:"covered"`
	ShortfallMonth        *string `json:"shortfallMonth"`
	MaxUnfunded           float64 `json:"maxUnfunded"`
	StartNetWorth         float64 `json:"startNetWorth"`
	EndNetWorth           float64 `json:"endNetWorth"`
	EndNetWorthReal       float64 `json:"endNetWorthReal"`
	StartSecurities       float64 `json:"startSecurities"`
	EndSecurities         float64 `json:"endSecurities"`
	StartRealEstateEquity float64 `json:"startRealEstateEquity"`
	EndRealEstateEquity   float64 `json:"endRealEstateEquity"`
	PortfolioReturn       float64 `json:"portfolioReturn"`
	PortfolioReturnReal   float64 `json:"portfolioReturnReal"`
	PortfolioVolatility   float64 `json:"portfolioVolatility"`
	MaxDrawdown           float64 `json:"maxDrawdown"`
	DrawdownPeak          *string `json:"drawdownPeak"`
	DrawdownTrough        *string `json:"drawdownTrough"`
	Inflation             float64 `json:"inflation"`
	Income                float64 `json:"income"`
	Taxes                 float64 `json:"taxes"`
	Expenses              float64 `json:"expenses"`
	PropertyCashFlow      float64 `json:"propertyCashFlow"`
	SaleProceeds          float64 `json:"saleProceeds"`
	Contributed           float64 `json:"contributed"`
	Withdrawn             float64 `json:"withdrawn"`
}

type monthJSON struct {
	Month            string    `json:"month"`
	NetWorth         float64   `json:"netWorth"`
	RealNetWorth     float64   `json:"realNetWorth"`
	Securities       float64   `json:"securities"`
	Holdings         []float64 `json:"holdings"`
	RealEstateValue  float64   `json:"realEstateValue"`
	MortgageBalance  float64   `json:"mortgageBalance"`
	Unfunded         float64   `json:"unfunded"`
	Income           float64   `json:"income"`
	Taxes            float64   `json:"taxes"`
	Expenses         float64   `json:"expenses"`
	PropertyCashFlow float64   `json:"propertyCashFlow"`
	SaleProceeds     float64   `json:"saleProceeds"`
	Contributed      float64   `json:"contributed"`
	Withdrawn        float64   `json:"withdrawn"`
	PortfolioReturn  float64   `json:"portfolioReturn"`
	PriceLevel       float64   `json:"priceLevel"`
}

type runJSON struct {
	Name     string      `json:"name"`
	AsOf     string      `json:"asOf"`
	Summary  summaryJSON `json:"summary"`
	Holdings []string    `json:"holdings"`
	Months   []monthJSON `json:"months"`
}

type spreadJSON struct {
	Worst float64 `json:"worst"`
	P10   float64 `json:"p10"`
	P25   float64 `json:"p25"`
	P50   float64 `json:"p50"`
	P75   float64 `json:"p75"`
	P90   float64 `json:"p90"`
	Best  float64 `json:"best"`
}

type rollingJSON struct {
	Name            string        `json:"name"`
	AsOf            string        `json:"asOf"`
	Months          int           `json:"months"`
	SuccessRate     float64       `json:"successRate"`
	EndNetWorthReal spreadJSON    `json:"endNetWorthReal"`
	Periods         []summaryJSON `json:"periods"`
}

// RunJSON is the value to marshal for one run: its summary and every month.
func RunJSON(result *backtest.Result, summary backtest.Summary) any {
	out := runJSON{
		Name:     result.Plan.Name,
		AsOf:     result.Plan.AsOf.String(),
		Summary:  summaryOf(summary),
		Holdings: result.HoldingNames,
		Months:   make([]monthJSON, len(result.Months)),
	}
	for i, r := range result.Months {
		holdings := make([]float64, len(r.Holdings))
		for j, v := range r.Holdings {
			holdings[j] = cents(v)
		}
		out.Months[i] = monthJSON{
			Month:            r.Month.String(),
			NetWorth:         cents(r.NetWorth()),
			RealNetWorth:     cents(r.RealNetWorth()),
			Securities:       cents(r.Securities()),
			Holdings:         holdings,
			RealEstateValue:  cents(r.RealEstateValue),
			MortgageBalance:  cents(r.MortgageBalance),
			Unfunded:         cents(r.Unfunded),
			Income:           cents(r.Income),
			Taxes:            cents(r.Taxes),
			Expenses:         cents(r.Expenses),
			PropertyCashFlow: cents(r.PropertyCashFlow),
			SaleProceeds:     cents(r.SaleProceeds),
			Contributed:      cents(r.Contributed),
			Withdrawn:        cents(r.Withdrawn),
			PortfolioReturn:  r.PortfolioReturn,
			PriceLevel:       r.PriceLevel,
		}
	}
	return out
}

// RollingJSON is the value to marshal for a rolling analysis: the spread of
// outcomes and every period's summary.
func RollingJSON(rolling *backtest.Rolling) any {
	out := rollingJSON{
		Name:        rolling.Plan.Name,
		AsOf:        rolling.Plan.AsOf.String(),
		Months:      rolling.Months,
		SuccessRate: rolling.SuccessRate(),
		EndNetWorthReal: spreadJSON{
			Worst: cents(rolling.Worst().EndNetWorthReal),
			P10:   cents(rolling.EndNetWorthReal(10)),
			P25:   cents(rolling.EndNetWorthReal(25)),
			P50:   cents(rolling.EndNetWorthReal(50)),
			P75:   cents(rolling.EndNetWorthReal(75)),
			P90:   cents(rolling.EndNetWorthReal(90)),
			Best:  cents(rolling.Best().EndNetWorthReal),
		},
		Periods: make([]summaryJSON, len(rolling.Periods)),
	}
	for i, p := range rolling.Periods {
		out.Periods[i] = summaryOf(p)
	}
	return out
}

func summaryOf(s backtest.Summary) summaryJSON {
	return summaryJSON{
		Start:                 s.Start.String(),
		End:                   s.End.String(),
		Months:                s.Months,
		Covered:               s.Covered(),
		ShortfallMonth:        monthString(s.ShortfallMonth),
		MaxUnfunded:           cents(s.MaxUnfunded),
		StartNetWorth:         cents(s.StartNetWorth),
		EndNetWorth:           cents(s.EndNetWorth),
		EndNetWorthReal:       cents(s.EndNetWorthReal),
		StartSecurities:       cents(s.StartSecurities),
		EndSecurities:         cents(s.EndSecurities),
		StartRealEstateEquity: cents(s.StartRealEstateEquity),
		EndRealEstateEquity:   cents(s.EndRealEstateEquity),
		PortfolioReturn:       s.PortfolioReturn,
		PortfolioReturnReal:   s.PortfolioReturnReal,
		PortfolioVolatility:   s.PortfolioVolatility,
		MaxDrawdown:           s.MaxDrawdown,
		DrawdownPeak:          monthString(s.DrawdownPeak),
		DrawdownTrough:        monthString(s.DrawdownTrough),
		Inflation:             s.Inflation,
		Income:                cents(s.Income),
		Taxes:                 cents(s.Taxes),
		Expenses:              cents(s.Expenses),
		PropertyCashFlow:      cents(s.PropertyCashFlow),
		SaleProceeds:          cents(s.SaleProceeds),
		Contributed:           cents(s.Contributed),
		Withdrawn:             cents(s.Withdrawn),
	}
}

func monthString(m *calendar.Month) *string {
	if m == nil {
		return nil
	}
	s := m.String()
	return &s
}

func cents(v float64) float64 {
	return math.Round(v*100) / 100
}

// WriteRunCSV writes one row per month: balances at month end, flows during
// the month.
func WriteRunCSV(w io.Writer, result *backtest.Result) error {
	header := []string{
		"month", "planMonth", "netWorth", "realNetWorth", "securities", "realEstateValue", "mortgageBalance",
		"unfunded", "income", "taxes", "expenses", "propertyCashFlow", "saleProceeds", "contributed",
		"withdrawn", "portfolioReturn", "priceLevel",
	}
	for _, name := range result.HoldingNames {
		header = append(header, "holding:"+name)
	}
	out := csv.NewWriter(w)
	out.Write(header)
	for k, r := range result.Months {
		row := []string{r.Month.String(), result.Plan.AsOf.Add(k).String()}
		for _, v := range []float64{
			r.NetWorth(), r.RealNetWorth(), r.Securities(), r.RealEstateValue, r.MortgageBalance, r.Unfunded,
			r.Income, r.Taxes, r.Expenses, r.PropertyCashFlow, r.SaleProceeds, r.Contributed, r.Withdrawn,
		} {
			row = append(row, fixed(v, 2))
		}
		row = append(row, fixed(r.PortfolioReturn, 6), fixed(r.PriceLevel, 6))
		for _, v := range r.Holdings {
			row = append(row, fixed(v, 2))
		}
		out.Write(row)
	}
	out.Flush()
	return out.Error()
}

// WriteRollingCSV writes one row per period.
func WriteRollingCSV(w io.Writer, rolling *backtest.Rolling) error {
	out := csv.NewWriter(w)
	out.Write([]string{
		"start", "end", "covered", "shortfallMonth", "endNetWorth", "endNetWorthReal", "portfolioReturn",
		"portfolioReturnReal", "maxDrawdown", "inflation",
	})
	for _, p := range rolling.Periods {
		shortfall := ""
		if p.ShortfallMonth != nil {
			shortfall = p.ShortfallMonth.String()
		}
		out.Write([]string{
			p.Start.String(), p.End.String(), strconv.FormatBool(p.Covered()), shortfall,
			fixed(p.EndNetWorth, 2), fixed(p.EndNetWorthReal, 2), fixed(p.PortfolioReturn, 6),
			fixed(p.PortfolioReturnReal, 6), fixed(p.MaxDrawdown, 6), fixed(p.Inflation, 6),
		})
	}
	out.Flush()
	return out.Error()
}

func fixed(v float64, decimals int) string {
	return strconv.FormatFloat(v, 'f', decimals, 64)
}
