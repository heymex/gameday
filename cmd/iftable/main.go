// Command iftable dumps IF-MIB interface tables for every host listed in .env
// so you can verify BASELINE_IFINDEX / DIST_CORE_IFINDEX / DMZ_ISP_IFINDEX.
//
//	go run ./cmd/iftable
//	go run ./cmd/iftable -env /path/to/.env
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

const (
	oidIfDescr      = "1.3.6.1.2.1.2.2.1.2"
	oidIfOperStatus = "1.3.6.1.2.1.2.2.1.8"
	oidIfName       = "1.3.6.1.2.1.31.1.1.1.1"
	oidIfHighSpeed  = "1.3.6.1.2.1.31.1.1.1.15"
	oidIfAlias      = "1.3.6.1.2.1.31.1.1.1.18"
)

var seriesPrefixes = []struct {
	prefix string
	label  string
}{
	{"BASELINE", "baseline"},
	{"DIST_CORE", "dist_core"},
	{"DMZ_ISP", "dmz_isp"},
}

type device struct {
	Host      string
	Community string
	// ifIndex -> series labels that reference it in .env
	Wanted map[int][]string
}

type iface struct {
	Index  int
	Name   string
	Descr  string
	Alias  string
	Oper   string
	Speed  string
}

func main() {
	envPath := flag.String("env", ".env", "path to env file")
	flag.Parse()

	if err := loadDotEnv(*envPath); err != nil {
		fmt.Fprintf(os.Stderr, "load %s: %v\n", *envPath, err)
		os.Exit(1)
	}

	devices, err := loadDevices()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	hosts := make([]string, 0, len(devices))
	for h := range devices {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)

	failed := false
	for _, host := range hosts {
		d := devices[host]
		if err := dumpDevice(d); err != nil {
			fmt.Fprintf(os.Stderr, "\n%s: %v\n", host, err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func loadDevices() (map[string]*device, error) {
	out := map[string]*device{}
	for _, sp := range seriesPrefixes {
		host := os.Getenv(sp.prefix + "_HOST")
		if host == "" {
			return nil, fmt.Errorf("%s_HOST is not set", sp.prefix)
		}
		community := os.Getenv(sp.prefix + "_COMMUNITY")
		if community == "" {
			community = os.Getenv("SNMP_COMMUNITY")
		}
		if community == "" {
			return nil, fmt.Errorf("%s_COMMUNITY or SNMP_COMMUNITY must be set", sp.prefix)
		}
		idxStr := os.Getenv(sp.prefix + "_IFINDEX")
		if idxStr == "" {
			return nil, fmt.Errorf("%s_IFINDEX is not set", sp.prefix)
		}
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			return nil, fmt.Errorf("%s_IFINDEX: %w", sp.prefix, err)
		}

		d := out[host]
		if d == nil {
			d = &device{Host: host, Community: community, Wanted: map[int][]string{}}
			out[host] = d
		}
		d.Wanted[idx] = append(d.Wanted[idx], sp.label)
	}
	return out, nil
}

func dumpDevice(d *device) error {
	fmt.Printf("\n=== %s ===\n", d.Host)
	wanted := make([]string, 0, len(d.Wanted))
	for idx, labels := range d.Wanted {
		wanted = append(wanted, fmt.Sprintf("%s=%d", strings.Join(labels, "+"), idx))
	}
	sort.Strings(wanted)
	fmt.Printf("configured: %s\n\n", strings.Join(wanted, ", "))

	snmp := &gosnmp.GoSNMP{
		Target:    d.Host,
		Port:      161,
		Community: d.Community,
		Version:   gosnmp.Version2c,
		Timeout:   3 * time.Second,
		Retries:   1,
	}
	if err := snmp.Connect(); err != nil {
		return err
	}
	defer snmp.Conn.Close()

	byIndex := map[int]*iface{}

	ensure := func(idx int) *iface {
		if ifc := byIndex[idx]; ifc != nil {
			return ifc
		}
		ifc := &iface{Index: idx}
		byIndex[idx] = ifc
		return ifc
	}

	walkers := []struct {
		oid string
		set func(*iface, gosnmp.SnmpPDU)
	}{
		{oidIfName, func(i *iface, p gosnmp.SnmpPDU) { i.Name = pduString(p) }},
		{oidIfDescr, func(i *iface, p gosnmp.SnmpPDU) { i.Descr = pduString(p) }},
		{oidIfAlias, func(i *iface, p gosnmp.SnmpPDU) { i.Alias = pduString(p) }},
		{oidIfOperStatus, func(i *iface, p gosnmp.SnmpPDU) { i.Oper = operStatus(p) }},
		{oidIfHighSpeed, func(i *iface, p gosnmp.SnmpPDU) { i.Speed = highSpeed(p) }},
	}

	for _, w := range walkers {
		err := snmp.BulkWalk(w.oid, func(pdu gosnmp.SnmpPDU) error {
			idx, ok := oidIndex(pdu.Name, w.oid)
			if !ok {
				return nil
			}
			w.set(ensure(idx), pdu)
			return nil
		})
		if err != nil {
			return fmt.Errorf("walk %s: %w", w.oid, err)
		}
	}

	indexes := make([]int, 0, len(byIndex))
	for idx := range byIndex {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)

	fmt.Printf("%-8s %-6s %-8s %-22s %-28s %s\n", "ifIndex", "oper", "Mb/s", "ifName", "ifAlias", "ifDescr")
	fmt.Printf("%-8s %-6s %-8s %-22s %-28s %s\n", "-------", "----", "----", "------", "-------", "-------")

	foundWanted := map[int]bool{}
	for _, idx := range indexes {
		i := byIndex[idx]
		mark := "  "
		if labels, ok := d.Wanted[idx]; ok {
			mark = "->"
			foundWanted[idx] = true
			_ = labels
		}
		fmt.Printf("%s%-6d %-6s %-8s %-22s %-28s %s\n",
			mark,
			i.Index,
			trunc(i.Oper, 6),
			trunc(i.Speed, 8),
			trunc(i.Name, 22),
			trunc(i.Alias, 28),
			i.Descr,
		)
		if labels, ok := d.Wanted[idx]; ok {
			fmt.Printf("         ^ configured for %s\n", strings.Join(labels, ", "))
		}
	}

	missing := make([]int, 0)
	for idx := range d.Wanted {
		if !foundWanted[idx] {
			missing = append(missing, idx)
		}
	}
	sort.Ints(missing)
	for _, idx := range missing {
		fmt.Printf("\n!! ifIndex %d (%s) not present in IF-MIB on this device\n",
			idx, strings.Join(d.Wanted[idx], ", "))
	}

	fmt.Printf("\n%d interfaces\n", len(indexes))
	return nil
}

func oidIndex(fullOID, base string) (int, bool) {
	fullOID = strings.TrimPrefix(fullOID, ".")
	base = strings.TrimPrefix(base, ".")
	if !strings.HasPrefix(fullOID, base+".") {
		return 0, false
	}
	n, err := strconv.Atoi(fullOID[len(base)+1:])
	if err != nil {
		return 0, false
	}
	return n, true
}

func pduString(p gosnmp.SnmpPDU) string {
	switch v := p.Value.(type) {
	case []byte:
		return string(v)
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func operStatus(p gosnmp.SnmpPDU) string {
	n := gosnmp.ToBigInt(p.Value).Int64()
	switch n {
	case 1:
		return "up"
	case 2:
		return "down"
	case 3:
		return "testing"
	case 4:
		return "unknown"
	case 5:
		return "dormant"
	case 6:
		return "notPres"
	case 7:
		return "lowerLayer"
	default:
		return strconv.FormatInt(n, 10)
	}
}

func highSpeed(p gosnmp.SnmpPDU) string {
	return strconv.FormatUint(gosnmp.ToBigInt(p.Value).Uint64(), 10)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file not found (copy .env.example to .env first)")
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if _, set := os.LookupEnv(key); !set {
			_ = os.Setenv(key, val)
		}
	}
	return sc.Err()
}
