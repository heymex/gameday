# gameday

SNMP throughput monitor for live-event broadcasts. Polls a few uplink counters, subtracts the broadcast port as a baseline, and exposes time-aligned series so you can see whether anything else is competing for bandwidth on the path to the ISP.

## Why

Broadcast gear sits on a dumb switch that uplinks into the closet. From there traffic rides Port-channels through distribution → core → DMZ/edge → ISP (e.g. an ESPN+ stream on guest Internet).

When the pipe feels congested, you need a quick answer: is it mostly the broadcast, or is other traffic eating dist→core or DMZ→ISP?

**gameday** tracks:

| Series | Meaning |
| --- | --- |
| `baseline` | Closet access port facing the dumb switch (broadcast in) |
| `dist_core` | Dist → core Port-channel total (upstream) |
| `dmz_isp` | DMZ → ISP Port-channel total (upload) |
| `*_other` | `total − baseline` (clamped at 0) |

A flat `*_other` line means the broadcast owns that hop. A rising one means something else is sharing the uplink.

## Setup

```bash
cp .env.example .env   # set community, hosts, ifIndexes
go mod tidy
go build -o gameday .
./gameday
```

Open `http://localhost:8080` for the live charts (static UI in `./web`, polls `/api/data` every 2s).

## Configuration

Copy `.env.example` to `.env` (gitignored). Existing environment variables win over `.env`.

| Variable | Required | Description |
| --- | --- | --- |
| `SNMP_COMMUNITY` | yes\* | Shared SNMPv2c community |
| `BASELINE_HOST` / `BASELINE_IFINDEX` | yes | Closet access port (stream ingress) |
| `DIST_CORE_HOST` / `DIST_CORE_IFINDEX` | yes | Dist → core Port-channel |
| `DMZ_ISP_HOST` / `DMZ_ISP_IFINDEX` | yes | DMZ → ISP Port-channel |
| `*_DIR` | no | `in` or `out` (defaults: baseline `in`, others `out`) |
| `*_COMMUNITY` | no | Per-series community override |
| `LISTEN_ADDR` | no | HTTP listen address (default `:8080`) |

\*Or set `BASELINE_COMMUNITY` / `DIST_CORE_COMMUNITY` / `DMZ_ISP_COMMUNITY` individually.

Poll the **logical Port-channel** ifIndex when possible — HC counters already aggregate members.

## API

`GET /api/data` returns uPlot-shaped columns:

```text
[ x(unix), baseline, dist_core, dist_core_other, dmz_isp, dmz_isp_other ]
```

Samples are on a shared 2s tick (~30 minutes of ring history). Ticks without a valid baseline are omitted so derived “other” series stay meaningful.

## Notes

- Uses IF-MIB 64-bit `ifHCInOctets` / `ifHCOutOctets` only (32-bit counters wrap too fast on gigabit links).
- SNMPv2c today; `newPoller` in `collector.go` notes where v3 USM plugs in.
- UI in `./web` uses uPlot (CDN) with two charts: dist→core and DMZ→ISP, each showing baseline, link total, and other.
