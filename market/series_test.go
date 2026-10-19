package market

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/mustard-money/financial-planning/calendar"
)

func month(year, m int) calendar.Month { return calendar.NewMonth(year, m) }

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func nearAll(got, want []float64) bool {
	return slices.EqualFunc(got, want, near)
}

// wantErr fails the test unless err mentions the message.
func wantErr(t *testing.T, err error, message string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), message) {
		t.Errorf("error = %v, want one mentioning %q", err, message)
	}
}

func flat(id string, start calendar.Month, months int) *Series {
	return &Series{ID: id, Start: start, Returns: make([]float64, months)}
}

func TestFromLevelsStartsTheMonthAfterTheFirstLevel(t *testing.T) {
	s, err := FromLevels("x", map[calendar.Month]float64{month(2000, 1): 100, month(2000, 2): 110, month(2000, 3): 99})
	if err != nil {
		t.Fatal(err)
	}
	if s.Start != month(2000, 2) || s.End() != month(2000, 3) {
		t.Errorf("covers %v to %v, want 2000-02 to 2000-03", s.Start, s.End())
	}
	if !nearAll(s.Returns, []float64{0.10, -0.10}) || s.LastLevel != 99 {
		t.Errorf("returns %v, last level %v", s.Returns, s.LastLevel)
	}
}

func TestFromLevelsSpreadsAGapEvenly(t *testing.T) {
	// A quarterly index, or a month with no release: 100 -> 133.1 over three months is 10% a month.
	s, err := FromLevels("x", map[calendar.Month]float64{month(2000, 1): 100, month(2000, 4): 133.1})
	if err != nil {
		t.Fatal(err)
	}
	if !nearAll(s.Returns, []float64{0.10, 0.10, 0.10}) {
		t.Errorf("returns %v, want three of 0.10", s.Returns)
	}
}

func TestFromLevelsRejectsTooLittleOrNonPositiveData(t *testing.T) {
	_, err := FromLevels("x", map[calendar.Month]float64{month(2000, 1): 100})
	wantErr(t, err, "at least two months")
	_, err = FromLevels("x", map[calendar.Month]float64{month(2000, 1): 100, month(2000, 2): 0})
	wantErr(t, err, "not above zero")
}

func TestFromYieldsEarnsATwelfthOfTheYearlyRateCompounded(t *testing.T) {
	s, err := FromYields("x", map[calendar.Month]float64{month(2000, 1): 12, month(2000, 3): 0})
	if err != nil {
		t.Fatal(err)
	}
	if s.Start != month(2000, 1) || len(s.Returns) != 3 {
		t.Fatalf("starts %v with %d months, want 2000-01 with 3", s.Start, len(s.Returns))
	}
	if !near(math.Pow(1+s.Returns[0], 12), 1.12) {
		t.Errorf("a 12%% yield compounds to %v over a year", math.Pow(1+s.Returns[0], 12))
	}
	// The missing month reuses the one before.
	if s.Returns[1] != s.Returns[0] || s.Returns[2] != 0 {
		t.Errorf("returns %v", s.Returns)
	}
	_, err = FromYields("x", nil)
	wantErr(t, err, "no data")
}

func TestWithEarlierKeepsItsOwnReturnsAndAddsTheMonthsBefore(t *testing.T) {
	fund := &Series{ID: "fund", Start: month(2000, 3), Returns: []float64{0.03, 0.04}, LastLevel: 50}
	proxy := &Series{ID: "proxy", Start: month(2000, 1), Returns: []float64{0.01, 0.02, 0.99, 0.99}}
	joined, err := fund.WithEarlier(proxy)
	if err != nil {
		t.Fatal(err)
	}
	if joined.ID != "fund" || joined.Start != month(2000, 1) || joined.LastLevel != 50 {
		t.Errorf("got %+v", joined)
	}
	if !slices.Equal(joined.Returns, []float64{0.01, 0.02, 0.03, 0.04}) {
		t.Errorf("returns %v", joined.Returns)
	}
	if !slices.Equal(proxy.Returns, []float64{0.01, 0.02, 0.99, 0.99}) {
		t.Errorf("the proxy was modified: %v", proxy.Returns)
	}
}

func TestWithEarlierIgnoresASeriesThatIsNotEarlier(t *testing.T) {
	fund := &Series{ID: "fund", Start: month(2000, 1), Returns: []float64{0.03}}
	same, err := fund.WithEarlier(&Series{ID: "proxy", Start: month(2000, 1), Returns: []float64{0.5}})
	if err != nil || same != fund {
		t.Errorf("got %+v, %v; want the fund unchanged", same, err)
	}
}

func TestWithEarlierRejectsAGap(t *testing.T) {
	fund := &Series{ID: "fund", Start: month(2000, 6), Returns: []float64{0.03}}
	_, err := fund.WithEarlier(&Series{ID: "proxy", Start: month(2000, 1), Returns: []float64{0.01, 0.01}})
	wantErr(t, err, "proxy ends at 2000-02, before fund starts at 2000-06")
}

func TestWindowIsTheMonthsEverySeriesCovers(t *testing.T) {
	data := Data{"a": flat("a", month(2000, 1), 24), "b": flat("b", month(2000, 7), 24)}
	first, last, err := data.Window([]string{"a", "b"})
	if err != nil || first != month(2000, 7) || last != month(2001, 12) {
		t.Errorf("window = %v to %v, %v; want 2000-07 to 2001-12", first, last, err)
	}
	first, last, err = data.Window([]string{"a"})
	if err != nil || first != month(2000, 1) || last != month(2001, 12) {
		t.Errorf("window = %v to %v, %v; want 2000-01 to 2001-12", first, last, err)
	}
}

func TestWindowErrors(t *testing.T) {
	data := Data{"a": flat("a", month(2000, 1), 12), "b": flat("b", month(2005, 1), 12)}
	_, _, err := data.Window([]string{"a", "b"})
	wantErr(t, err, "share no months: b starts at 2005-01 but a ends at 2000-12")
	_, _, err = data.Window([]string{"a", "c"})
	wantErr(t, err, "no market data loaded for c")
	_, _, err = data.Window(nil)
	wantErr(t, err, "no series")
}
