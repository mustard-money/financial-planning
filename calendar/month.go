// Package calendar provides Month, the backtester's unit of time.
package calendar

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Month is a calendar month, stored as months since year 0 so that the
// difference between two is a count of months.
type Month int

var monthPattern = regexp.MustCompile(`^(\d{4})-(\d{2})(-\d{2})?$`)

// NewMonth returns the given month (1 = January) of the given year.
func NewMonth(year, month int) Month {
	return Month(year*12 + month - 1)
}

// ParseMonth parses "YYYY-MM"; a trailing "-DD" is accepted and ignored.
func ParseMonth(s string) (Month, error) {
	match := monthPattern.FindStringSubmatch(s)
	if match == nil {
		return 0, fmt.Errorf("expected a month like 2001-06, got %q", s)
	}
	year, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	if month < 1 || month > 12 {
		return 0, fmt.Errorf("expected a month like 2001-06, got %q", s)
	}
	return NewMonth(year, month), nil
}

// MonthOf returns the month t falls in, in t's location.
func MonthOf(t time.Time) Month {
	return NewMonth(t.Year(), int(t.Month()))
}

func (m Month) Year() int { return int(m) / 12 }

// Month returns the month of the year, 1 to 12.
func (m Month) Month() int { return int(m)%12 + 1 }

// Add returns the month n months later (earlier when n is negative).
func (m Month) Add(n int) Month { return m + Month(n) }

// Sub returns how many months m is after o.
func (m Month) Sub(o Month) int { return int(m - o) }

func (m Month) String() string {
	return fmt.Sprintf("%04d-%02d", m.Year(), m.Month())
}
