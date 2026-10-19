// Package scenario backtests a Mustard what-if scenario: the user's chosen
// income, expenses, investments and life events, run through market history
// instead of an assumed yearly return.
//
// A request carries the scenario and the user's items in the shapes the
// customer-data API returns them, so whatever holds those records can pass
// them on unchanged. Each investment is backtested as its own ticker when it
// names one, and otherwise as an index-tracking ETF chosen from its account
// and name.
package scenario

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/mustard-money/financial-planning/plan"
)

// Request is one scenario to backtest.
type Request struct {
	Scenario Scenario `json:"scenario"`
	// Items are the user's items; the scenario's itemIds pick the ones that
	// count, so passing all of them is fine.
	Items   []Item  `json:"items"`
	Profile Profile `json:"profile"`
	// Today is the date the amounts are current on, YYYY-MM-DD; default now.
	Today string `json:"today"`
	// TaxRate is the share of earned income and of withdrawals from pre-tax
	// accounts that goes to tax. The backtest has no tax tables: the caller
	// works out an effective rate. Default 0.
	TaxRate float64 `json:"taxRate"`
}

// Scenario is the customer-data API's scenario.
type Scenario struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	ItemIDs    []string   `json:"itemIds"`
	Events     []Event    `json:"events"`
	Parameters Parameters `json:"parameters"`
}

// Event kinds.
const (
	OneTimeCost   = "oneTimeCost"
	OneTimeIncome = "oneTimeIncome"
	IncomePause   = "incomePause"
	RecurringCost = "recurringCost"
)

// Event is a life event; amounts are in today's dollars.
type Event struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	Name   string  `json:"name"`
	Year   int     `json:"year"`   // of the scenario; 1 is the first
	Amount float64 `json:"amount"` // per year for recurringCost
	Months *int    `json:"months"` // incomePause only
	Years  *int    `json:"years"`  // recurringCost only
}

// Parameters are the scenario's assumptions. A backtest takes returns and
// inflation from history, so ReturnRate is unused and Inflation only turns
// IncomeGrowth into growth above inflation.
type Parameters struct {
	Years             int     `json:"years"`
	ReturnRate        float64 `json:"returnRate"`
	Inflation         float64 `json:"inflation"`
	IncomeGrowth      float64 `json:"incomeGrowth"`
	IncomeAdjustment  float64 `json:"incomeAdjustment"`
	ExpenseAdjustment float64 `json:"expenseAdjustment"`
}

// Item categories.
const (
	Income      = "income"
	Investments = "investments"
	Expenses    = "expenses"
)

// Item is the customer-data API's item, with the fields a backtest reads.
type Item struct {
	ID        string   `json:"id"`
	Category  string   `json:"category"`
	Name      string   `json:"name"`
	Amount    float64  `json:"amount"`
	Frequency *string  `json:"frequency"`
	Benefits  *Benefit `json:"benefits"`

	// Income: when it starts and ends.
	StartYear  *int `json:"startYear"`
	StartMonth *int `json:"startMonth"`
	EndYear    *int `json:"endYear"`
	EndMonth   *int `json:"endMonth"`

	// Investments.
	Account    *string  `json:"account"`
	AsOfYear   *int     `json:"asOfYear"`
	AsOfMonth  *int     `json:"asOfMonth"`
	ReturnRate *float64 `json:"returnRate"`
	// Ticker is the security the investment is held in, as Yahoo Finance
	// writes it (VTI, BRK-B). The API has no such field yet; an investment
	// without one is backtested as an index ETF.
	Ticker               *string  `json:"ticker"`
	WithdrawalStartYear  *int     `json:"withdrawalStartYear"`
	WithdrawalStartMonth *int     `json:"withdrawalStartMonth"`
	WithdrawalRate       *float64 `json:"withdrawalRate"`
}

// Benefit is the workplace savings on a W-2 income; only the balances
// matter here, as investments the scenario starts with.
type Benefit struct {
	Retirement *WorkplacePlan `json:"retirement"`
	Plan457    *WorkplacePlan `json:"plan457"`
	HSA        *WorkplacePlan `json:"hsa"`
}

// WorkplacePlan is one workplace account.
type WorkplacePlan struct {
	Roth    bool     `json:"roth"`
	Balance *Balance `json:"balance"`
}

// Balance is an account's balance on a date.
type Balance struct {
	Amount    float64 `json:"amount"`
	AsOfYear  *int    `json:"asOfYear"`
	AsOfMonth *int    `json:"asOfMonth"`
}

// Profile is the customer-data API's account profile.
type Profile struct {
	ExpectedReturn *float64 `json:"expectedReturn"`
}

// DefaultExpectedReturn matches DefaultExpectedReturn in the website's
// src/data/assumptions.ts.
const DefaultExpectedReturn = 0.07

// Parse reads and checks a request. Fields it doesn't know are ignored, so
// records can be passed as the API returns them.
func Parse(data []byte) (*Request, error) {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("request: %s", strings.TrimPrefix(err.Error(), "json: "))
	}
	if err := req.validate(); err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	return &req, nil
}

// today is the date the request's amounts are current on.
func (r *Request) today(now time.Time) (time.Time, error) {
	if r.Today == "" {
		return now, nil
	}
	return time.Parse(time.DateOnly, r.Today)
}

func (r *Request) validate() error {
	p := r.Scenario.Parameters
	switch {
	case p.Years < 1 || p.Years > 60:
		return fmt.Errorf("scenario.parameters.years must be between 1 and 60")
	case p.Inflation <= -1 || p.IncomeGrowth <= -1 || p.IncomeAdjustment < -1 || p.ExpenseAdjustment < -1:
		return fmt.Errorf("scenario.parameters has a rate below -100%%")
	case r.TaxRate < 0 || r.TaxRate > 1:
		return fmt.Errorf("taxRate must be between 0 and 1")
	}
	if _, err := r.today(time.Time{}); err != nil {
		return fmt.Errorf("today must be a date like 2026-10-04")
	}
	for i, e := range r.Scenario.Events {
		path := fmt.Sprintf("scenario.events[%d]", i)
		switch {
		case !slices.Contains([]string{OneTimeCost, OneTimeIncome, IncomePause, RecurringCost}, e.Kind):
			return fmt.Errorf("%s.kind %q is not a kind of event", path, e.Kind)
		case e.Year < 1:
			return fmt.Errorf("%s.year must be at least 1", path)
		case e.Amount < 0 || math.IsNaN(e.Amount):
			return fmt.Errorf("%s.amount must be at least 0", path)
		case e.Kind == IncomePause && (e.Months == nil || *e.Months < 1):
			return fmt.Errorf("%s.months must be at least 1", path)
		case e.Kind == RecurringCost && (e.Years == nil || *e.Years < 1):
			return fmt.Errorf("%s.years must be at least 1", path)
		}
	}
	for i, item := range r.Items {
		path := fmt.Sprintf("items[%d]", i)
		switch {
		case !slices.Contains([]string{Income, Investments, Expenses}, item.Category):
			return fmt.Errorf("%s.category %q is not a category", path, item.Category)
		case item.Amount < 0 || math.IsNaN(item.Amount):
			return fmt.Errorf("%s.amount must be at least 0", path)
		}
		if item.Frequency != nil {
			if _, ok := plan.PerMonth(*item.Frequency); !ok {
				return fmt.Errorf("%s.frequency %q is not a frequency", path, *item.Frequency)
			}
		}
		if item.WithdrawalRate != nil && (*item.WithdrawalRate <= 0 || *item.WithdrawalRate > 1) {
			return fmt.Errorf("%s.withdrawalRate must be above 0 and at most 1", path)
		}
	}
	return nil
}
