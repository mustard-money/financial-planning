package calendar

import (
	"testing"
	"time"
)

func TestParseMonth(t *testing.T) {
	for input, want := range map[string]Month{
		"2001-06":    NewMonth(2001, 6),
		"2001-06-01": NewMonth(2001, 6),
		"0987-12":    NewMonth(987, 12),
	} {
		got, err := ParseMonth(input)
		if err != nil || got != want {
			t.Errorf("ParseMonth(%q) = %v, %v; want %v", input, got, err, want)
		}
	}
	for _, input := range []string{"2001", "2001-13", "2001-00", "June 2001", "2001-6", " 2001-06"} {
		if _, err := ParseMonth(input); err == nil {
			t.Errorf("ParseMonth(%q) succeeded, want an error", input)
		}
	}
}

func TestMonthParts(t *testing.T) {
	m := NewMonth(2001, 11)
	if m.Year() != 2001 || m.Month() != 11 {
		t.Errorf("got year %d month %d, want 2001 and 11", m.Year(), m.Month())
	}
	if got := NewMonth(987, 3).String(); got != "0987-03" {
		t.Errorf("String() = %q, want 0987-03", got)
	}
	if got := MonthOf(time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)); got != NewMonth(2026, 10) {
		t.Errorf("MonthOf = %v, want 2026-10", got)
	}
}

func TestMonthArithmeticCrossesYears(t *testing.T) {
	if got := NewMonth(2001, 11).Add(3); got != NewMonth(2002, 2) {
		t.Errorf("2001-11 + 3 = %v, want 2002-02", got)
	}
	if got := NewMonth(2001, 1).Add(-1); got != NewMonth(2000, 12) {
		t.Errorf("2001-01 - 1 = %v, want 2000-12", got)
	}
	if got := NewMonth(2002, 2).Sub(NewMonth(2001, 11)); got != 3 {
		t.Errorf("2002-02 - 2001-11 = %d, want 3", got)
	}
	if !(NewMonth(2001, 12) < NewMonth(2002, 1)) {
		t.Error("2001-12 should sort before 2002-01")
	}
}
