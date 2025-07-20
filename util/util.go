package util

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	envDurationCache sync.Map // map[string]time.Duration
	envIntCache      sync.Map // map[string]int
	envStringCache   sync.Map // map[string]string
)

func GetEnvDuration(key string, def time.Duration) time.Duration {
	if v, ok := envDurationCache.Load(key); ok {
		return v.(time.Duration)
	}
	val := def
	if s := os.Getenv(key); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			val = d
		}
	}
	envDurationCache.Store(key, val)
	return val
}

func GetEnvInt(key string, def int) int {
	if v, ok := envIntCache.Load(key); ok {
		return v.(int)
	}
	val := def
	if s := os.Getenv(key); s != "" {
		if i, err := strconv.Atoi(s); err == nil {
			val = i
		}
	}
	envIntCache.Store(key, val)
	return val
}

func GetEnvString(key, def string) string {
	if v, ok := envStringCache.Load(key); ok {
		return v.(string)
	}
	val := def
	if s := os.Getenv(key); s != "" {
		val = s
	}
	envStringCache.Store(key, val)
	return val
}

func GetEnvStringSlice(key, def string) []string {
	if v := os.Getenv(key); v != "" {
		if !strings.Contains(v, ",") {
			return []string{strings.TrimSpace(v)}
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for i := range parts {
			s := strings.TrimSpace(parts[i])
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	if def == "" {
		return nil
	}
	return []string{def}
}

func GetEnvBool(key string, def bool) bool {
	if v, ok := envStringCache.Load(key); ok {
		return v.(string) == "true"
	}
	val := def
	if s := os.Getenv(key); s != "" {
		switch s {
		case "true", "1":
			val = true
		case "false", "0":
			val = false
		}
	}
	envStringCache.Store(key, strconv.FormatBool(val))
	return val
}
