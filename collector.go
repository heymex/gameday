// collector.go — SNMP throughput collector with baseline subtraction.
//
// Live-event path (upload toward the ISP):
//
//	broadcast gear → dumb switch → closet access port (baseline, typically out)
//	  → closet → dist ⇄ core ⇄ DMZ/edge ⇄ ISP
//
// Polls HC octet counters on a shared tick, converts to bits/sec, and derives
// "other traffic" at each chokepoint as (link total − broadcast baseline) so
// you can see whether anything besides the broadcast port is eating the
// dist→core or DMZ→ISP pipes. Serves aligned series as JSON for uPlot.
//
// Hosts, ifIndexes, and communities come from the environment (see
// .env.example). Copy .env.example → .env and fill in site values.
//
// Setup:
//   cp .env.example .env   # edit hosts / community / ifIndexes
//   go mod tidy
//   go build -o gameday .
//
// See newPoller for where SNMPv3 slots in.

package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gosnmp/gosnmp"
)

const (
	pollInterval = 2 * time.Second // fast enough to catch contention during a live event
	ringSize     = 900             // 900 samples * 2s = 30 min of history per series
	snmpTimeout  = 2 * time.Second
	snmpRetries  = 1
)

// IF-MIB high-capacity (64-bit) octet counters. Append ".<ifIndex>".
// Always use these, never the 32-bit ifInOctets/ifOutOctets: a 32-bit octet
// counter wraps in ~34s on a gigabit link and produces garbage deltas.
const (
	oidIfHCInOctets  = "1.3.6.1.2.1.31.1.1.1.6"
	oidIfHCOutOctets = "1.3.6.1.2.1.31.1.1.1.10"
)

type direction int

const (
	dirIn  direction = iota // ifHCInOctets  — traffic INTO the interface
	dirOut                  // ifHCOutOctets — traffic OUT of the interface
)

// A Series is one counter, on one interface, in one direction.
// Prefer the logical Port-channel ifIndex (HC counters already aggregate
// members) over summing member links by hand.
type Series struct {
	Name      string
	Host      string
	Community string
	IfIndex   int
	Dir       direction
}

func (s Series) oid() string {
	base := oidIfHCInOctets
	if s.Dir == dirOut {
		base = oidIfHCOutOctets
	}
	return base + "." + strconv.Itoa(s.IfIndex)
}

// other returns total − baseline, clamped at zero. A near-flat "other" line
// means the broadcast owns the pipe; a rising one means something else is
// competing for the uplink.
func other(total, baseline float64) float64 {
	if v := total - baseline; v > 0 {
		return v
	}
	return 0
}

// --- storage --------------------------------------------------------------

// Sample is one throughput reading for one series at one tick. Tick ties every
// series to the same instant so the baseline subtraction is time-aligned.
type Sample struct {
	Tick int64
	T    time.Time
	Bps  float64
	OK   bool // false on priming read, SNMP error, or counter reset
}

type Store struct {
	mu   sync.RWMutex
	data map[string][]Sample // series name -> ring buffer
}

func newStore() *Store { return &Store{data: map[string][]Sample{}} }

func (st *Store) add(name string, s Sample) {
	st.mu.Lock()
	defer st.mu.Unlock()
	buf := append(st.data[name], s)
	if len(buf) > ringSize {
		buf = buf[len(buf)-ringSize:]
	}
	st.data[name] = buf
}

func (st *Store) snapshot() map[string][]Sample {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make(map[string][]Sample, len(st.data))
	for k, v := range st.data {
		cp := make([]Sample, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// --- polling --------------------------------------------------------------

type poller struct {
	s        Series
	conn     *gosnmp.GoSNMP
	lastCtr  uint64
	lastTime time.Time
	lastBps  float64
	hasRate  bool
	primed   bool
}

func newPoller(s Series) (*poller, error) {
	g := &gosnmp.GoSNMP{
		Target:    s.Host,
		Port:      161,
		Community: s.Community,
		Version:   gosnmp.Version2c, // for v3: set Version3, MsgFlags, SecurityModel,
		Timeout:   snmpTimeout,      // and SecurityParameters (USM user + auth/priv)
		Retries:   snmpRetries,
	}
	if err := g.Connect(); err != nil {
		return nil, err
	}
	return &poller{s: s, conn: g}, nil
}

// poll reads the counter and converts to bits/sec using the previous *change*.
// Many switches refresh IF-MIB counters every several seconds; polling faster
// than that used to emit fake 0 bps samples and then spike the whole delta into
// a single 2s window. When the counter is unchanged we hold the last good rate
// and keep lastTime so the next advance is amortized over the full gap.
func (p *poller) poll(now time.Time) (float64, bool) {
	res, err := p.conn.Get([]string{p.s.oid()})
	if err != nil || len(res.Variables) == 0 {
		p.primed = false
		p.hasRate = false
		return 0, false
	}
	cur := gosnmp.ToBigInt(res.Variables[0].Value).Uint64()

	if !p.primed {
		p.lastCtr, p.lastTime, p.primed = cur, now, true
		return 0, false
	}
	if cur < p.lastCtr {
		// Device reboot / counter reset — re-prime.
		p.lastCtr, p.lastTime, p.hasRate = cur, now, false
		return 0, false
	}
	if cur == p.lastCtr {
		if p.hasRate {
			return p.lastBps, true
		}
		return 0, false
	}

	dt := now.Sub(p.lastTime).Seconds()
	if dt <= 0 {
		return 0, false
	}
	bps := float64(cur-p.lastCtr) * 8 / dt
	p.lastCtr, p.lastTime, p.lastBps, p.hasRate = cur, now, bps, true
	return bps, true
}

// --- collector ------------------------------------------------------------

type Collector struct {
	pollers []*poller
	store   *Store
	tick    int64
}

// run fires every pollInterval and reads all series concurrently, stamping
// them with one shared timestamp and tick number. That shared stamp is what
// makes total − baseline valid: without it, drifting per-series schedules
// would inject phantom jitter into the derived "other" line.
func (c *Collector) run() {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		tick := atomic.AddInt64(&c.tick, 1)

		var wg sync.WaitGroup
		for _, p := range c.pollers {
			wg.Add(1)
			go func(p *poller) {
				defer wg.Done()
				bps, ok := p.poll(now)
				c.store.add(p.s.Name, Sample{Tick: tick, T: now, Bps: bps, OK: ok})
			}(p)
		}
		wg.Wait()
	}
}

// handleData emits uPlot-shaped columns:
//
//	[ x(unix), baseline, dist_core, dist_core_other, dmz_isp, dmz_isp_other ]
//
// Ticks without a valid baseline are skipped, since the derived series are
// meaningless without B(t).
func (c *Collector) handleData(w http.ResponseWriter, r *http.Request) {
	snap := c.store.snapshot()

	// Index OK samples by tick so series line up regardless of map order.
	type row struct {
		t    float64
		vals map[string]float64
	}
	rows := map[int64]*row{}
	for name, samples := range snap {
		for _, s := range samples {
			if !s.OK {
				continue
			}
			rw := rows[s.Tick]
			if rw == nil {
				rw = &row{t: float64(s.T.UnixMilli()) / 1000, vals: map[string]float64{}}
				rows[s.Tick] = rw
			}
			rw.vals[name] = s.Bps
		}
	}

	ticks := make([]int64, 0, len(rows))
	for tk := range rows {
		ticks = append(ticks, tk)
	}
	sort.Slice(ticks, func(i, j int) bool { return ticks[i] < ticks[j] })

	cols := []string{"baseline", "dist_core", "dist_core_other", "dmz_isp", "dmz_isp_other"}
	out := make([][]float64, len(cols)+1)
	for _, tk := range ticks {
		rw := rows[tk]
		base, ok := rw.vals["baseline"]
		if !ok {
			continue
		}
		distCore := rw.vals["dist_core"]
		dmzIsp := rw.vals["dmz_isp"]

		out[0] = append(out[0], rw.t)
		out[1] = append(out[1], base)
		out[2] = append(out[2], distCore)
		out[3] = append(out[3], other(distCore, base))
		out[4] = append(out[4], dmzIsp)
		out[5] = append(out[5], other(dmzIsp, base))
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		log.Printf("encode: %v", err)
	}
}

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Fatalf("load .env: %v", err)
	}

	series, err := loadSeries()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	store := newStore()
	c := &Collector{store: store}
	for _, s := range series {
		p, err := newPoller(s)
		if err != nil {
			log.Fatalf("connect %s (%s): %v", s.Name, s.Host, err)
		}
		c.pollers = append(c.pollers, p)
		log.Printf("series %s → %s ifIndex %d", s.Name, s.Host, s.IfIndex)
	}
	go c.run()

	addr := envOr("LISTEN_ADDR", ":8080")
	http.HandleFunc("/api/data", c.handleData)
	http.Handle("/", http.FileServer(http.Dir("./web")))
	log.Printf("listening on %s  (GET /api/data)", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
