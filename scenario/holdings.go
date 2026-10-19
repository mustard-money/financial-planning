package scenario

import (
	"fmt"
	"regexp"
	"strings"
)

// Holding is how one of the scenario's investments is backtested.
type Holding struct {
	ItemID string  `json:"itemId"`
	Name   string  `json:"name"`
	Value  float64 `json:"value"` // today
	// Ticker is the security whose history is used.
	Ticker string `json:"ticker"`
	// Source is "ticker" when the investment named the ticker itself, and
	// "proxy" when an index ETF stands in for it.
	Source string `json:"source"`
	// Tracks says what a proxy represents, or what stands in for a ticker
	// before it existed.
	Tracks string `json:"tracks"`
	// Earlier lists what supplies the months before Ticker's own history
	// begins, nearest first.
	Earlier []string `json:"earlier"`

	series string
}

// Prices says which tickers the prices database has, and which of them are
// single companies' stocks.
type Prices interface {
	Has(ticker string) (bool, error)
	IsStock(ticker string) (bool, error)
}

// proxy is an index-tracking ETF that stands in for a kind of investment.
type proxy struct {
	Ticker string
	Tracks string
	// Earlier are older funds tracking the same thing, used for the months
	// before the ETF existed when the prices database has them. "cash" is
	// Treasury bill yields, which need no ticker.
	Earlier []string
	// Words in an investment's name that point to this proxy.
	Words []string
}

// The fallbacks by account, and what an investment is taken to be when
// neither its account nor its name says otherwise.
var (
	stocks = proxy{Ticker: "SPY", Tracks: "US large-company stocks (S&P 500)", Earlier: []string{"VFINX"}}
	bank   = proxy{Ticker: "BIL", Tracks: "cash (1-3 month Treasury bills)", Earlier: []string{"cash"}}
	cd     = proxy{Ticker: "SHV", Tracks: "short-term deposits (Treasuries under a year)", Earlier: []string{"cash"}}
)

// byName are checked in order, so the more specific come first: "treasury
// bill" before "treasury", "international bond" is a bond fund.
var byName = []proxy{
	{Ticker: bank.Ticker, Tracks: bank.Tracks, Earlier: bank.Earlier,
		Words: []string{"cash", "checking", "money market", "emergency fund", "t-bill", "t-bills", "treasury bill", "treasury bills", "high-yield savings", "high yield savings"}},
	{Ticker: "TIP", Tracks: "inflation-protected Treasuries (TIPS)", Earlier: []string{"VIPSX"},
		Words: []string{"tips", "inflation-protected", "inflation protected"}},
	{Ticker: "MUB", Tracks: "municipal bonds", Earlier: []string{"VWITX"},
		Words: []string{"muni", "munis", "municipal"}},
	{Ticker: "IEF", Tracks: "intermediate-term Treasuries", Earlier: []string{"VFITX"},
		Words: []string{"treasury", "treasuries"}},
	{Ticker: "AGG", Tracks: "US investment-grade bonds", Earlier: []string{"VBMFX"},
		Words: []string{"bond", "bonds", "fixed income", "aggregate"}},
	{Ticker: "EEM", Tracks: "emerging-market stocks", Earlier: []string{"VEIEX"},
		Words: []string{"emerging"}},
	{Ticker: "EFA", Tracks: "developed-market stocks outside the US", Earlier: []string{"VGTSX"},
		Words: []string{"international", "intl", "foreign", "developed", "ex-us", "ex us"}},
	{Ticker: "VNQ", Tracks: "US real estate investment trusts", Earlier: []string{"VGSIX"},
		Words: []string{"real estate", "reit", "reits"}},
	{Ticker: "GLD", Tracks: "gold",
		Words: []string{"gold"}},
	{Ticker: "AOR", Tracks: "a 60/40 mix of stocks and bonds", Earlier: []string{"VBINX"},
		Words: []string{"target date", "target retirement", "balanced", "60/40", "lifecycle", "lifestrategy"}},
	{Ticker: "QQQ", Tracks: "the Nasdaq-100",
		Words: []string{"nasdaq", "qqq"}},
	{Ticker: "IWM", Tracks: "US small-company stocks (Russell 2000)", Earlier: []string{"NAESX"},
		Words: []string{"small cap", "small-cap", "smallcap", "small company", "russell 2000"}},
	{Ticker: "VTI", Tracks: "the whole US stock market", Earlier: []string{"VTSMX", "VFINX"},
		Words: []string{"total market", "total stock", "total us", "total u.s."}},
	{Ticker: stocks.Ticker, Tracks: stocks.Tracks, Earlier: stocks.Earlier,
		Words: []string{"s&p", "sp500", "s & p", "500 index", "large cap", "large-cap"}},
}

// ProxyTickers lists every ticker the proxies can use, the ETFs and the
// older funds behind them, for making sure the prices database has them.
func ProxyTickers() []string {
	var tickers []string
	seen := map[string]bool{"cash": true}
	for _, p := range append([]proxy{stocks, bank, cd}, byName...) {
		for _, t := range append([]string{p.Ticker}, p.Earlier...) {
			if !seen[t] {
				seen[t] = true
				tickers = append(tickers, t)
			}
		}
	}
	return tickers
}

// proxyFor picks the ETF that stands in for an investment, from its account
// and then its name.
func proxyFor(account, name string) proxy {
	switch account {
	case "bank":
		return bank
	case "cd":
		return cd
	}
	lower := strings.ToLower(name)
	for _, p := range byName {
		for _, word := range p.Words {
			if hasPhrase(lower, word) {
				return p
			}
		}
	}
	return stocks
}

// hasPhrase reports whether the phrase appears in the text as whole words, so
// "gold" doesn't match "Goldman".
func hasPhrase(text, phrase string) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], phrase)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(phrase)
		if (start == 0 || !isWordChar(text[start-1])) && (end == len(text) || !isWordChar(text[end])) {
			return true
		}
		from = start + 1
	}
}

func isWordChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

var tickerPattern = regexp.MustCompile(`^[A-Z0-9^.=-]{1,32}$`)

// resolve decides what an investment is backtested as: its own ticker if it
// names one the database has, otherwise the proxy for its account and name.
// A ticker's history is extended back with the proxy's, so a recent listing
// doesn't shorten the backtest. It returns a warning when a named ticker
// can't be used.
func resolve(h Holding, ticker, account string, prices Prices) (Holding, string, error) {
	p := proxyFor(account, h.Name)
	if ok, err := prices.Has(p.Ticker); err != nil {
		return h, "", err
	} else if !ok {
		return h, "", fmt.Errorf("the prices database has no %s, which stands in for %s", p.Ticker, p.Tracks)
	}
	chain := []string{p.Ticker}
	for _, earlier := range p.Earlier {
		ok := earlier == "cash"
		if !ok {
			var err error
			if ok, err = prices.Has(earlier); err != nil {
				return h, "", err
			}
		}
		if ok {
			chain = append(chain, earlier)
		}
	}

	h.Ticker, h.Source, h.Tracks = p.Ticker, "proxy", p.Tracks
	warning := ""
	if ticker = strings.ToUpper(strings.TrimSpace(ticker)); ticker != "" {
		ok := tickerPattern.MatchString(ticker)
		if ok {
			var err error
			if ok, err = prices.Has(ticker); err != nil {
				return h, "", err
			}
		}
		switch {
		case !ok:
			warning = fmt.Sprintf("%s: there is no price history for %s, so it is backtested as %s (%s)", h.Name, ticker, p.Ticker, p.Tracks)
		case ticker != p.Ticker:
			h.Ticker, h.Source = ticker, "ticker"
			chain = append([]string{ticker}, chain...)
		default:
			h.Source = "ticker"
		}
		if stock, err := prices.IsStock(ticker); err != nil {
			return h, "", err
		} else if ok && stock {
			warning = fmt.Sprintf("%s: %s is one company's stock, so every period replays that company's own past, including any exceptional run. Its future is unlikely to look the same.", h.Name, ticker)
		}
	}

	h.Earlier = chain[1:]
	ids := make([]string, len(chain))
	for i, t := range chain {
		ids[i] = "db:" + t
		if t == "cash" {
			ids[i] = "cash"
		}
	}
	h.series = strings.Join(ids, "|")
	return h, warning, nil
}
