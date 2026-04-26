// Package gateway implements the HTTP frontend: it routes /fn/<name> to a
// registered function, spawns it on demand, and translates HTTP <-> wire
// frames. Each function has a pool of warm instances (up to
// MaxConcurrency) so concurrent requests don't serialize on a single
// process. An idle reaper closes instances that have not been invoked
// within IdleTTL, so memory stays scale-to-zero per function.
package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fabianringel/cfunc/internal/spawn"
	"github.com/fabianringel/cfunc/internal/wire"
)

// DefaultDialTimeout is how long we wait for a freshly spawned user
// process to dial the socket before giving up.
var DefaultDialTimeout = 5 * time.Second

// DefaultIdleTTL is the default idle window before a function instance
// is reaped.
var DefaultIdleTTL = 30 * time.Second

// DefaultReapInterval is how often the reaper scans for idle instances.
var DefaultReapInterval = 5 * time.Second

// DefaultMaxConcurrency is the default per-function instance pool size.
// Plenty for typical FaaS load; users can pin per FunctionDef.
const DefaultMaxConcurrency = 4

// FunctionDef is what callers register with the gateway.
type FunctionDef struct {
	Name           string
	Binary         string
	Env            []string
	Layers         []LayerMount
	MaxConcurrency int // 0 -> DefaultMaxConcurrency
}

// LayerMount pairs a resolved host directory with the path it should
// appear at inside the container.
type LayerMount struct {
	Name      string
	HostPath  string
	MountPath string
}

// Spawner starts a function instance for a given FunctionDef.
type Spawner func(def FunctionDef) (*spawn.Instance, error)

// DefaultSpawner spawns the binary as a host subprocess.
func DefaultSpawner(def FunctionDef) (*spawn.Instance, error) {
	return spawn.Start(def.Binary, def.Env, DefaultDialTimeout)
}

type Options struct {
	IdleTTL      time.Duration
	ReapInterval time.Duration
	Now          func() time.Time
	Spawn        Spawner
	Logger       *slog.Logger
}

type Gateway struct {
	opts      Options
	startedAt time.Time

	mu        sync.Mutex
	functions map[string]FunctionDef
	pools     map[string]*pool

	stopReaper chan struct{}
	reaperDone chan struct{}
}

// pool is the per-function set of warm instances. Bookkeeping mu
// protects the slice; busy mutexes on individual instances control
// per-instance acquisition. lifetime* counters survive instance reaps
// so the dashboard's totals don't reset when warm instances expire.
type pool struct {
	mu        sync.Mutex
	instances []*managedInstance
	spawning  int // outstanding spawns counted against maxSize
	maxSize   int

	// Aggregated counters from instances that have already been reaped.
	lifetimeInvokes  uint64
	lifetimeErrors   uint64
	lifetimeTotalDur time.Duration
}

// managedInstance wraps spawn.Instance with bookkeeping. busy is held
// for the entire duration of an Invoke; the reaper TryLock-skips it.
type managedInstance struct {
	inst     *spawn.Instance
	busy     sync.Mutex
	created  time.Time
	lastUsed time.Time

	invokes  uint64
	errors   uint64
	totalDur time.Duration
}

func New() *Gateway { return NewWithOptions(Options{}) }

func NewWithOptions(opts Options) *Gateway {
	if opts.IdleTTL == 0 {
		opts.IdleTTL = DefaultIdleTTL
	}
	if opts.ReapInterval == 0 {
		opts.ReapInterval = DefaultReapInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Spawn == nil {
		opts.Spawn = DefaultSpawner
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	g := &Gateway{
		opts:       opts,
		startedAt:  opts.Now(),
		functions:  map[string]FunctionDef{},
		pools:      map[string]*pool{},
		stopReaper: make(chan struct{}),
		reaperDone: make(chan struct{}),
	}
	go g.reapLoop()
	return g
}

// Register stores a FunctionDef for routing. Convenience overload.
func (g *Gateway) Register(name, binary string) {
	g.RegisterDef(FunctionDef{Name: name, Binary: binary})
}

// RegisterDef registers a function. If one with the same name exists,
// it's replaced and any pooled warm instances are shut down.
func (g *Gateway) RegisterDef(def FunctionDef) {
	if def.Name == "" {
		panic("gateway: FunctionDef.Name required")
	}
	if def.MaxConcurrency <= 0 {
		def.MaxConcurrency = DefaultMaxConcurrency
	}
	g.mu.Lock()
	oldPool := g.pools[def.Name]
	delete(g.pools, def.Name)
	g.functions[def.Name] = def
	g.mu.Unlock()
	if oldPool != nil {
		oldPool.closeAll()
	}
}

// Unregister removes a function and its pooled instances.
func (g *Gateway) Unregister(name string) bool {
	g.mu.Lock()
	_, hadDef := g.functions[name]
	old := g.pools[name]
	delete(g.functions, name)
	delete(g.pools, name)
	g.mu.Unlock()
	if old != nil {
		old.closeAll()
	}
	return hadDef
}

// Close terminates everything.
func (g *Gateway) Close() {
	close(g.stopReaper)
	<-g.reaperDone

	g.mu.Lock()
	pools := g.pools
	g.pools = map[string]*pool{}
	g.mu.Unlock()
	for _, p := range pools {
		p.closeAll()
	}
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/fn/")
	if name == "" || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}

	mi, release, err := g.acquire(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer release()

	reqID := newID()
	tStart := g.opts.Now()
	body, _ := io.ReadAll(r.Body)
	headers := map[string]string{}
	for k, v := range r.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	event, _ := json.Marshal(map[string]any{
		"method":  r.Method,
		"path":    r.URL.Path,
		"headers": headers,
		"body":    json.RawMessage(quoteIfNotJSON(body)),
	})
	ctx, _ := json.Marshal(map[string]any{"deadline_ms": 30000, "trace_id": reqID})

	reply, err := mi.inst.Invoke(&wire.Frame{
		Type: wire.TypeInvoke, ID: reqID, Event: event, Ctx: ctx,
	})
	if err != nil {
		mi.errors++
		g.opts.Logger.Error("invoke failed",
			"fn", name, "request_id", reqID, "err", err.Error())
		http.Error(w, "function invoke failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	now := g.opts.Now()
	mi.lastUsed = now
	mi.invokes++
	dur := now.Sub(tStart)
	mi.totalDur += dur
	g.opts.Logger.Info("invoke",
		"fn", name,
		"request_id", reqID,
		"duration_ms", dur.Milliseconds(),
		"mode", mi.inst.Mode,
	)

	if reply.Type == wire.TypeError {
		http.Error(w, reply.Error.Type+": "+reply.Error.Message, http.StatusInternalServerError)
		return
	}
	var resp struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	}
	if err := json.Unmarshal(reply.Result, &resp); err != nil {
		http.Error(w, "bad response: "+err.Error(), http.StatusBadGateway)
		return
	}
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	if resp.Status == 0 {
		resp.Status = 200
	}
	w.WriteHeader(resp.Status)
	if len(resp.Body) > 0 {
		var s string
		if err := json.Unmarshal(resp.Body, &s); err == nil {
			_, _ = w.Write([]byte(s))
		} else {
			_, _ = w.Write(resp.Body)
		}
	}
}

// acquire returns a locked managedInstance + a release func. Strategy:
//   1. Try-lock any existing free instance in the pool.
//   2. If pool not full, spawn a new instance, lock it, return.
//   3. Otherwise block on the *first* instance's busy mutex (FIFO-ish).
//
// Spawning happens outside the gateway mutex so the dial-back
// handshake doesn't block other requests.
func (g *Gateway) acquire(name string) (*managedInstance, func(), error) {
	g.mu.Lock()
	def, ok := g.functions[name]
	if !ok {
		g.mu.Unlock()
		return nil, nil, errFunctionNotFound(name)
	}
	p, ok := g.pools[name]
	if !ok {
		p = &pool{maxSize: def.MaxConcurrency}
		g.pools[name] = p
	}
	g.mu.Unlock()

	for {
		// Try to grab a free existing instance.
		p.mu.Lock()
		for _, mi := range p.instances {
			if mi.busy.TryLock() {
				p.mu.Unlock()
				return mi, func() { mi.busy.Unlock() }, nil
			}
		}
		// Spawn capacity? Count in-flight spawns so we don't over-shoot
		// when many requests race in while the first spawn is still
		// dialing back.
		if len(p.instances)+p.spawning < p.maxSize {
			p.spawning++
			p.mu.Unlock()
			inst, err := g.opts.Spawn(def)
			if err != nil {
				p.mu.Lock()
				p.spawning--
				p.mu.Unlock()
				return nil, nil, err
			}
			now := g.opts.Now()
			mi := &managedInstance{inst: inst, created: now, lastUsed: now}
			mi.busy.Lock()

			p.mu.Lock()
			p.spawning--
			p.instances = append(p.instances, mi)
			poolSize := len(p.instances)
			p.mu.Unlock()

			g.opts.Logger.Info("spawned",
				"fn", name,
				"mode", inst.Mode,
				"cold_start_ms", inst.ColdStartDuration.Milliseconds(),
				"pool_size", poolSize,
			)
			return mi, func() { mi.busy.Unlock() }, nil
		}
		// Pool full and all busy. Wait on the oldest instance.
		first := p.instances[0]
		p.mu.Unlock()
		first.busy.Lock()
		// Re-check: could've been reaped while waiting.
		p.mu.Lock()
		stillThere := false
		for _, mi := range p.instances {
			if mi == first {
				stillThere = true
				break
			}
		}
		p.mu.Unlock()
		if stillThere {
			return first, func() { first.busy.Unlock() }, nil
		}
		first.busy.Unlock()
		// Loop and retry.
	}
}

func (p *pool) closeAll() {
	p.mu.Lock()
	insts := p.instances
	p.instances = nil
	p.mu.Unlock()
	for _, mi := range insts {
		_ = mi.inst.Close()
	}
}

// reapLoop runs until Close.
func (g *Gateway) reapLoop() {
	defer close(g.reaperDone)
	t := time.NewTicker(g.opts.ReapInterval)
	defer t.Stop()
	for {
		select {
		case <-g.stopReaper:
			return
		case <-t.C:
			g.reapOnce()
		}
	}
}

// ReapNow triggers a reap pass synchronously. Useful for tests.
func (g *Gateway) ReapNow() { g.reapOnce() }

func (g *Gateway) reapOnce() {
	now := g.opts.Now()
	var victims []*managedInstance

	g.mu.Lock()
	for _, p := range g.pools {
		p.mu.Lock()
		kept := p.instances[:0]
		for _, mi := range p.instances {
			if !mi.busy.TryLock() {
				kept = append(kept, mi)
				continue
			}
			if now.Sub(mi.lastUsed) >= g.opts.IdleTTL {
				// Roll counters into pool-level totals so dashboard
				// metrics don't reset when warm instances expire.
				p.lifetimeInvokes += mi.invokes
				p.lifetimeErrors += mi.errors
				p.lifetimeTotalDur += mi.totalDur
				victims = append(victims, mi)
				// keep busy locked so nobody picks it up
			} else {
				mi.busy.Unlock()
				kept = append(kept, mi)
			}
		}
		p.instances = kept
		p.mu.Unlock()
	}
	g.mu.Unlock()

	for _, mi := range victims {
		if err := mi.inst.Close(); err != nil {
			slog.Warn("idle close failed", "err", err)
		}
		mi.busy.Unlock()
	}
}

func quoteIfNotJSON(b []byte) []byte {
	if len(b) == 0 {
		return []byte(`""`)
	}
	if json.Valid(b) {
		return b
	}
	q, _ := json.Marshal(string(b))
	return q
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type fnNotFoundError string

func (e fnNotFoundError) Error() string { return "function not found: " + string(e) }
func errFunctionNotFound(n string) error {
	return fnNotFoundError(n)
}
