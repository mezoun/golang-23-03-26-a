package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	mrand "math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ─────────────────────────────────────────────
//  Init — gob type registration (WAJIB untuk
//  encode/decode interface{} nested types)
// ─────────────────────────────────────────────

func init() {
	gob.Register(map[string]interface{}{})
	gob.Register([]interface{}{})
	gob.Register(map[string]string{})
	gob.Register(float64(0))
	gob.Register(bool(false))
	gob.Register("")
}

// ─────────────────────────────────────────────
//  Konstanta sistem — ubah sesuai kebutuhan
// ─────────────────────────────────────────────

const (
	// Batas multi-tenant
	maxConcurrentWorkers = 50  // max worker aktif bersamaan
	maxStoredWorkers     = 500 // max worker tersimpan di store
	maxStepsPerWorker    = 100 // max steps per worker
	maxConcurrentCalls   = 20  // max concurrent call_api simultan
	maxVarsBuckets       = 20  // max bucket dalam vars
	maxVarsPerBucket     = 50  // max key per bucket

	// Body size limit
	maxBodyBytes    = 128 << 10 // 128 KB — untuk /create, /update
	maxWebhookBytes = 64 << 10  // 64 KB — untuk webhook body

	// Retry
	maxRetryCount = 5

	// Sleep step
	maxSleepMs = 60_000 // 60 detik

	// Loop interval
	defaultLoopIntervalMs = 500 // 500ms default
	minLoopIntervalMs     = 200 // 200ms minimum

	// Storage
	storeFile   = "workers.gob"
	flushWindow = 300 * time.Millisecond

	// Log folder — per-worker log files
	logDir = "log"

	// File manager — output folder
	outputDir      = "output"
	maxUploadBytes = 10 << 20 // 10 MB per file (multipart upload)
	maxSaveBytes   = 5 << 20  // 5 MB per save — agent output biasanya < 1 MB
	maxOutputFiles = 200      // max file di folder output
	maxListFiles   = 500      // batas iterasi ReadDir

	// Staggered start saat boot
	staggerMinMs = 10
	staggerMaxMs = 40

	// Jump limit per run (proteksi infinite loop)
	jumpLimit = 1000
)

// Global semaphore untuk concurrent call_api
var callAPISem = make(chan struct{}, maxConcurrentCalls)

// ─────────────────────────────────────────────
//  Domain types
// ─────────────────────────────────────────────

type HTTPMethod string

const (
	MethodGET  HTTPMethod = "GET"
	MethodPOST HTTPMethod = "POST"
)

// RetryConfig konfigurasi retry untuk call_api.
type RetryConfig struct {
	Max     int  `json:"max"`      // max percobaan ulang (dikunci maxRetryCount)
	DelayMs int  `json:"delay_ms"` // delay awal antar retry (ms)
	Backoff bool `json:"backoff"`  // exponential backoff jika true
}

// StepValue holds config for each step type.
//
//	log      → { "message": "..." }
//	webhook  → { "method": "POST", "path": "/hook" }
//	call_api → { "method": "POST", "url": "...", "headers": {}, "body": {}, "retry": {...} }
//	branch   → { "cases": [...] }
//	sleep    → { "sleep_ms": 500 } atau { "sleep_seconds": 2 }
//	set_var  → { "set_key": "nama_field", "set_value": "{{step.field}}" }
type StepValue struct {
	// log
	Message string `json:"message,omitempty"`

	// webhook & call_api
	Method HTTPMethod `json:"method,omitempty"`

	// webhook
	Path string `json:"path,omitempty"`

	// call_api
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
	Retry   *RetryConfig      `json:"retry,omitempty"`

	// branch
	Cases []BranchCase `json:"cases,omitempty"`

	// sleep
	SleepMs      int `json:"sleep_ms,omitempty"`
	SleepSeconds int `json:"sleep_seconds,omitempty"`

	// set_var
	SetKey   string `json:"set_key,omitempty"`
	SetValue string `json:"set_value,omitempty"`
}

// BranchCase adalah satu cabang dalam step branch.
type BranchCase struct {
	When *Condition `json:"when,omitempty"` // nil = else
	Goto string     `json:"goto,omitempty"` // step ID tujuan; "" = sequential
}

// Condition adalah satu ekspresi perbandingan.
type Condition struct {
	Left  string `json:"left"`
	Op    string `json:"op"`
	Right string `json:"right"`
}

// Step adalah satu unit kerja dalam workflow.
type Step struct {
	ID     string          `json:"id"`
	Name   string          `json:"name,omitempty"`
	Type   string          `json:"type"`
	Value  *StepValue      `json:"value"`
	Output json.RawMessage `json:"output,omitempty"`
	Next   string          `json:"next,omitempty"`

	// runtime — tidak diserialisasi
	parsedOutput interface{}
}

type WorkerMode string

const (
	ModeOnce WorkerMode = "once"
	ModeLoop WorkerMode = "loop"
)

type Worker struct {
	ID             string                       `json:"id"`
	Name           string                       `json:"name"`
	Mode           WorkerMode                   `json:"mode"`
	Vars           map[string]map[string]string `json:"vars,omitempty"`
	Steps          []Step                       `json:"steps"`
	Running        bool                         `json:"running"`
	LoopIntervalMs int                          `json:"loop_interval_ms,omitempty"`
	Created        time.Time                    `json:"created"`
	Updated        time.Time                    `json:"updated"`
}

// loopDelay mengembalikan interval loop yang aman.
func (w *Worker) loopDelay() time.Duration {
	ms := w.LoopIntervalMs
	if ms < minLoopIntervalMs {
		ms = defaultLoopIntervalMs
	}
	return time.Duration(ms) * time.Millisecond
}

// ─────────────────────────────────────────────
//  WorkerStatus — runtime info, in-memory only
// ─────────────────────────────────────────────

type WorkerStatus struct {
	ID        string    `json:"id"`
	Running   bool      `json:"running"`
	LastRunAt time.Time `json:"last_run_at,omitempty"`
	LastRunOk bool      `json:"last_run_ok"`
	LastError string    `json:"last_error,omitempty"`
	RunCount  int64     `json:"run_count"`
}

// ─────────────────────────────────────────────
//  prepareSteps
// ─────────────────────────────────────────────

func prepareSteps(steps []Step) ([]Step, map[string]int) {
	out := make([]Step, len(steps))
	copy(out, steps)
	idx := make(map[string]int, len(steps))
	for i := range out {
		if len(out[i].Output) > 0 {
			var v interface{}
			if err := json.Unmarshal(out[i].Output, &v); err == nil {
				out[i].parsedOutput = v
			}
		}
		if out[i].ID != "" {
			idx[out[i].ID] = i
		}
	}
	return out, idx
}

// ─────────────────────────────────────────────
//  Storage — gob + async debounced flush
// ─────────────────────────────────────────────

type Store struct {
	mu    sync.RWMutex
	data  map[string]*Worker
	count int64 // atomic via atomic.AddInt64/LoadInt64
	dirty int32 // atomic bool: 0=clean, 1=dirty
	wake  chan struct{}
	done  chan struct{} // sinyal shutdown untuk flusher
	wg    sync.WaitGroup
}

func openStore() (*Store, error) {
	s := &Store{
		data: make(map[string]*Worker),
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
	f, err := os.Open(storeFile)
	if err != nil {
		if os.IsNotExist(err) {
			s.wg.Add(1)
			go s.flusher()
			return s, nil
		}
		return nil, err
	}
	defer f.Close()
	if err := gob.NewDecoder(f).Decode(&s.data); err != nil && err != io.EOF {
		log.Printf("warn: store decode failed (%v), starting fresh", err)
		s.data = make(map[string]*Worker)
	}
	var stale []string
	for id, w := range s.data {
		for _, step := range w.Steps {
			if step.Value == nil {
				stale = append(stale, id)
				break
			}
		}
	}
	for _, id := range stale {
		log.Printf("migrate: dropping worker %s — old step schema (re-create it)", id)
		delete(s.data, id)
	}
	atomic.StoreInt64(&s.count, int64(len(s.data)))
	s.wg.Add(1)
	go s.flusher()
	return s, nil
}

func (s *Store) flusher() {
	defer s.wg.Done()
	for {
		select {
		case <-s.done:
			// Shutdown — flush terakhir
			if atomic.LoadInt32(&s.dirty) == 1 {
				if err := s.flushNow(); err != nil {
					log.Printf("store: final flush error: %v", err)
				}
			}
			return
		case <-s.wake:
		}
		// Debounce: drain semua wake yang menumpuk
		time.Sleep(flushWindow)
		for {
			select {
			case <-s.wake:
			default:
				goto flush
			}
		}
	flush:
		if atomic.LoadInt32(&s.dirty) == 0 {
			continue
		}
		if err := s.flushNow(); err != nil {
			log.Printf("store: flush error: %v", err)
		} else {
			atomic.StoreInt32(&s.dirty, 0)
		}
	}
}

func (s *Store) markDirty() {
	atomic.StoreInt32(&s.dirty, 1)
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// shutdown menutup flusher dengan graceful flush terakhir dan menunggu flusher selesai.
func (s *Store) shutdown() {
	close(s.done)
	s.wg.Wait()
}

func (s *Store) flushNow() error {
	// Buat snapshot map dulu, baru lepas lock — encode di luar lock
	s.mu.RLock()
	snapshot := make(map[string]*Worker, len(s.data))
	for k, v := range s.data {
		cp := *v
		snapshot[k] = &cp
	}
	s.mu.RUnlock()

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(snapshot); err != nil {
		return err
	}
	dir := filepath.Dir(storeFile)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, "workers-*.gob.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	tmp.Close()
	return os.Rename(tmpName, storeFile)
}

func (s *Store) Save(w *Worker) {
	s.mu.Lock()
	_, exists := s.data[w.ID]
	cp := *w
	s.data[w.ID] = &cp
	if !exists {
		atomic.AddInt64(&s.count, 1)
	}
	s.mu.Unlock()
	s.markDirty()
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	if _, ok := s.data[id]; ok {
		delete(s.data, id)
		atomic.AddInt64(&s.count, -1)
	}
	s.mu.Unlock()
	s.markDirty()
}

// Get mengembalikan copy Worker — caller tidak bisa mutasi data store.
func (s *Store) Get(id string) (Worker, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.data[id]
	if !ok {
		return Worker{}, false
	}
	return *w, true
}

func (s *Store) List() []*Worker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*Worker, 0, len(s.data))
	for _, w := range s.data {
		cp := *w
		list = append(list, &cp)
	}
	return list
}

// Count mengembalikan jumlah worker tersimpan secara O(1).
func (s *Store) Count() int64 {
	return atomic.LoadInt64(&s.count)
}

// ─────────────────────────────────────────────
//  pipelineCtx
// ─────────────────────────────────────────────

type pipelineCtx struct {
	Steps map[string]map[string]interface{}
}

func newPipelineCtx(stepCount int) pipelineCtx {
	cap := stepCount
	if cap < 8 {
		cap = 8
	}
	return pipelineCtx{Steps: make(map[string]map[string]interface{}, cap)}
}

func (p *pipelineCtx) store(stepID string, data map[string]interface{}) {
	if stepID == "" {
		return
	}
	p.Steps[stepID] = data
}

func (p *pipelineCtx) reset() {
	for k := range p.Steps {
		delete(p.Steps, k)
	}
}

// ─────────────────────────────────────────────
//  webhookRouter
// ─────────────────────────────────────────────

type hookEntry struct {
	method   string
	workerID string
	arrived  chan map[string]interface{}
}

type webhookRouter struct {
	mu      sync.RWMutex
	entries map[string]*hookEntry
}

func newWebhookRouter() *webhookRouter {
	return &webhookRouter{entries: make(map[string]*hookEntry)}
}

func (wr *webhookRouter) register(path, method, workerID string, arrived chan map[string]interface{}) {
	wr.mu.Lock()
	wr.entries[path] = &hookEntry{method: method, workerID: workerID, arrived: arrived}
	wr.mu.Unlock()
	log.Printf("[worker:%s] WEBHOOK registered %s %s", workerID, method, path)
}

func (wr *webhookRouter) unregister(path string) {
	wr.mu.Lock()
	delete(wr.entries, path)
	wr.mu.Unlock()
}

func (wr *webhookRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	wr.mu.RLock()
	entry, ok := wr.entries[r.URL.Path]
	wr.mu.RUnlock()

	if !ok {
		http.NotFound(w, r)
		return
	}
	if entry.method != r.Method {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes))

	if len(body) > 256 {
		log.Printf("[worker:%s] WEBHOOK %s %s | body: %s... (%d bytes)",
			entry.workerID, r.Method, r.URL.Path, body[:256], len(body))
	} else {
		log.Printf("[worker:%s] WEBHOOK %s %s | body: %s",
			entry.workerID, r.Method, r.URL.Path, body)
	}

	bodyMap := make(map[string]interface{})
	_ = json.Unmarshal(body, &bodyMap)

	// Expose _query params
	if r.URL.RawQuery != "" {
		qmap := make(map[string]interface{})
		for k, v := range r.URL.Query() {
			if len(v) == 1 {
				qmap[k] = v[0]
			} else {
				sl := make([]interface{}, len(v))
				for i, vv := range v {
					sl[i] = vv
				}
				qmap[k] = sl
			}
		}
		bodyMap["_query"] = qmap
	}

	// Expose _headers (filter sensitif)
	blocked := map[string]bool{
		"Authorization": true, "Cookie": true, "Set-Cookie": true,
	}
	hmap := make(map[string]interface{})
	for k, v := range r.Header {
		if blocked[k] {
			continue
		}
		if len(v) == 1 {
			hmap[k] = v[0]
		} else {
			sl := make([]interface{}, len(v))
			for i, vv := range v {
				sl[i] = vv
			}
			hmap[k] = sl
		}
	}
	bodyMap["_headers"] = hmap

	w.Header().Set("Content-Type", "application/json")

	// HTTP 429 jika worker sedang memproses (channel penuh)
	select {
	case entry.arrived <- bodyMap:
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"received"}`)
	default:
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintln(w, `{"error":"worker busy, retry later"}`)
		log.Printf("[worker:%s] WEBHOOK %s %s DROPPED — worker busy (HTTP 429)",
			entry.workerID, r.Method, r.URL.Path)
	}
}

// ─────────────────────────────────────────────
//  Runtime
// ─────────────────────────────────────────────

type Runtime struct {
	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	statuses map[string]*WorkerStatus
}

func newRuntime() *Runtime {
	return &Runtime{
		cancels:  make(map[string]context.CancelFunc),
		statuses: make(map[string]*WorkerStatus),
	}
}

func (rt *Runtime) ActiveCount() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.cancels)
}

func (rt *Runtime) getStatus(id string) WorkerStatus {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if s, ok := rt.statuses[id]; ok {
		return *s
	}
	return WorkerStatus{ID: id}
}

func (rt *Runtime) updateStatus(id string, ok bool, errMsg string) {
	rt.mu.Lock()
	s, exists := rt.statuses[id]
	if !exists {
		s = &WorkerStatus{ID: id}
		rt.statuses[id] = s
	}
	s.LastRunAt = time.Now()
	s.LastRunOk = ok
	s.LastError = errMsg
	s.RunCount++
	rt.mu.Unlock()
}

func (rt *Runtime) Start(w Worker, store *Store, hooks *webhookRouter) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if _, running := rt.cancels[w.ID]; running {
		return
	}

	// Init status
	if _, ok := rt.statuses[w.ID]; !ok {
		rt.statuses[w.ID] = &WorkerStatus{ID: w.ID}
	}
	rt.statuses[w.ID].Running = true

	ctx, cancel := context.WithCancel(context.Background())
	rt.cancels[w.ID] = cancel

	preparedSteps, stepIndex := prepareSteps(w.Steps)
	mode := w.Mode
	if mode == "" {
		mode = ModeOnce
	}

	go func() {
		workerID := w.ID

		// FASE 0-A: recover dari panic — isolasi worker
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				errMsg := fmt.Sprintf("PANIC: %v", r)
				log.Printf("[worker:%s] %s\n%s", workerID, errMsg, stack)
				rt.updateStatus(workerID, false, errMsg)
				rt.mu.Lock()
				rt.statuses[workerID].Running = false
				delete(rt.cancels, workerID)
				rt.mu.Unlock()
				// Update store: running=false
				if wk, ok := store.Get(workerID); ok {
					wk.Running = false
					wk.Updated = time.Now()
					store.Save(&wk)
				}
			}
		}()

		pctx := newPipelineCtx(len(preparedSteps))
		timer := time.NewTimer(w.loopDelay())
		timer.Stop()

		for {
			// Refresh vars dari store tiap iterasi (live update vars tanpa restart)
			latestW, ok := store.Get(workerID)
			if !ok {
				break
			}

			runErr := runWorker(ctx, latestW, preparedSteps, stepIndex, hooks, &pctx)
			pctx.reset()

			if runErr == "" {
				rt.updateStatus(workerID, true, "")
			} else {
				rt.updateStatus(workerID, false, runErr)
			}

			select {
			case <-ctx.Done():
				timer.Stop()
				if wk, ok2 := store.Get(workerID); ok2 {
					wk.Running = false
					wk.Updated = time.Now()
					store.Save(&wk)
				}
				rt.mu.Lock()
				rt.statuses[workerID].Running = false
				delete(rt.cancels, workerID)
				rt.mu.Unlock()
				log.Printf("[worker:%s] stopped", workerID)
				return
			default:
			}

			if mode == ModeLoop {
				delay := latestW.loopDelay()
				timer.Reset(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					if wk, ok2 := store.Get(workerID); ok2 {
						wk.Running = false
						wk.Updated = time.Now()
						store.Save(&wk)
					}
					rt.mu.Lock()
					rt.statuses[workerID].Running = false
					delete(rt.cancels, workerID)
					rt.mu.Unlock()
					return
				case <-timer.C:
				}
				continue
			}

			// once — selesai
			if wk, ok2 := store.Get(workerID); ok2 {
				wk.Running = false
				wk.Updated = time.Now()
				store.Save(&wk)
			}
			rt.mu.Lock()
			rt.statuses[workerID].Running = false
			delete(rt.cancels, workerID)
			rt.mu.Unlock()
			return
		}
	}()
}

func (rt *Runtime) Stop(id string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if cancel, ok := rt.cancels[id]; ok {
		cancel()
		delete(rt.cancels, id)
	}
}

// StopAll menghentikan semua worker (untuk graceful shutdown).
func (rt *Runtime) StopAll() {
	rt.mu.Lock()
	ids := make([]string, 0, len(rt.cancels))
	for id, cancel := range rt.cancels {
		cancel()
		ids = append(ids, id)
	}
	for _, id := range ids {
		delete(rt.cancels, id)
	}
	rt.mu.Unlock()
	log.Printf("runtime: stopped %d worker(s)", len(ids))
}

func (rt *Runtime) IsRunning(id string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	_, ok := rt.cancels[id]
	return ok
}

// deleteStatus menghapus status worker dari memori — dipanggil saat worker didelete.
func (rt *Runtime) deleteStatus(id string) {
	rt.mu.Lock()
	delete(rt.statuses, id)
	rt.mu.Unlock()
}

// ─────────────────────────────────────────────
//  runWorker — index-based traversal + goto
//  Return: error string (kosong = sukses)
// ─────────────────────────────────────────────

func runWorker(
	ctx context.Context,
	w Worker,
	steps []Step,
	stepIndex map[string]int,
	hooks *webhookRouter,
	pctx *pipelineCtx,
) string {
	vars := w.Vars
	if vars == nil {
		vars = map[string]map[string]string{}
	}

	jumps := 0
	i := 0
	lastErr := ""

	for i < len(steps) {
		select {
		case <-ctx.Done():
			log.Printf("[worker:%s] stopped mid-run", w.ID)
			return "stopped"
		default:
		}

		step := steps[i]

		if step.Value == nil {
			i++
			continue
		}

		nextIdx := i + 1
		jumped := false

		switch step.Type {

		case "webhook":
			out := execWebhook(ctx, w.ID, step, hooks)
			pctx.store(step.ID, out)
			if step.parsedOutput != nil {
				pctx.store(step.ID, renderOutputParsed(step.parsedOutput, *pctx, vars))
			}

		case "log":
			msg := renderTemplate(step.Value.Message, *pctx, vars)
			appendWorkerLog(w.ID, fmt.Sprintf("[step:%s] %s", step.stepLabel(), msg))
			pctx.store(step.ID, map[string]interface{}{"message": msg})
			if step.parsedOutput != nil {
				pctx.store(step.ID, renderOutputParsed(step.parsedOutput, *pctx, vars))
			}

		case "call_api":
			out := execCallAPI(ctx, w.ID, step, *pctx, vars)
			pctx.store(step.ID, out)
			if step.parsedOutput != nil {
				pctx.store(step.ID, renderOutputParsed(step.parsedOutput, *pctx, vars))
			}
			if errVal, ok := out["_error"]; ok {
				lastErr = fmt.Sprintf("call_api step %s: %v", step.stepLabel(), errVal)
			}

		case "branch":
			gotoID := execBranch(w.ID, step, *pctx, vars)
			if gotoID != "" {
				if target, ok := stepIndex[gotoID]; ok {
					jumps++
					if jumps > jumpLimit {
						msg := fmt.Sprintf("[worker:%s][step:%s] BRANCH jump limit (%d) exceeded — aborting run",
							w.ID, step.stepLabel(), jumpLimit)
						log.Print(msg)
						return msg
					}
					nextIdx = target
					jumped = true
					log.Printf("[worker:%s][step:%s] BRANCH → goto %q (idx %d)",
						w.ID, step.stepLabel(), gotoID, target)
				} else {
					log.Printf("[worker:%s][step:%s] BRANCH goto %q not found — sequential",
						w.ID, step.stepLabel(), gotoID)
				}
			} else {
				log.Printf("[worker:%s][step:%s] BRANCH → no case matched — sequential",
					w.ID, step.stepLabel())
			}

		case "sleep":
			execSleep(ctx, w.ID, step, *pctx, vars)
			pctx.store(step.ID, map[string]interface{}{"done": true})
			if step.parsedOutput != nil {
				pctx.store(step.ID, renderOutputParsed(step.parsedOutput, *pctx, vars))
			}

		case "set_var":
			execSetVar(w.ID, step, pctx, vars)
			if step.parsedOutput != nil {
				pctx.store(step.ID, renderOutputParsed(step.parsedOutput, *pctx, vars))
			}

		default:
			log.Printf("[worker:%s][step:%s] unknown type: %q", w.ID, step.stepLabel(), step.Type)
		}

		// step.Next override (hanya aktif jika branch belum jump)
		if !jumped && step.Next != "" {
			if target, ok := stepIndex[step.Next]; ok {
				jumps++
				if jumps > jumpLimit {
					msg := fmt.Sprintf("[worker:%s][step:%s] NEXT jump limit (%d) exceeded — aborting run",
						w.ID, step.stepLabel(), jumpLimit)
					log.Print(msg)
					return msg
				}
				nextIdx = target
				log.Printf("[worker:%s][step:%s] NEXT → goto %q (idx %d)",
					w.ID, step.stepLabel(), step.Next, target)
			}
		}

		i = nextIdx
	}

	log.Printf("[worker:%s] workflow completed", w.ID)
	return lastErr
}

// ─────────────────────────────────────────────
//  Branch executor
// ─────────────────────────────────────────────

func execBranch(workerID string, step Step, pctx pipelineCtx, vars map[string]map[string]string) string {
	if step.Value == nil || len(step.Value.Cases) == 0 {
		return ""
	}
	for i, c := range step.Value.Cases {
		if c.When == nil {
			log.Printf("[worker:%s][step:%s] BRANCH case[%d] else → goto %q",
				workerID, step.stepLabel(), i, c.Goto)
			return c.Goto
		}
		if evalCondition(c.When, pctx, vars) {
			log.Printf("[worker:%s][step:%s] BRANCH case[%d] match (%q %s %q) → goto %q",
				workerID, step.stepLabel(), i,
				renderTemplate(c.When.Left, pctx, vars),
				c.When.Op,
				renderTemplate(c.When.Right, pctx, vars),
				c.Goto)
			return c.Goto
		}
	}
	return ""
}

func evalCondition(c *Condition, pctx pipelineCtx, vars map[string]map[string]string) bool {
	left := renderTemplate(c.Left, pctx, vars)
	right := renderTemplate(c.Right, pctx, vars)
	op := strings.TrimSpace(c.Op)

	lf, lErr := strconv.ParseFloat(left, 64)
	rf, rErr := strconv.ParseFloat(right, 64)
	numeric := lErr == nil && rErr == nil

	switch op {
	case "==":
		if numeric {
			return lf == rf
		}
		return left == right
	case "!=":
		if numeric {
			return lf != rf
		}
		return left != right
	case ">":
		if numeric {
			return lf > rf
		}
		return left > right
	case "<":
		if numeric {
			return lf < rf
		}
		return left < right
	case ">=":
		if numeric {
			return lf >= rf
		}
		return left >= right
	case "<=":
		if numeric {
			return lf <= rf
		}
		return left <= right
	case "contains":
		return strings.Contains(left, right)
	case "starts_with":
		return strings.HasPrefix(left, right)
	case "ends_with":
		return strings.HasSuffix(left, right)
	default:
		log.Printf("branch: unknown op %q — treated as false", op)
		return false
	}
}

// ─────────────────────────────────────────────
//  Step executors
// ─────────────────────────────────────────────

func (s Step) stepLabel() string {
	if s.Name != "" {
		return s.Name + "(" + s.ID + ")"
	}
	return s.ID
}

func execWebhook(ctx context.Context, workerID string, step Step, hooks *webhookRouter) map[string]interface{} {
	cfg := step.Value
	fullPath := "/" + workerID + cfg.Path

	arrived := make(chan map[string]interface{}, 1)
	hooks.register(fullPath, string(cfg.Method), workerID, arrived)
	defer hooks.unregister(fullPath)

	select {
	case bodyMap := <-arrived:
		log.Printf("[worker:%s][step:%s] WEBHOOK triggered", workerID, step.stepLabel())
		return bodyMap
	case <-ctx.Done():
		log.Printf("[worker:%s][step:%s] WEBHOOK cancelled", workerID, step.stepLabel())
		return map[string]interface{}{}
	}
}

// HTTP client dengan connection pool yang dioptimalkan untuk 1 CPU / 1GB RAM
var apiClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     30 * time.Second,
		DisableKeepAlives:   false,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	},
}

// acquireCallSlot mengambil slot semaphore call_api, aware ctx.
func acquireCallSlot(ctx context.Context) bool {
	select {
	case callAPISem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func releaseCallSlot() {
	<-callAPISem
}

func execCallAPI(ctx context.Context, workerID string, step Step, pctx pipelineCtx, vars map[string]map[string]string) map[string]interface{} {
	cfg := step.Value

	// Retry config
	maxTry := 1
	delayMs := 500
	backoff := false
	if cfg.Retry != nil {
		maxTry = cfg.Retry.Max + 1
		if maxTry > maxRetryCount+1 {
			maxTry = maxRetryCount + 1
		}
		if maxTry < 1 {
			maxTry = 1
		}
		delayMs = cfg.Retry.DelayMs
		if delayMs < 0 {
			delayMs = 0
		}
		backoff = cfg.Retry.Backoff
	}

	var lastOut map[string]interface{}

	for attempt := 0; attempt < maxTry; attempt++ {
		if attempt > 0 {
			delay := time.Duration(delayMs) * time.Millisecond
			if backoff {
				delay = delay * time.Duration(1<<uint(attempt-1))
			}
			select {
			case <-ctx.Done():
				return map[string]interface{}{"_error": "cancelled", "_attempts": attempt}
			case <-time.After(delay):
			}
			log.Printf("[worker:%s][step:%s] CALL_API retry %d/%d", workerID, step.stepLabel(), attempt, maxTry-1)
		}

		// Acquire semaphore
		if !acquireCallSlot(ctx) {
			return map[string]interface{}{"_error": "cancelled (semaphore)", "_attempts": attempt + 1}
		}

		out, retry := doCallAPI(ctx, workerID, step, pctx, vars, attempt)
		releaseCallSlot()

		lastOut = out
		if !retry {
			return out
		}
	}

	if lastOut == nil {
		lastOut = map[string]interface{}{}
	}
	lastOut["_attempts"] = maxTry
	return lastOut
}

func doCallAPI(ctx context.Context, workerID string, step Step, pctx pipelineCtx, vars map[string]map[string]string, attempt int) (map[string]interface{}, bool) {
	cfg := step.Value
	renderedURL := renderTemplate(cfg.URL, pctx, vars)

	var reqBody io.Reader
	if len(cfg.Body) > 0 {
		renderedBody := renderTemplate(string(cfg.Body), pctx, vars)
		reqBody = bytes.NewReader([]byte(renderedBody))
	}
	req, err := http.NewRequestWithContext(ctx, string(cfg.Method), renderedURL, reqBody)
	if err != nil {
		log.Printf("[worker:%s][step:%s] CALL_API build error: %v", workerID, step.stepLabel(), err)
		return map[string]interface{}{"_error": err.Error(), "_attempts": attempt + 1}, false
	}
	if len(cfg.Body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, renderTemplate(v, pctx, vars))
	}

	resp, err := apiClient.Do(req)
	if err != nil {
		log.Printf("[worker:%s][step:%s] CALL_API error: %v", workerID, step.stepLabel(), err)
		// Network error → bisa retry
		return map[string]interface{}{"_error": err.Error(), "_attempts": attempt + 1}, cfg.Retry != nil
	}
	defer resp.Body.Close()

	respRaw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if len(respRaw) > 256 {
		log.Printf("[worker:%s][step:%s] CALL_API %s %s → HTTP %d | %s... (%d bytes)",
			workerID, step.stepLabel(), cfg.Method, renderedURL, resp.StatusCode, respRaw[:256], len(respRaw))
	} else {
		log.Printf("[worker:%s][step:%s] CALL_API %s %s → HTTP %d | %s",
			workerID, step.stepLabel(), cfg.Method, renderedURL, resp.StatusCode, respRaw)
	}

	// Retry untuk 429 dan 5xx
	shouldRetry := cfg.Retry != nil && (resp.StatusCode == 429 || resp.StatusCode >= 500)

	out := make(map[string]interface{})
	if err := json.Unmarshal(respRaw, &out); err != nil {
		out["raw"] = string(respRaw)
		out["status"] = resp.StatusCode
	}
	out["_status"] = resp.StatusCode

	if shouldRetry {
		out["_error"] = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return out, shouldRetry
}

// execSleep — sleep cancellable, max 60 detik.
func execSleep(ctx context.Context, workerID string, step Step, pctx pipelineCtx, vars map[string]map[string]string) {
	cfg := step.Value
	ms := cfg.SleepMs
	if cfg.SleepSeconds > 0 {
		ms = cfg.SleepSeconds * 1000
	}
	if ms > maxSleepMs {
		ms = maxSleepMs
	}
	if ms <= 0 {
		return
	}
	log.Printf("[worker:%s][step:%s] SLEEP %dms", workerID, step.stepLabel(), ms)
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
	case <-ctx.Done():
		log.Printf("[worker:%s][step:%s] SLEEP cancelled", workerID, step.stepLabel())
	}
}

// execSetVar — simpan nilai ke pctx.
func execSetVar(workerID string, step Step, pctx *pipelineCtx, vars map[string]map[string]string) {
	cfg := step.Value
	if cfg.SetKey == "" {
		log.Printf("[worker:%s][step:%s] SET_VAR: set_key kosong", workerID, step.stepLabel())
		return
	}
	rendered := renderTemplate(cfg.SetValue, *pctx, vars)
	pctx.store(step.ID, map[string]interface{}{
		cfg.SetKey: rendered,
	})
	log.Printf("[worker:%s][step:%s] SET_VAR %q = %q", workerID, step.stepLabel(), cfg.SetKey, rendered)
}

// ─────────────────────────────────────────────
//  Template rendering
// ─────────────────────────────────────────────

func renderTemplate(s string, pctx pipelineCtx, vars map[string]map[string]string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	for pass := 0; pass < 20; pass++ {
		next := applyTemplates(s, pctx, vars)
		if next == s {
			break
		}
		s = next
	}
	return s
}

func applyTemplates(s string, pctx pipelineCtx, vars map[string]map[string]string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		start := strings.Index(s[i:], "{{")
		if start == -1 {
			b.WriteString(s[i:])
			break
		}
		start += i
		b.WriteString(s[i:start])

		end := strings.Index(s[start+2:], "}}")
		if end == -1 {
			b.WriteString(s[start:])
			break
		}
		end = start + 2 + end
		placeholder := s[start : end+2]
		inner := strings.TrimSpace(s[start+2 : end])

		if resolved, ok := resolveToken(inner, pctx, vars); ok {
			b.WriteString(resolved)
		} else {
			b.WriteString(placeholder)
		}
		i = end + 2
	}
	return b.String()
}

func resolveToken(inner string, pctx pipelineCtx, vars map[string]map[string]string) (string, bool) {
	// Prefix json: → JSON-safe escape
	jsonMode := strings.HasPrefix(inner, "json:")
	if jsonMode {
		inner = inner[5:]
	}

	dotIdx := strings.Index(inner, ".")
	if dotIdx <= 0 {
		return "", false
	}
	ns := inner[:dotIdx]
	key := inner[dotIdx+1:]

	var resolved string
	found := false

	if bucket, ok := vars[ns]; ok {
		resolved = bucket[key]
		found = true
	} else if stepData, ok := pctx.Steps[ns]; ok {
		resolved = valueToString(resolveKey(key, stepData))
		found = true
	}

	if !found {
		return "", false
	}

	if jsonMode {
		// JSON-safe: escape untuk diinjeksikan dalam string JSON
		b, _ := json.Marshal(resolved)
		// json.Marshal menghasilkan "\"...\""  — strip outer quotes
		if len(b) >= 2 {
			return string(b[1 : len(b)-1]), true
		}
	}
	return resolved, true
}

func valueToString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	case int:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func resolveKey(key string, data map[string]interface{}) interface{} {
	parts := strings.SplitN(key, ".", 2)
	val, ok := data[parts[0]]
	if !ok {
		return ""
	}
	if len(parts) == 1 {
		return val
	}
	if nested, ok := val.(map[string]interface{}); ok {
		return resolveKey(parts[1], nested)
	}
	return ""
}

func renderOutputParsed(v interface{}, pctx pipelineCtx, vars map[string]map[string]string) map[string]interface{} {
	rendered := renderValue(v, pctx, vars)
	if m, ok := rendered.(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{"value": rendered}
}

func renderValue(v interface{}, pctx pipelineCtx, vars map[string]map[string]string) interface{} {
	switch val := v.(type) {
	case string:
		return renderTemplate(val, pctx, vars)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, vv := range val {
			out[k] = renderValue(vv, pctx, vars)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, vv := range val {
			out[i] = renderValue(vv, pctx, vars)
		}
		return out
	default:
		return v
	}
}

// ─────────────────────────────────────────────
//  Helpers
// ─────────────────────────────────────────────

// stepsHash menghasilkan hash FNV-64 dari steps JSON — lebih cepat dari DeepEqual.
func stepsHash(steps []Step) uint64 {
	b, _ := json.Marshal(steps)
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}

// validateStepIDs memastikan tidak ada duplikasi ID.
func validateStepIDs(steps []Step) error {
	seen := make(map[string]bool, len(steps))
	for _, s := range steps {
		if s.ID != "" {
			if seen[s.ID] {
				return fmt.Errorf("duplicate step ID: %s", s.ID)
			}
			seen[s.ID] = true
		}
	}
	return nil
}

// validateVars memastikan vars tidak melebihi batas.
func validateVars(vars map[string]map[string]string) error {
	if len(vars) > maxVarsBuckets {
		return fmt.Errorf("max %d vars buckets allowed", maxVarsBuckets)
	}
	for bucket, m := range vars {
		if len(m) > maxVarsPerBucket {
			return fmt.Errorf("bucket %q exceeds max %d keys", bucket, maxVarsPerBucket)
		}
	}
	return nil
}

// ─────────────────────────────────────────────
//  HTTP handlers
// ─────────────────────────────────────────────

type App struct {
	store   *Store
	runtime *Runtime
	hooks   *webhookRouter
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("writeJSON: encode error: %v", err)
		http.Error(w, `{"error":"internal encode error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if _, err := w.Write(b); err != nil {
		log.Printf("writeJSON: write error: %v", err)
	}
}

func errJSON(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func uniqueID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func stepID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return fmt.Sprintf("%08x", b)
}

func assignStepIDs(steps []Step) {
	for i := range steps {
		if steps[i].ID == "" {
			steps[i].ID = stepID()
		}
	}
}

// requireJSON memvalidasi Content-Type untuk POST/PUT.
func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		errJSON(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	return true
}

// ─────────────────────────────────────────────
//  File Manager — /files/upload, /files/list,
//                 /files/view, /files/delete
// ─────────────────────────────────────────────

// fileInfo adalah metadata file untuk respons JSON.
type fileInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size_bytes"`
	ModTime time.Time `json:"modified"`
}

// ensureOutputDir memastikan folder output ada.
func ensureOutputDir() error {
	return os.MkdirAll(outputDir, 0o755)
}

// appendWorkerLog menulis satu baris log ke file log/{workerID}.log secara append.
// Operasi ini instant (OS-level file write), tidak ada API call, tidak ada re-render.
// Format: 2006-01-02T15:04:05.000Z07:00 <message>\n
func appendWorkerLog(workerID, message string) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}
	fpath := filepath.Join(logDir, workerID+".log")
	f, err := os.OpenFile(fpath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	ts := time.Now().Format("2006-01-02T15:04:05.000Z07:00")
	_, _ = fmt.Fprintf(f, "%s %s\n", ts, message)
	_ = f.Close()
}

// safeFilename mencegah path traversal: hanya izinkan nama file tanpa
// separator direktori apapun. Mengembalikan ("", false) jika tidak aman.
func safeFilename(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	clean := filepath.Base(filepath.Clean(name))
	if clean == "." || clean == ".." || strings.ContainsAny(clean, "/\\") {
		return "", false
	}
	return clean, true
}

// POST /files/upload
// Content-Type: multipart/form-data, field name "file"
//
// Curl contoh:
//
//	curl -X POST http://localhost:8080/files/upload \
//	     -F "file=@/path/to/yourfile.txt"
func (a *App) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "POST only")
		return
	}

	// Batasi total body agar tidak kebanjiran RAM
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+4096)

	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		errJSON(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file terlalu besar (maks %d MB) atau form tidak valid", maxUploadBytes>>20))
		return
	}
	defer r.MultipartForm.RemoveAll() //nolint:errcheck

	src, header, err := r.FormFile("file")
	if err != nil {
		errJSON(w, http.StatusBadRequest, "field 'file' wajib ada")
		return
	}
	defer src.Close()

	filename, ok := safeFilename(header.Filename)
	if !ok {
		errJSON(w, http.StatusBadRequest, "nama file tidak valid")
		return
	}

	if err := ensureOutputDir(); err != nil {
		errJSON(w, http.StatusInternalServerError, "tidak dapat membuat folder output")
		return
	}

	// Cek jumlah file yang sudah ada
	entries, _ := os.ReadDir(outputDir)
	fileCount := 0
	for _, e := range entries {
		if !e.IsDir() {
			fileCount++
		}
	}
	if fileCount >= maxOutputFiles {
		errJSON(w, http.StatusInsufficientStorage,
			fmt.Sprintf("folder output penuh (maks %d file)", maxOutputFiles))
		return
	}

	dstPath := filepath.Join(outputDir, filename)
	out, err := os.Create(dstPath)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "gagal membuat file")
		return
	}
	defer out.Close()

	written, err := io.Copy(out, src)
	if err != nil {
		out.Close()
		os.Remove(dstPath)
		errJSON(w, http.StatusInternalServerError, "gagal menulis file")
		return
	}

	log.Printf("[files] upload: %s (%d bytes)", filename, written)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"name":       filename,
		"size_bytes": written,
		"path":       dstPath,
	})
}

// GET /files/list
//
// Curl contoh:
//
//	curl http://localhost:8080/files/list
func (a *App) handleFileList(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, []fileInfo{})
			return
		}
		errJSON(w, http.StatusInternalServerError, "tidak dapat membaca folder output")
		return
	}

	result := make([]fileInfo, 0, len(entries))
	for i, e := range entries {
		if i >= maxListFiles {
			break
		}
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, fileInfo{
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	writeJSON(w, http.StatusOK, result)
}

// GET /files/view?name=<filename>
// Mengirim file sebagai download (Content-Disposition: attachment).
//
// Curl contoh:
//
//	curl -O -J "http://localhost:8080/files/view?name=report.pdf"
//	# atau sekadar tampilkan:
//	curl "http://localhost:8080/files/view?name=data.json"
func (a *App) handleFileView(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	filename, ok := safeFilename(name)
	if !ok {
		errJSON(w, http.StatusBadRequest, "parameter 'name' tidak valid atau kosong")
		return
	}

	fpath := filepath.Join(outputDir, filename)

	info, err := os.Stat(fpath)
	if err != nil {
		if os.IsNotExist(err) {
			errJSON(w, http.StatusNotFound, "file tidak ditemukan")
		} else {
			errJSON(w, http.StatusInternalServerError, "tidak dapat mengakses file")
		}
		return
	}
	if info.IsDir() {
		errJSON(w, http.StatusBadRequest, "bukan file")
		return
	}

	// Paksa download; hapus header ini jika ingin inline di browser
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	http.ServeFile(w, r, fpath)
}

// generateAgentFilename menghasilkan nama file unik untuk hasil agent.
// Format: agent-{tag}_{YYYYMMDD-HHMMSS}_{rand8hex}.json
// Jika tag kosong: agent_{YYYYMMDD-HHMMSS}_{rand8hex}.json
func generateAgentFilename(tag string) string {
	ts := time.Now().Format("20060102-150405")
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		b[0] = byte(time.Now().UnixNano())
	}
	randPart := fmt.Sprintf("%08x", b)
	if tag == "" {
		return fmt.Sprintf("agent_%s_%s.json", ts, randPart)
	}
	// Bersihkan tag: hanya huruf, angka, dash
	var clean []byte
	for _, c := range []byte(tag) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' {
			clean = append(clean, c)
		}
	}
	if len(clean) == 0 {
		clean = []byte("agent")
	}
	if len(clean) > 32 {
		clean = clean[:32]
	}
	return fmt.Sprintf("agent-%s_%s_%s.json", clean, ts, randPart)
}

// saveRequest adalah payload untuk /files/save.
type saveRequest struct {
	Tag     string `json:"tag"`     // opsional — label agent, muncul di nama file
	Content string `json:"content"` // wajib — isi file (string/JSON)
}

// POST /files/save
// Menerima JSON body dan menyimpan isinya ke folder output/ sebagai file baru.
// Nama file di-generate otomatis agar unik dan jelas berasal dari agent.
// Cocok dipanggil via call_api dari dalam pipeline worker.
//
// Request body:
//
//	{ "tag": "recipe", "content": "{\"title\":\"Sate Padang\",...}" }
//
// Response:
//
//	{ "name": "agent-recipe_20250326-143022_a1b2c3d4.json",
//	  "size_bytes": 2345,
//	  "view_url": "/files/view?name=agent-recipe_20250326-143022_a1b2c3d4.json" }
//
// Curl contoh (manual):
//
//	curl -s -X POST http://localhost:8080/files/save \
//	     -H "Content-Type: application/json" \
//	     -d '{"tag":"recipe","content":"{\"title\":\"Rendang\"}"}' | jq .
func (a *App) handleFileSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !requireJSON(w, r) {
		return
	}

	var req saveRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, int64(maxSaveBytes)+512)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Content == "" {
		errJSON(w, http.StatusBadRequest, "field 'content' wajib ada dan tidak boleh kosong")
		return
	}
	if len(req.Content) > maxSaveBytes {
		errJSON(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("content terlalu besar (maks %d MB)", maxSaveBytes>>20))
		return
	}

	if err := ensureOutputDir(); err != nil {
		errJSON(w, http.StatusInternalServerError, "tidak dapat membuat folder output")
		return
	}

	// Cek kuota file
	entries, _ := os.ReadDir(outputDir)
	fileCount := 0
	for _, e := range entries {
		if !e.IsDir() {
			fileCount++
		}
	}
	if fileCount >= maxOutputFiles {
		errJSON(w, http.StatusInsufficientStorage,
			fmt.Sprintf("folder output penuh (maks %d file)", maxOutputFiles))
		return
	}

	filename := generateAgentFilename(req.Tag)
	dstPath := filepath.Join(outputDir, filename)

	if err := os.WriteFile(dstPath, []byte(req.Content), 0o644); err != nil {
		errJSON(w, http.StatusInternalServerError, "gagal menyimpan file")
		return
	}

	viewURL := "/files/view?name=" + filename
	log.Printf("[files] save: %s (%d bytes) tag=%q", filename, len(req.Content), req.Tag)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"name":       filename,
		"size_bytes": len(req.Content),
		"view_url":   viewURL,
	})
}

// POST /files/delete?name=<filename>
//
// Curl contoh:
//
//	curl -X DELETE "http://localhost:8080/files/delete?name=report.pdf"
func (a *App) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		errJSON(w, http.StatusMethodNotAllowed, "DELETE only")
		return
	}

	name := r.URL.Query().Get("name")
	filename, ok := safeFilename(name)
	if !ok {
		errJSON(w, http.StatusBadRequest, "parameter 'name' tidak valid atau kosong")
		return
	}

	fpath := filepath.Join(outputDir, filename)
	if _, err := os.Stat(fpath); os.IsNotExist(err) {
		errJSON(w, http.StatusNotFound, "file tidak ditemukan")
		return
	}

	if err := os.Remove(fpath); err != nil {
		errJSON(w, http.StatusInternalServerError, "gagal menghapus file")
		return
	}

	log.Printf("[files] delete: %s", filename)
	writeJSON(w, http.StatusOK, map[string]string{"deleted": filename})
}

// POST /create
func (a *App) handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !requireJSON(w, r) {
		return
	}

	// Cek batas total stored
	if a.store.Count() >= maxStoredWorkers {
		errJSON(w, http.StatusServiceUnavailable, fmt.Sprintf("max %d stored workers reached", maxStoredWorkers))
		return
	}

	var worker Worker
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&worker); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Validasi steps
	if len(worker.Steps) > maxStepsPerWorker {
		errJSON(w, http.StatusBadRequest, fmt.Sprintf("max %d steps per worker", maxStepsPerWorker))
		return
	}
	if err := validateStepIDs(worker.Steps); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateVars(worker.Vars); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	if worker.ID == "" {
		worker.ID = uniqueID()
	}
	assignStepIDs(worker.Steps)
	worker.Created = time.Now()
	worker.Updated = time.Now()
	if worker.Mode == "" {
		worker.Mode = ModeOnce
	}
	a.store.Save(&worker)

	if worker.Running {
		// Cek batas concurrent
		if a.runtime.ActiveCount() >= maxConcurrentWorkers {
			worker.Running = false
			a.store.Save(&worker)
			writeJSON(w, http.StatusCreated, worker)
			log.Printf("[worker:%s] created but not started — max concurrent workers (%d) reached", worker.ID, maxConcurrentWorkers)
			return
		}
		a.runtime.Start(worker, a.store, a.hooks)
	}
	writeJSON(w, http.StatusCreated, worker)
}

// GET /list
func (a *App) handleList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.List())
}

// GET /get?id=<id>
func (a *App) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	worker, ok := a.store.Get(id)
	if !ok {
		errJSON(w, http.StatusNotFound, "worker not found")
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

// GET /status?id=<id>
func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if _, ok := a.store.Get(id); !ok {
		errJSON(w, http.StatusNotFound, "worker not found")
		return
	}
	st := a.runtime.getStatus(id)
	st.Running = a.runtime.IsRunning(id)
	writeJSON(w, http.StatusOK, st)
}

// PUT /update
func (a *App) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		errJSON(w, http.StatusMethodNotAllowed, "PUT only")
		return
	}
	if !requireJSON(w, r) {
		return
	}
	var updated Worker
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&updated); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	existing, ok := a.store.Get(updated.ID)
	if !ok {
		errJSON(w, http.StatusNotFound, "worker not found")
		return
	}

	// Validasi
	if len(updated.Steps) > maxStepsPerWorker {
		errJSON(w, http.StatusBadRequest, fmt.Sprintf("max %d steps per worker", maxStepsPerWorker))
		return
	}
	if err := validateStepIDs(updated.Steps); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateVars(updated.Vars); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	updated.Created = existing.Created
	updated.Updated = time.Now()

	// Ganti DeepEqual dengan FNV hash — lebih cepat dan akurat untuk RawMessage
	wasRunning := a.runtime.IsRunning(updated.ID)
	if wasRunning {
		// Stop jika: (a) caller meminta berhenti, atau (b) steps berubah
		if !updated.Running || stepsHash(existing.Steps) != stepsHash(updated.Steps) {
			a.runtime.Stop(updated.ID)
			wasRunning = false
		}
	}
	assignStepIDs(updated.Steps)
	a.store.Save(&updated)

	if updated.Running && !wasRunning {
		if a.runtime.ActiveCount() >= maxConcurrentWorkers {
			updated.Running = false
			a.store.Save(&updated)
			writeJSON(w, http.StatusOK, updated)
			return
		}
		a.runtime.Start(updated, a.store, a.hooks)
	}
	writeJSON(w, http.StatusOK, updated)
}

// DELETE /delete?id=<id>
func (a *App) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		errJSON(w, http.StatusMethodNotAllowed, "DELETE only")
		return
	}
	id := r.URL.Query().Get("id")
	if _, ok := a.store.Get(id); !ok {
		errJSON(w, http.StatusNotFound, "worker not found")
		return
	}
	a.runtime.Stop(id)
	a.runtime.deleteStatus(id)
	a.store.Delete(id)
	writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

// POST /run?id=<id>
func (a *App) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	id := r.URL.Query().Get("id")
	worker, ok := a.store.Get(id)
	if !ok {
		errJSON(w, http.StatusNotFound, "worker not found")
		return
	}
	if a.runtime.IsRunning(id) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "already running", "mode": worker.Mode})
		return
	}
	// Cek limit concurrent
	if a.runtime.ActiveCount() >= maxConcurrentWorkers {
		errJSON(w, http.StatusServiceUnavailable, fmt.Sprintf("max %d concurrent workers reached", maxConcurrentWorkers))
		return
	}
	worker.Running = true
	worker.Updated = time.Now()
	a.store.Save(&worker)
	a.runtime.Start(worker, a.store, a.hooks)
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "started", "mode": worker.Mode})
}

// POST /stop?id=<id>
func (a *App) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	id := r.URL.Query().Get("id")
	worker, ok := a.store.Get(id)
	if !ok {
		errJSON(w, http.StatusNotFound, "worker not found")
		return
	}
	a.runtime.Stop(id)
	worker.Running = false
	worker.Updated = time.Now()
	a.store.Save(&worker)
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// ─────────────────────────────────────────────
//  Entry point
// ─────────────────────────────────────────────

func main() {
	// FASE 2-A: Hapus GOMAXPROCS(1) — Go scheduler mengatur sendiri
	// runtime.GOMAXPROCS(1) ← dihapus

	store, err := openStore()
	if err != nil {
		log.Fatalf("cannot open store: %v", err)
	}

	hooks := newWebhookRouter()
	rt := newRuntime()
	app := &App{store: store, runtime: rt, hooks: hooks}

	mux := http.NewServeMux()
	mux.HandleFunc("/create", app.handleCreate)
	mux.HandleFunc("/list", app.handleList)
	mux.HandleFunc("/get", app.handleGet)
	mux.HandleFunc("/status", app.handleStatus)
	mux.HandleFunc("/update", app.handleUpdate)
	mux.HandleFunc("/delete", app.handleDelete)
	mux.HandleFunc("/run", app.handleRun)
	mux.HandleFunc("/stop", app.handleStop)
	// File manager
	mux.HandleFunc("/files/upload", app.handleFileUpload)
	mux.HandleFunc("/files/list", app.handleFileList)
	mux.HandleFunc("/files/view", app.handleFileView)
	mux.HandleFunc("/files/delete", app.handleFileDelete)
	mux.HandleFunc("/files/save", app.handleFileSave)
	mux.Handle("/", hooks)

	// FASE 3-F: Staggered start — jeda random antar worker saat boot
	runningWorkers := make([]Worker, 0)
	for _, w := range store.List() {
		if w.Running {
			runningWorkers = append(runningWorkers, *w)
		}
	}
	log.Printf("auto-starting %d worker(s) with staggered delay", len(runningWorkers))
	for i, w := range runningWorkers {
		wCopy := w
		delay := time.Duration(staggerMinMs+mrand.Intn(staggerMaxMs)) * time.Millisecond * time.Duration(i+1)
		time.AfterFunc(delay, func() {
			log.Printf("auto-starting worker %s (%s)", wCopy.ID, wCopy.Name)
			app.runtime.Start(wCopy, store, hooks)
		})
	}

	addr := ":8080"
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// FASE 0-B: Graceful shutdown
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("worker-engine listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-sigCtx.Done()
	log.Println("shutdown signal received — stopping workers...")

	// Stop semua worker
	rt.StopAll()

	// Beri waktu 5 detik untuk server selesai
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}

	// Flush terakhir store — blocking sampai flusher benar-benar selesai
	store.shutdown()

	log.Println("worker-engine exited cleanly")
}
