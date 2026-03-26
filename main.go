package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─────────────────────────────────────────────
//  Domain types
// ─────────────────────────────────────────────

type HTTPMethod string

const (
	MethodGET  HTTPMethod = "GET"
	MethodPOST HTTPMethod = "POST"
)

// StepValue holds config for each step type.
//
//	log      → { "message": "..." }
//	webhook  → { "method": "POST", "path": "/hook" }
//	call_api → { "method": "POST", "url": "...", "headers": {}, "body": {} }
//	branch   → { "cases": [...] }
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

	// branch
	// Cases dievaluasi berurutan — cocok pertama menang (if / elseif / else).
	// Case tanpa "when" (atau when nil) = else, selalu cocok.
	//
	//   "when": { "left": "{{step.field}}", "op": "==", "right": "Pizza" }
	//   "goto": "target_step_id"   ← lompat ke step ini jika cocok
	//                                 kosong = lanjut sequential
	//
	// Op: ==  !=  >  <  >=  <=  contains  starts_with  ends_with
	Cases []BranchCase `json:"cases,omitempty"`
}

// BranchCase adalah satu cabang dalam step branch.
type BranchCase struct {
	When *Condition `json:"when,omitempty"` // nil = else
	Goto string     `json:"goto,omitempty"` // step ID tujuan; "" = sequential
}

// Condition adalah satu ekspresi perbandingan.
// Left dan Right mendukung template {{...}}.
type Condition struct {
	Left  string `json:"left"`
	Op    string `json:"op"`
	Right string `json:"right"`
}

// Step adalah satu unit kerja dalam workflow.
//
// Next (opsional) — paksa lompat ke step ID ini setelah eksekusi step ini,
// tanpa perlu branch. Berguna untuk skip block atau custom flow.
type Step struct {
	ID     string          `json:"id"`
	Name   string          `json:"name,omitempty"`
	Type   string          `json:"type"` // "log" | "webhook" | "call_api" | "branch"
	Value  *StepValue      `json:"value"`
	Output json.RawMessage `json:"output,omitempty"`
	Next   string          `json:"next,omitempty"` // opsional: paksa goto step ID ini

	// runtime — tidak diserialisasi
	parsedOutput interface{}
}

type WorkerMode string

const (
	ModeOnce WorkerMode = "once"
	ModeLoop WorkerMode = "loop"
)

type Worker struct {
	ID      string                       `json:"id"`
	Name    string                       `json:"name"`
	Mode    WorkerMode                   `json:"mode"`
	Vars    map[string]map[string]string `json:"vars,omitempty"`
	Steps   []Step                       `json:"steps"`
	Running bool                         `json:"running"`
	Created time.Time                    `json:"created"`
	Updated time.Time                    `json:"updated"`
}

// prepareSteps pre-parses Output JSON dan membangun stepID→index map.
// Dipanggil sekali saat worker start — tidak diulang setiap loop iterasi.
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

const (
	storeFile   = "workers.gob"
	flushWindow = 300 * time.Millisecond
)

type Store struct {
	mu    sync.RWMutex
	data  map[string]*Worker
	dirty atomic.Bool
	wake  chan struct{}
}

func openStore() (*Store, error) {
	s := &Store{
		data: make(map[string]*Worker),
		wake: make(chan struct{}, 1),
	}
	f, err := os.Open(storeFile)
	if err != nil {
		if os.IsNotExist(err) {
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
	go s.flusher()
	return s, nil
}

func (s *Store) flusher() {
	for range s.wake {
		time.Sleep(flushWindow)
		for {
			select {
			case <-s.wake:
			default:
				goto flush
			}
		}
	flush:
		if !s.dirty.Load() {
			continue
		}
		if err := s.flushNow(); err != nil {
			log.Printf("store: flush error: %v", err)
		} else {
			s.dirty.Store(false)
		}
	}
}

func (s *Store) markDirty() {
	s.dirty.Store(true)
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Store) flushNow() error {
	s.mu.RLock()
	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(s.data)
	s.mu.RUnlock()
	if err != nil {
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
	s.data[w.ID] = w
	s.mu.Unlock()
	s.markDirty()
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	delete(s.data, id)
	s.mu.Unlock()
	s.markDirty()
}

func (s *Store) Get(id string) (*Worker, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.data[id]
	return w, ok
}

func (s *Store) List() []*Worker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*Worker, 0, len(s.data))
	for _, w := range s.data {
		list = append(list, w)
	}
	return list
}

// ─────────────────────────────────────────────
//  pipelineCtx
// ─────────────────────────────────────────────

type pipelineCtx struct {
	Steps map[string]map[string]interface{}
}

func newPipelineCtx() pipelineCtx {
	return pipelineCtx{Steps: make(map[string]map[string]interface{})}
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

	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))

	if len(body) > 256 {
		log.Printf("[worker:%s] WEBHOOK %s %s | body: %s... (%d bytes)",
			entry.workerID, r.Method, r.URL.Path, body[:256], len(body))
	} else {
		log.Printf("[worker:%s] WEBHOOK %s %s | body: %s",
			entry.workerID, r.Method, r.URL.Path, body)
	}

	bodyMap := make(map[string]interface{})
	_ = json.Unmarshal(body, &bodyMap)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, `{"status":"received"}`)
	select {
	case entry.arrived <- bodyMap:
	default:
	}
}

// ─────────────────────────────────────────────
//  Runtime
// ─────────────────────────────────────────────

var loopDelay = 50 * time.Millisecond

type Runtime struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newRuntime() *Runtime {
	return &Runtime{cancels: make(map[string]context.CancelFunc)}
}

func (rt *Runtime) Start(w *Worker, store *Store, hooks *webhookRouter) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if _, running := rt.cancels[w.ID]; running {
		return
	}
	mode := w.Mode
	if mode == "" {
		mode = ModeOnce
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancels[w.ID] = cancel

	preparedSteps, stepIndex := prepareSteps(w.Steps)

	go func() {
		pctx := newPipelineCtx()
		timer := time.NewTimer(loopDelay)
		timer.Stop()

		for {
			runWorker(ctx, w, preparedSteps, stepIndex, hooks, &pctx)
			pctx.reset()

			select {
			case <-ctx.Done():
				timer.Stop()
				if wk, ok := store.Get(w.ID); ok {
					wk.Running = false
					wk.Updated = time.Now()
					store.Save(wk)
				}
				rt.mu.Lock()
				delete(rt.cancels, w.ID)
				rt.mu.Unlock()
				return
			default:
			}

			if mode == ModeLoop {
				timer.Reset(loopDelay)
				select {
				case <-ctx.Done():
					timer.Stop()
					if wk, ok := store.Get(w.ID); ok {
						wk.Running = false
						wk.Updated = time.Now()
						store.Save(wk)
					}
					rt.mu.Lock()
					delete(rt.cancels, w.ID)
					rt.mu.Unlock()
					return
				case <-timer.C:
				}
				continue
			}

			if wk, ok := store.Get(w.ID); ok {
				wk.Running = false
				wk.Updated = time.Now()
				store.Save(wk)
			}
			rt.mu.Lock()
			delete(rt.cancels, w.ID)
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

func (rt *Runtime) IsRunning(id string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	_, ok := rt.cancels[id]
	return ok
}

// ─────────────────────────────────────────────
//  runWorker — index-based traversal dengan goto support
//
//  Traversal pakai pointer index `i` (bukan range) agar bisa di-jump
//  oleh branch atau step.Next ke step ID manapun secara O(1).
//
//  Proteksi infinite loop: max jumpLimit jump per satu run.
//  Jika terlampaui → log error + abort run (tidak crash server).
// ─────────────────────────────────────────────

const jumpLimit = 1000

func runWorker(
	ctx context.Context,
	w *Worker,
	steps []Step,
	stepIndex map[string]int,
	hooks *webhookRouter,
	pctx *pipelineCtx,
) {
	vars := w.Vars
	if vars == nil {
		vars = map[string]map[string]string{}
	}

	jumps := 0
	i := 0

	for i < len(steps) {
		select {
		case <-ctx.Done():
			log.Printf("[worker:%s] stopped", w.ID)
			return
		default:
		}

		step := steps[i]

		if step.Value == nil {
			i++
			continue
		}

		nextIdx := i + 1 // default: sequential
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
			log.Printf("[worker:%s][step:%s] LOG → %s", w.ID, step.stepLabel(), msg)
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

		case "branch":
			gotoID := execBranch(w.ID, step, *pctx, vars)
			if gotoID != "" {
				if target, ok := stepIndex[gotoID]; ok {
					jumps++
					if jumps > jumpLimit {
						log.Printf("[worker:%s][step:%s] BRANCH jump limit (%d) exceeded — aborting run",
							w.ID, step.stepLabel(), jumpLimit)
						return
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

		default:
			log.Printf("[worker:%s][step:%s] unknown type: %q", w.ID, step.stepLabel(), step.Type)
		}

		// step.Next override (hanya aktif jika branch belum jump)
		if !jumped && step.Next != "" {
			if target, ok := stepIndex[step.Next]; ok {
				jumps++
				if jumps > jumpLimit {
					log.Printf("[worker:%s][step:%s] NEXT jump limit (%d) exceeded — aborting run",
						w.ID, step.stepLabel(), jumpLimit)
					return
				}
				nextIdx = target
				log.Printf("[worker:%s][step:%s] NEXT → goto %q (idx %d)",
					w.ID, step.stepLabel(), step.Next, target)
			}
		}

		i = nextIdx
	}

	log.Printf("[worker:%s] workflow completed", w.ID)
}

// ─────────────────────────────────────────────
//  Branch executor
//
//  Evaluasi cases berurutan — cocok pertama menang.
//  Return: step ID tujuan dari case yang cocok, atau "" jika tidak ada.
// ─────────────────────────────────────────────

func execBranch(workerID string, step Step, pctx pipelineCtx, vars map[string]map[string]string) string {
	if step.Value == nil || len(step.Value.Cases) == 0 {
		return ""
	}
	for i, c := range step.Value.Cases {
		if c.When == nil {
			// else — selalu cocok
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

// evalCondition mengevaluasi satu Condition setelah render template.
// Jika kedua sisi bisa di-parse float64 → numeric comparison.
// Fallback ke string comparison.
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

var apiClient = &http.Client{Timeout: 30 * time.Second}

func execCallAPI(ctx context.Context, workerID string, step Step, pctx pipelineCtx, vars map[string]map[string]string) map[string]interface{} {
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
		return map[string]interface{}{}
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
		return map[string]interface{}{}
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

	out := make(map[string]interface{})
	if err := json.Unmarshal(respRaw, &out); err != nil {
		out["raw"] = string(respRaw)
		out["status"] = resp.StatusCode
	}
	return out
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
	dotIdx := strings.Index(inner, ".")
	if dotIdx <= 0 {
		return "", false
	}
	ns := inner[:dotIdx]
	key := inner[dotIdx+1:]

	if bucket, ok := vars[ns]; ok {
		return bucket[key], true
	}
	if stepData, ok := pctx.Steps[ns]; ok {
		return valueToString(resolveKey(key, stepData)), true
	}
	return "", false
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
//  HTTP handlers
// ─────────────────────────────────────────────

type App struct {
	store   *Store
	runtime *Runtime
	hooks   *webhookRouter
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
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

// POST /create
func (a *App) handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var worker Worker
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&worker); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
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
		a.runtime.Start(&worker, a.store, a.hooks)
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

// PUT /update
func (a *App) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		errJSON(w, http.StatusMethodNotAllowed, "PUT only")
		return
	}
	var updated Worker
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&updated); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	existing, ok := a.store.Get(updated.ID)
	if !ok {
		errJSON(w, http.StatusNotFound, "worker not found")
		return
	}
	updated.Created = existing.Created
	updated.Updated = time.Now()

	wasRunning := a.runtime.IsRunning(updated.ID)
	if wasRunning && !reflect.DeepEqual(existing.Steps, updated.Steps) {
		a.runtime.Stop(updated.ID)
		wasRunning = false
	}
	assignStepIDs(updated.Steps)
	a.store.Save(&updated)
	if updated.Running && !wasRunning {
		a.runtime.Start(&updated, a.store, a.hooks)
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
	worker.Running = true
	worker.Updated = time.Now()
	a.store.Save(worker)
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
	a.store.Save(worker)
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// ─────────────────────────────────────────────
//  Entry point
// ─────────────────────────────────────────────

func main() {
	runtime.GOMAXPROCS(1)

	store, err := openStore()
	if err != nil {
		log.Fatalf("cannot open store: %v", err)
	}

	hooks := newWebhookRouter()
	app := &App{store: store, runtime: newRuntime(), hooks: hooks}

	mux := http.NewServeMux()
	mux.HandleFunc("/create", app.handleCreate)
	mux.HandleFunc("/list", app.handleList)
	mux.HandleFunc("/get", app.handleGet)
	mux.HandleFunc("/update", app.handleUpdate)
	mux.HandleFunc("/delete", app.handleDelete)
	mux.HandleFunc("/run", app.handleRun)
	mux.HandleFunc("/stop", app.handleStop)
	mux.Handle("/", hooks)

	for _, w := range store.List() {
		if w.Running {
			log.Printf("auto-starting worker %s (%s)", w.ID, w.Name)
			app.runtime.Start(w, store, hooks)
		}
	}

	addr := ":8080"
	log.Printf("worker-engine listening on %s", addr)

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
