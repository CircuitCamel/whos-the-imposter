// Package config loads startup configuration from a .env file and the
// environment, ahead of flag parsing.
package config

import (
	"bufio"
	"log"
	"os"
	"strconv"
	"strings"
)

// LoadDotenv reads simple KEY=VALUE lines from path and applies them with
// os.Setenv, so the rest of the program can just read the environment. A
// real environment variable (set by the shell, systemd, a container
// platform...) always wins over the file - that's a more deliberate
// override than a checked-in default. A missing file is fine; this is a
// convenience, not a requirement, so the game still starts from flags and
// defaults without one.
func LoadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
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
		if n := len(val); n >= 2 {
			if (val[0] == '"' && val[n-1] == '"') || (val[0] == '\'' && val[n-1] == '\'') {
				val = val[1 : n-1]
			}
		}
		if key == "" {
			continue
		}
		if _, already := os.LookupEnv(key); already {
			continue
		}
		os.Setenv(key, val)
	}
}

// EnvOr returns the named environment variable, or def if it's unset or empty.
func EnvOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// EnvIntOr returns the named environment variable parsed as an int, or def
// if it's unset or empty. A value that's set but not a number is a config
// mistake worth stopping the server for, same as an unreadable topics file.
func EnvIntOr(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("%s=%q is not a whole number", key, v)
	}
	return n
}
