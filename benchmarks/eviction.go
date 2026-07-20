package benchmarks

import (
	"fmt"
	"time"
)

type EvictionPolicy string

const (
	EvictRandom EvictionPolicy = "random"
	EvictOldest EvictionPolicy = "oldest"
	EvictNewest EvictionPolicy = "newest"
)

type EvictionBenchConfig struct {
	Svc        string         `json:"svc"`
	Evict      bool           `json:"evict"`
	EvictDelay time.Duration  `json:"evict_delay"`
	Policy     EvictionPolicy `json:"policy"`
}

func NewEvictionBenchConfig(svc string, evict bool, evictDelay time.Duration, policy EvictionPolicy) *EvictionBenchConfig {
	return &EvictionBenchConfig{
		Svc:        svc,
		Evict:      evict,
		EvictDelay: evictDelay,
		Policy:     policy,
	}
}

func (cfg *EvictionBenchConfig) String() string {
	return fmt.Sprintf("&{ svc:%v evict:%v delay:%v policy:%v }", cfg.Svc, cfg.Evict, cfg.EvictDelay, cfg.Policy)
}
