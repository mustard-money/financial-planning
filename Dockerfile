# The batch jobs as an image: the prices downloader (the default command) and
# the backtest tool. Pure Go (the SQLite driver too), so a static binary on
# distroless, which includes the CA certificates the downloads need.
#   docker build -t mustard/financial-planning:dev .
#   docker run --rm -v prices:/data mustard/financial-planning:dev            # refresh prices
#   docker run --rm -v prices:/data mustard/financial-planning:dev -limit 25  # a trial run
#   docker run --rm --entrypoint /backtest mustard/financial-planning:dev -h

# ---- build ----
FROM golang:1.26 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/prices ./prices && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/backtest . && \
    mkdir /out/data

# ---- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/prices /prices
COPY --from=build /out/backtest /backtest
# The prices database; mount a volume here to keep it between runs. Owned by
# the nonroot user, so a new volume (which copies this ownership) is writable.
COPY --from=build --chown=65532:65532 /out/data /data
WORKDIR /data
USER nonroot:nonroot
# prices stops cleanly (resumable) on SIGINT; run with --stop-signal SIGINT.
STOPSIGNAL SIGINT
ENTRYPOINT ["/prices", "-db", "/data/prices.db"]
