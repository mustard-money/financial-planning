# financial-planning

Batch analysis jobs for the Mustard app: a [backtester](#backtest) for hand-written plans, a [scenario backtest](#scenario-backtest) for users' scenarios, and a [downloader](#prices) for the daily closing prices both can use. (The tax tables live in `../customer-data/taxtables`.)

## Backtest

Takes a household's plan (income streams, expense streams, a portfolio of securities and investment real estate) and runs it through market history, month by month: what would have happened to this plan if the coming years unfolded like the ones starting in 1987, or 2000, or every start month on record?

It is Go (1.26+) using only the standard library. (The [prices](#prices) command adds one dependency, a pure-Go SQLite driver.)

```sh
make build                                                    # builds bin/backtest
bin/backtest coverage examples/household.json                 # which months the plan's market data covers
bin/backtest run examples/household.json                      # one run over all the history available
bin/backtest run examples/household.json -start 2000-01 -years 10
bin/backtest rolling examples/household.json -years 20        # every 20-year period on record
```

`go run . run examples/household.json` works without building. Flags may come before or after the plan; `bin/backtest run -h` lists them.

`run` and `rolling` print a text report; `-json` prints the full result as JSON instead, and `-csv FILE` also writes it as a table (a row per month for `run`, a row per period for `rolling`). The first run downloads market data; later runs reuse it for a week (`-offline` never downloads, `-refresh` always does).

### The plan

A JSON file; [`examples/household.json`](examples/household.json) uses every part. Amounts are dollars as of `asOf`, and rates are fractions (`0.07` is 7%). Unknown fields are rejected.

| Field            | Meaning |
| ---------------- | ------- |
| `name`           | Optional title for reports |
| `asOf`           | The month the amounts and values are quoted in, `YYYY-MM`; default this month. Dates in the plan are measured from it |
| `income`         | List of income streams |
| `expenses`       | List of expense streams |
| `securities`     | List of holdings |
| `realEstate`     | List of investment properties |
| `policy`         | How securities are managed (optional) |
| `inflationIndex` | Series for inflation; default `fred:CPIAUCSL` (US CPI) |

A plan needs at least one security or property.

**Income and expense streams** use the field names of the customer-data API's items, so items can be pasted in. Their `id`, `category`, `incomeType`, `benefits`, `createdAt` and `updatedAt` are accepted and ignored (workplace-plan contributions in `benefits` are not modelled).

| Field | Meaning |
| ----- | ------- |
| `name` | Required |
| `amount` | Required; dollars per occurrence, >= 0 |
| `frequency` | `weekly`, `biweekly`, `semimonthly`, `monthly` (default), `quarterly`, `semiannually` or `yearly`. Spread evenly over months, as the website does |
| `startYear`, `startMonth`, `endYear`, `endMonth` | Optional; when it starts and ends. A start year alone means January, an end year alone December |
| `indexation` | `inflation` (default): the amount rises with the inflation of the period being replayed. `none`: it stays fixed in dollars, like a loan payment |
| `growthRate` | Optional yearly growth, on top of inflation when indexed; -0.5 to 0.5 |
| `taxRate` | Income only: the share lost to tax, 0 to 1 (default 0). A flat effective rate; the website's bracket-by-bracket tax tables are not reproduced here |

**Securities**

| Field | Meaning |
| ----- | ------- |
| `name` | Required, unique |
| `symbol` | A ticker on Yahoo Finance (`VTI`). Either this or `series` |
| `proxy` | With `symbol`: one or more older tickers that stand in for the months before the fund existed (`"VFINX"` for `VTI`), in order of preference |
| `series` | Any [series id](#market-data) instead of a ticker: `cash`, `csv:funds/private.csv`, ... |
| `value` or `shares` | What is held: dollars, or a number of shares priced at the latest month-end close (`shares` needs `symbol`) |

**Real estate**

| Field | Meaning |
| ----- | ------- |
| `name`, `value` | Required; what the property is worth today |
| `priceIndex` | Series its value follows; default `fred:CSUSHPINSA` (S&P Case-Shiller US national index, from 1987). FRED has metro indexes (`fred:SFXRSA` San Francisco, `fred:NYXRSA` New York) and state ones (`fred:CASTHPI`, quarterly, from 1975) |
| `monthlyRent` | Rent at full occupancy; default 0 |
| `rentIndex` | Series the rent follows; default the plan's inflation. `fred:CUSR0000SEHA` is the CPI for rent |
| `vacancyRate` | Share of rent lost to vacancy |
| `managementFeeRate` | Share of collected rent paid to a manager |
| `annualCostRate` | Property tax, insurance and upkeep, as a yearly share of the property's value |
| `monthlyCosts` | Other costs in dollars (HOA dues, ...), rising with inflation |
| `mortgage` | `{ "balance", "rate", "payment" }` or `{ "balance", "rate", "remainingMonths" }`: a fixed-rate loan, given its monthly principal-and-interest payment or the months left |
| `sale` | `{ "year", "month", "costRate" }`: sell at the end of that month (default December), paying `costRate` of the price (default 0.06) and the rest of the loan. The proceeds join that month's cash flow |

**Policy**

| Field | Meaning |
| ----- | ------- |
| `targetWeights` | Holding name to weight (any scale; they are normalised). Where surpluses are invested, and what rebalancing restores. Default: the starting mix |
| `rebalance` | `never` (default), `monthly`, `quarterly` or `yearly`: how often holdings are reset to the target weights |
| `withdrawalOrder` | Holding names to draw down first, in order, when spending exceeds income. Whatever is still needed comes from the other holdings in proportion to their size |

### How a run works

The plan's `asOf` month is laid over a historical start month. The plan's own numbers (today's balances, rents and budgets) are kept as they are; history supplies what the plan can't know: each security's return, house prices, rents and inflation, in the order they happened. Each month:

1. Income (after its tax rate) and expenses are paid, at the price level the month opens with.
2. Each property collects rent, pays its costs and mortgage, and is repriced by its index. A property due to be sold is sold.
3. Securities earn the month's return.
4. The net cash flow moves. A surplus is invested by the target weights; a deficit is withdrawn from securities.
5. In a rebalancing month, securities are reset to the target weights.

Securities never go below zero. A deficit they can't cover accumulates as an **unfunded shortfall**, without interest, which later surpluses repay first; real estate is not sold to cover it. A run "covers every month's spending" when that never happens.

The report gives net worth at the start and end (also in start dollars, i.e. after inflation), the securities' time-weighted return, volatility and deepest drawdown, the cash flows, and a year-by-year table. `rolling` repeats the run from every start month that leaves room for a whole period and reports how often spending was covered and the spread of ending net worth.

### Market data

Series are named by id; every one is reduced to a return per month.

| Id | Source |
| -- | ------ |
| `yahoo:VTI` | Yahoo Finance monthly closes, adjusted for dividends and splits (so returns are total returns). What `symbol` uses |
| `fred:CPIAUCSL` | A [FRED](https://fred.stlouisfed.org) index: prices, house prices, rents. Quarterly indexes and skipped releases are filled in at a constant rate between the neighbouring values |
| `fred-yield:TB3MS` | A FRED interest rate in percent a year, earned a month at a time |
| `cash` | Short for `fred-yield:TB3MS` (3-month Treasury bills, from 1934): savings accounts, money market funds |
| `db:VTI` | A ticker's month-end closes in the [prices](#prices) database, given with `-db data/prices.db`. Adjusted closes, so returns are total returns |
| `csv:path.csv` | A file next to the plan. First line `month,level` (prices or an index) or `month,return` (fractions, consecutive months); months as `YYYY-MM` |
| `a\|b` | Series `a`, with `b` standing in for the months before `a` begins. What `proxy` builds |

Downloads are cached in `mustard/market-data` under the user cache directory (`~/Library/Caches` on macOS, `~/.cache` on Linux); override with `-cache-dir`. Yahoo's endpoint is unofficial and can change or be withdrawn; for anything beyond personal analysis, supply licensed data as CSV. A backtest can only cover months that every series has, so the youngest series sets the limit: `coverage` shows which one, and `proxy` is how to push it back.

### What it does not do

- **Taxes** beyond each income's flat `taxRate`: no tax on dividends, capital gains, rent or withdrawals, and no difference between taxable and retirement accounts.
- **Transaction costs** and fund expenses beyond what is already in a fund's price.
- **Your property**, exactly. A house price index is an average and smoother than any one building; a single property is riskier than its index suggests.
- **Lumpy timing.** A yearly bonus or tax bill is spread over twelve months.
- **Prediction.** History is one sample, the rolling periods overlap heavily (two starts a month apart share nearly all their months), and holdings chosen today were chosen knowing how they did. A plan that survived every period on record has passed a stress test, not earned a guarantee.

### From Go

```go
// A scenario
req, err := scenario.Parse(requestJSON)
prices, err := market.OpenPricesDB("data/prices.db")
result, err := scenario.Backtest(req, &market.Loader{CacheDir: cacheDir, Prices: prices}, time.Now())

// A plan
p, err := plan.Load("examples/household.json")
loader := &market.Loader{CacheDir: cacheDir, CSVDir: "examples"}
data, err := loader.LoadAll(p.SeriesIDs())

result, err := backtest.Simulate(p, data, calendar.NewMonth(2000, 1), 120)
summary := backtest.Summarize(result)

rolling, err := backtest.Roll(p, data, 240, 1)
fmt.Println(summary.EndNetWorthReal, rolling.SuccessRate())
```

## Scenario backtest

Runs one of a user's Mustard scenarios (the income, expenses and investments they chose, plus life events) through market history, where the app's own projection assumes a fixed yearly return. It reads prices from the [prices](#prices) database.

```sh
make build
bin/prices -symbols prices/proxies.txt        # once: adds the older index funds (see "History")
bin/backtest scenario examples/scenario.json  # a text report
bin/backtest scenario request.json -json      # the full result, for the app
cat request.json | bin/backtest scenario -    # the request on standard input
```

| Flag | Meaning |
| ---- | ------- |
| `-db` | The prices database; default `data/prices.db` |
| `-json` | Print the result as JSON |
| `-cache-dir`, `-offline`, `-refresh` | As for `backtest run`: inflation and Treasury bill yields come from FRED and are cached for a week |

The scenario is run from every starting month that leaves room for its whole length: a 20-year scenario with 40 years of history runs 241 times. The result says how often the investments lasted and how widely the outcomes spread.

### Which prices each investment uses

1. **Its own ticker**, if the investment names one (`"ticker": "VTI"`) and the database has it. Before the ticker's own history begins, the ETF from step 2 stands in, so a recent listing doesn't shorten the backtest.
2. **Otherwise an index-tracking ETF** for what the investment appears to be, from its account and then its name:

| The investment | ETF | Before the ETF existed |
| -------------- | --- | ---------------------- |
| Account `bank`; or named cash, checking, money market, emergency fund, T-bills | `BIL` | Treasury bill yields |
| Account `cd` | `SHV` | Treasury bill yields |
| Named TIPS, inflation-protected | `TIP` | `VIPSX` |
| Named muni, municipal | `MUB` | `VWITX` |
| Named Treasury | `IEF` | `VFITX` |
| Named bond, fixed income, aggregate | `AGG` | `VBMFX` |
| Named emerging | `EEM` | `VEIEX` |
| Named international, foreign, developed, ex-US | `EFA` | `VGTSX` |
| Named real estate, REIT | `VNQ` | `VGSIX` |
| Named gold | `GLD` | |
| Named target date, balanced, 60/40 | `AOR` | `VBINX` |
| Named Nasdaq | `QQQ` | |
| Named small cap, Russell 2000 | `IWM` | `NAESX` |
| Named total market, total stock | `VTI` | `VTSMX`, then `VFINX` |
| Named S&P, 500 index, large cap; and everything else | `SPY` | `VFINX` |

The first row that matches wins, so an "international bond fund" is a bond fund. The table is `byName` in [`scenario/holdings.go`](scenario/holdings.go). The result lists what every investment was backtested as, so the app can show it.

A ticker the database has no prices for falls back to step 2 with a warning. A ticker that is one company's stock also gets a warning: every period replays that company's own past, and a stock that happened to do very well makes every outcome look good.

**The app has no ticker field yet.** Items in customer-data don't record what an investment is held in, so until the API and the app add a `ticker` to investment items, every investment goes through step 2.

### History

How far back a period can start is set by the holding with the shortest history, and the ETFs are young: `SPY` starts in 1993, `AGG` in 2003, `BIL` in 2007. The third column above names older index mutual funds that tracked the same thing. They aren't exchange-listed, so the full download doesn't include them; `bin/prices -symbols prices/proxies.txt` adds them, and they are then used automatically. With them, stocks go back to 1980 and bonds to 1987. For the example scenario that is 237 starting months, against 36 without them.

A scenario longer than the history available can't be run. The result then has `periods: 0` and a `reason`, and still lists the holdings.

### The request

JSON with the scenario and the user's items in the shapes the customer-data API returns them, so they can be passed on unchanged (fields a backtest doesn't read are ignored).

```json
{
  "scenario": { "id": "house", "name": "Buy a house", "itemIds": ["..."], "events": [], "parameters": { "years": 20, "inflation": 0.03, "incomeGrowth": 0.03, "incomeAdjustment": 0, "expenseAdjustment": 0, "returnRate": 0.07 } },
  "items": [],
  "profile": { "expectedReturn": 0.07 },
  "taxRate": 0.27,
  "today": "2026-10-04"
}
```

| Field | Meaning |
| ----- | ------- |
| `scenario` | The scenario. Its `itemIds` pick which items count |
| `items` | The user's items; all of them is fine. Investment items may carry `ticker` |
| `profile` | The account profile. `expectedReturn` (default 0.07) is used only to bring dated balances up to today |
| `taxRate` | Share of earned income, and of withdrawals from traditional retirement accounts, that goes to tax; default 0. There are no tax tables here: the app, which has them, should pass the effective rate from its own projection |
| `today` | The date the amounts are current on; default now |

### How it follows the app's projection

It mirrors `src/scenarios/engine.ts` in the website wherever history doesn't replace an assumption:

- **Income** is the chosen income items with the scenario's adjustment, in the months each is in effect, less months lost to income pauses. It rises with the inflation of the period being replayed, plus the scenario's income growth over its own inflation assumption (3% growth with 3% inflation means keeping pace with prices).
- **Expenses** are the chosen expense items with their adjustment, rising with inflation, plus life-event costs.
- **Investments** start at today's value, estimated as the app does. Retirement accounts with a withdrawal plan pay out on it. The others take each month's surplus in proportion to their size, or cover the deficit. A W-2 income's workplace account balances start invested too.
- **Running out** means the same thing: total investments below zero.

What differs:

- **Returns and inflation** come from history. The scenario's `returnRate` is not used.
- **Tax** is the flat `taxRate`, where the app applies brackets.
- **Time** runs in months from today. The app runs in calendar years from January, so its "year 1" is the current calendar year and this one's is the next twelve months.
- **Life events** are spread over the twelve months of their year.
- **Workplace balances** with a date are grown to today without the contributions made since.

### The result

`-json` prints one object. Money is in today's dollars unless noted.

| Field | Meaning |
| ----- | ------- |
| `holdings` | For each investment: `itemId`, `name`, `value` today, the `ticker` used, `source` (`ticker` or `proxy`), what it `tracks`, and the `earlier` funds behind it |
| `warnings` | Things to show the user: tickers that couldn't be used, single stocks |
| `history` | `from`, `to`, `years` of price history, and `limitedBy`, the holding whose history is shortest |
| `periods`, `firstStart`, `lastStart` | How many periods were run and the first and last starting months |
| `successRate` | Share of periods in which the investments never ran out |
| `startInvestments`, `endInvestments` | The starting total, and the spread at the end: `worst`, `p10`, `p25`, `p50`, `p75`, `p90`, `best` |
| `yearly` | The same spread at the end of each year, for a fan chart |
| `worst`, `median`, `best` | Three whole periods: `start`, `end`, `endInvestments`, `shortfallYear`, the investments' yearly `return` and `maxDrawdown`, `inflation`, and a row per year with `income` (after tax), `withdrawals`, `taxes`, `expenses` and `investments` in the dollars of the time, and `investmentsReal` in today's |

The limits in [What it does not do](#what-it-does-not-do) apply here too, and one matters more: the starting months overlap, and with ETFs they are few. A hundred periods that all begin between 1993 and 2001 are closer to one experiment than to a hundred.

## Prices

Downloads the daily closing price of every security listed on a US exchange, as far back as Yahoo Finance has it (1962 for IBM, 1927 for the S&P 500 index), into a SQLite database.

```sh
make build
bin/prices -limit 25             # a trial run: the first 25 symbols
bin/prices                       # everything: about 13,000 symbols
bin/prices -symbols tickers.txt  # only the tickers in a file
```

The full run makes one request per symbol at up to 4 a second, so it takes about an hour. Expect a database of 2 to 3 GB (about 30 million rows; an estimate from the first 80 symbols).

**Resuming.** Each ticker is stored in its own transaction as it completes, together with a record of how its download went. A run that is interrupted (Ctrl-C), killed, or stopped by throttling leaves the database intact, and running the same command again continues: tickers settled in the last 20 hours are skipped and the rest are downloaded. Failed tickers are always tried again. Run daily, it refreshes everything.

| Flag | Meaning |
| ---- | ------- |
| `-db` | The SQLite file; default `data/prices.db` (ignored by git). Created if missing |
| `-symbols` | A file of tickers, one per line, as Yahoo writes them (`BRK-B`, `^GSPC`); `#` starts a comment. Without it, every listed symbol is downloaded |
| `-limit` | Stop after this many tickers |
| `-max-age` | Skip tickers downloaded more recently than this; default `20h`, and `0` downloads everything again |
| `-workers` | Downloads to run at once; default 4 |
| `-rate` | Most requests per second; default 4 |

### In Docker

`Dockerfile` builds both commands into one small image; the prices downloader is its default command, and the database lives in `/data` (mount a volume to keep it):

```sh
docker build -t mustard/financial-planning:dev .
docker run --rm -v prices:/data mustard/financial-planning:dev -limit 25
docker run --rm --entrypoint /backtest mustard/financial-planning:dev -h
```

It stops cleanly on SIGINT (`STOPSIGNAL`), so `docker stop` leaves a resumable database. `../dev.sh start batch` (dev mode, local binaries) and `../dev.sh test start batch` (this image) run it as part of the local stack.

### Tables

`prices`: a row per ticker and trading day. The primary key is `(ticker, date)`, and `(year, month, day)` is indexed.

| Column | |
| ------ | - |
| `ticker` | As Yahoo writes it: `BRK-B` |
| `date` | `YYYY-MM-DD`, on the exchange's calendar |
| `year`, `month`, `day` | The same date as numbers: `2008`, `10`, `3` |
| `close` | The closing price, adjusted for later splits (which is how Yahoo reports it: a share that closed at 100 before a 2-for-1 split shows as 50) |
| `adj_close` | Also adjusted for dividends, so the ratio of two days' `adj_close` is the total return between them |

`downloads`: a row per ticker tried, which is what resuming reads.

| Column | |
| ------ | - |
| `ticker` | |
| `status` | `saved`, `no data` (Yahoo has no prices for it) or `failed` |
| `detail` | Why it failed |
| `rows`, `first_date`, `last_date` | What `prices` holds for the ticker |
| `downloaded_at` | When it was last tried, UTC |

`symbols`: the current directory of listed symbols: `ticker`, `symbol` (the exchange's notation, `BRK.B`), `name`, `exchange`, and `etf` (1 or 0). It is replaced on each run without `-symbols`.

```sh
sqlite3 data/prices.db
```

```sql
-- One ticker's history
SELECT date, close, adj_close FROM prices WHERE ticker = 'IBM' ORDER BY date;

-- Every ticker's last close of each month in 2008
SELECT ticker, month, close FROM prices p
WHERE year = 2008 AND day = (SELECT max(day) FROM prices WHERE ticker = p.ticker AND year = 2008 AND month = p.month);

-- Total return by calendar year
SELECT year, max(adj_close) FILTER (WHERE date = last) / max(adj_close) FILTER (WHERE date = first) - 1 AS total_return
FROM (SELECT *, min(date) OVER (PARTITION BY year) AS first, max(date) OVER (PARTITION BY year) AS last
      FROM prices WHERE ticker = 'VTI')
GROUP BY year;

-- ETFs with at least 20 years of history
SELECT s.ticker, s.name, d.first_date FROM symbols s JOIN downloads d USING (ticker)
WHERE s.etf = 1 AND d.first_date <= date('now', '-20 years');

-- What wasn't saved, and why
SELECT ticker, status, detail FROM downloads WHERE status <> 'saved';
```

Filter by `ticker`, or by `year` (with `month` and `day` if wanted), to use an index; filtering on `date` alone, without a ticker, reads the whole table.

The list of symbols comes from [Nasdaq Trader's symbol directory](https://www.nasdaqtrader.com/trader.aspx?id=symboldirdefs), which covers Nasdaq, NYSE, NYSE American, NYSE Arca and Cboe: stocks, ETFs, preferred shares, units, warrants and rights, without the exchanges' test symbols. Exchange symbols are converted to Yahoo's notation (`BRK.B` becomes `BRK-B`, `BAC$B` becomes `BAC-PB`).

Things to know:

- **Each download replaces the ticker's rows.** A new dividend or split changes the adjusted values of every earlier day, so history is rewritten, not appended to. If a refresh fails, the rows already stored are kept.
- **Only what is listed today, at first.** Companies that were delisted, acquired or went bankrupt before your first run are not in the directory, so a study over this data sees only the survivors. Symbols that drop out of the directory later keep their rows.
- **Only US exchanges.** Mutual funds, indexes and foreign listings aren't in the directory; add them with `-symbols`.
- **Yahoo's endpoint is unofficial.** It can throttle or change. Requests that are throttled are retried with growing pauses, and the run stops itself if 20 symbols in a row can't be reached. Yahoo's terms don't allow redistributing the data; for a product, use a licensed provider.
- **One writer at a time.** Reading the database while a download runs is fine; `prices.db-wal` and `prices.db-shm` beside it are SQLite's working files.

## Layout

```
main.go      the backtest command line: flags, then load, run and report
scenario/    a user's scenario as a backtest: the request, which ticker each investment uses, the result
calendar/    Month, the unit of time
market/      Series (returns per month), and loading them from the prices database, FRED, Yahoo Finance and CSV
plan/        the plan model and its JSON validation
backtest/    the month-by-month simulation, summary statistics, and running every period of one length
report/      text, CSV and JSON output
prices/      the prices command: symbol directory, Yahoo daily history, the download run and its SQLite store
pricestest/  small prices databases for tests
examples/    a sample plan
```

Packages are top-level directories, as in customer-data. Tests use made-up data and a fake downloader, so they need no network.

## Checks before finishing

```sh
gofmt -l .    # must print nothing
go vet ./...
make test
```
