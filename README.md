# GameLift UDP Ping Beacons

Command line tool for players to measure Amazon GameLift UDP ping beacon quality from their own network.

It probes every configured GameLift endpoint for a fixed duration and reports:

- latency: min, average, p50, and p95 round-trip time
- jitter: average absolute change between consecutive successful RTT samples
- packet loss: sent probes that did not receive a UDP reply before the timeout

## Usage

```sh
go run . -duration 10s
```

Useful options:

```sh
go run . -duration 30s -interval 250ms -timeout 1s
go run . -duration 30s -format json
go run . -duration 30s -family ipv4
go run . -duration 30s -family ipv6
```

Flags:

- `-duration`: how long to ping each endpoint, default `10s`
- `-interval`: delay between probes sent to each endpoint, default `500ms`
- `-timeout`: per-probe response timeout, default `1s`
- `-family`: `auto`, `ipv4`, or `ipv6`, default `auto`
- `-format`: `table` or `json`, default `table`
- `-samples`: include raw RTT samples in JSON output

The table output is optimized for pasting into support tickets. JSON output is better for automated ingestion by a game team.

## Build

```sh
go build -o gamelift-ping-report .
./gamelift-ping-report -duration 30s
```
