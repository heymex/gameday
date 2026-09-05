package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// loadDotEnv reads KEY=VALUE pairs from path into the process environment
// without overwriting variables that are already set. Missing file is fine.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("required environment variable %s is not set", key)
	}
	return v, nil
}

func parseDir(s string) (direction, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "in":
		return dirIn, nil
	case "out":
		return dirOut, nil
	default:
		return 0, fmt.Errorf("direction must be \"in\" or \"out\", got %q", s)
	}
}

// seriesFromEnv builds one Series from PREFIX_HOST, PREFIX_IFINDEX, PREFIX_DIR,
// and PREFIX_COMMUNITY (falling back to SNMP_COMMUNITY).
func seriesFromEnv(name, prefix, defaultDir string) (Series, error) {
	host, err := requireEnv(prefix + "_HOST")
	if err != nil {
		return Series{}, err
	}
	idxStr, err := requireEnv(prefix + "_IFINDEX")
	if err != nil {
		return Series{}, err
	}
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return Series{}, fmt.Errorf("%s_IFINDEX: %w", prefix, err)
	}

	community := os.Getenv(prefix + "_COMMUNITY")
	if community == "" {
		community = os.Getenv("SNMP_COMMUNITY")
	}
	if community == "" {
		return Series{}, fmt.Errorf("%s_COMMUNITY or SNMP_COMMUNITY must be set", prefix)
	}

	dirStr := envOr(prefix+"_DIR", defaultDir)
	dir, err := parseDir(dirStr)
	if err != nil {
		return Series{}, fmt.Errorf("%s_DIR: %w", prefix, err)
	}

	return Series{
		Name:      name,
		Host:      host,
		Community: community,
		IfIndex:   idx,
		Dir:       dir,
	}, nil
}

// loadSeries reads the three measurement points from the environment.
// Topology (hosts / ifIndexes) and credentials never live in source.
func loadSeries() ([]Series, error) {
	baseline, err := seriesFromEnv("baseline", "BASELINE", "out")
	if err != nil {
		return nil, err
	}
	distCore, err := seriesFromEnv("dist_core", "DIST_CORE", "out")
	if err != nil {
		return nil, err
	}
	dmzIsp, err := seriesFromEnv("dmz_isp", "DMZ_ISP", "out")
	if err != nil {
		return nil, err
	}
	return []Series{baseline, distCore, dmzIsp}, nil
}
