// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"sort"
	"time"
)

// Stats is a point-in-time snapshot of gateway state. Safe to serialize.
type Stats struct {
	StartedAt time.Time       `json:"started_at"`
	NowAt     time.Time       `json:"now"`
	IdleTTLMS int64           `json:"idle_ttl_ms"`
	Functions []FunctionStats `json:"functions"`
	Layers    []LayerSummary  `json:"layers"`
}

// LayerSummary is the dashboard's view of one layer — keyed by its host
// directory because that is the page-cache deduplication boundary in the
// kernel (one inode = one set of cached pages, regardless of how many
// containers bind-mount it).
type LayerSummary struct {
	Name       string   `json:"name"`
	HostPath   string   `json:"host_path"`
	MountPath  string   `json:"mount_path"`
	RefCount   int      `json:"ref_count"`            // how many registered functions use it
	WarmRefs   int      `json:"warm_refs"`            // how many of those are currently running
	References []string `json:"references"`           // sorted function names
}

// FunctionStats is everything the dashboard wants to know about one
// registered function. Aggregates across the per-function instance pool.
type FunctionStats struct {
	Name           string             `json:"name"`
	Endpoint       string             `json:"endpoint"`
	Binary         string             `json:"binary"`
	Layers         []FunctionStatsLyr `json:"layers,omitempty"`
	Running        bool               `json:"running"`
	PoolSize       int                `json:"pool_size"`        // currently warm instances
	MaxConcurrency int                `json:"max_concurrency"`  // configured limit
	Mode           string             `json:"mode,omitempty"`
	ColdMS         int64              `json:"cold_start_ms,omitempty"` // freshest instance's cold start
	StartedAt      *time.Time         `json:"started_at,omitempty"`    // earliest instance
	LastUsed       *time.Time         `json:"last_used_at,omitempty"`  // most recent
	IdleMS         int64              `json:"idle_ms,omitempty"`
	Invokes        uint64             `json:"invokes"`
	Errors         uint64             `json:"errors"`
	AvgMS          float64            `json:"avg_duration_ms,omitempty"`
}

type FunctionStatsLyr struct {
	Name      string `json:"name"`
	HostPath  string `json:"host_path"`
	MountPath string `json:"mount_path"`
}

// startedAt records when the gateway came up (set in NewWithOptions).
// Snapshot used by the dashboard.
func (g *Gateway) Stats() Stats {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.opts.Now()
	out := Stats{
		StartedAt: g.startedAt,
		NowAt:     now,
		IdleTTLMS: g.opts.IdleTTL.Milliseconds(),
		Functions: make([]FunctionStats, 0, len(g.functions)),
	}

	// Layer aggregation: bucket by host path. host_path is the inode
	// boundary in the kernel, so it's the right key for "shared in RAM".
	type layerKey struct{ host, mount string }
	type layerAgg struct {
		name       string
		mount      string
		refs       []string
		warmRefs   int
	}
	layers := map[layerKey]*layerAgg{}

	for name, def := range g.functions {
		fs := FunctionStats{
			Name:           name,
			Endpoint:       "/fn/" + name,
			Binary:         def.Binary,
			MaxConcurrency: def.MaxConcurrency,
		}

		// Snapshot pool state for this function.
		var poolInsts []*managedInstance
		var lifetimeInv, lifetimeErr uint64
		var lifetimeDur time.Duration
		if p, ok := g.pools[name]; ok {
			p.mu.Lock()
			poolInsts = append(poolInsts, p.instances...)
			lifetimeInv = p.lifetimeInvokes
			lifetimeErr = p.lifetimeErrors
			lifetimeDur = p.lifetimeTotalDur
			p.mu.Unlock()
		}

		for _, l := range def.Layers {
			fs.Layers = append(fs.Layers, FunctionStatsLyr{
				Name: l.Name, HostPath: l.HostPath, MountPath: l.MountPath,
			})
			k := layerKey{host: l.HostPath, mount: l.MountPath}
			a, ok := layers[k]
			if !ok {
				a = &layerAgg{name: l.Name, mount: l.MountPath}
				layers[k] = a
			}
			a.refs = append(a.refs, name)
			if len(poolInsts) > 0 {
				a.warmRefs++
			}
		}

		if len(poolInsts) > 0 {
			fs.Running = true
			fs.PoolSize = len(poolInsts)
			fs.Mode = poolInsts[0].inst.Mode

			var totalInv, totalErr uint64
			var totalDur time.Duration
			earliest := poolInsts[0].created
			latest := poolInsts[0].lastUsed()
			minCold := poolInsts[0].inst.ColdStartDuration
			for _, mi := range poolInsts {
				totalInv += mi.invokes.Load()
				totalErr += mi.errors.Load()
				totalDur += time.Duration(mi.totalNS.Load())
				if mi.created.Before(earliest) {
					earliest = mi.created
				}
				if lu := mi.lastUsed(); lu.After(latest) {
					latest = lu
				}
				if mi.inst.ColdStartDuration < minCold {
					minCold = mi.inst.ColdStartDuration
				}
			}
			fs.ColdMS = minCold.Milliseconds()
			fs.StartedAt = &earliest
			fs.LastUsed = &latest
			fs.IdleMS = now.Sub(latest).Milliseconds()
			fs.Invokes = totalInv + lifetimeInv
			fs.Errors = totalErr + lifetimeErr
			if fs.Invokes > 0 {
				fs.AvgMS = float64((totalDur + lifetimeDur).Milliseconds()) / float64(fs.Invokes)
			}
		} else if lifetimeInv > 0 {
			// No warm instances but we've served before — keep totals.
			fs.Invokes = lifetimeInv
			fs.Errors = lifetimeErr
			if lifetimeInv > 0 {
				fs.AvgMS = float64(lifetimeDur.Milliseconds()) / float64(lifetimeInv)
			}
		}
		out.Functions = append(out.Functions, fs)
	}
	// Stable order so the UI doesn't jitter.
	sortFunctions(out.Functions)

	out.Layers = make([]LayerSummary, 0, len(layers))
	for k, a := range layers {
		refs := append([]string(nil), a.refs...)
		sort.Strings(refs)
		out.Layers = append(out.Layers, LayerSummary{
			Name: a.name, HostPath: k.host, MountPath: k.mount,
			RefCount: len(refs), WarmRefs: a.warmRefs, References: refs,
		})
	}
	sort.Slice(out.Layers, func(i, j int) bool {
		// Most-shared layers first — that's the dashboard's headline metric.
		if out.Layers[i].RefCount != out.Layers[j].RefCount {
			return out.Layers[i].RefCount > out.Layers[j].RefCount
		}
		return out.Layers[i].Name < out.Layers[j].Name
	})
	return out
}
