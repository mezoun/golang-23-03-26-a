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
	"sync"
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

type WebhookStep struct {
	Method HTTPMethod `json:"method"` // "GET" | "POST"
	Path   string     `json:"path"`   // e.g. "/hook/order"
}

type LogStep struct {
	Message string `json:"message"`
}

type CallAPIStep struct {
	Method  HTTPMethod             `json:"method"`
	URL     string                 `json:"url"`
	Headers map[string]string      `json:"headers,omitempty"`
	Body    map[string]interface{} `json:"body,omitempty"`
}

type Step struct {
	Type    string       `json:"type"` // "webhook" | "log" | "call_api"
	Webhook *WebhookStep `json:"webhook,omitempty"`
	Log     *LogStep     `json:"log,omitempty"`
	CallAPI *CallAPIStep `json:"call_api,omitempty"`
}

type Worker struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Steps   []Step    `json:"steps"`
	Running bool      `json:"running"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// ─────────────────────────────────────────────
//  Storage — gob with atomic write (cross-platform)
// ─────────────────────────────────────────────

const storeFile = "workers.gob"

type Store struct {
	mu   sync.RWMutex
	data map[string]*Worker
}

func openStore() (*Store, error) {
	s := &Store{data: make(map[string]*Worker)}
	f, err := os.Open(storeFile)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	defer f.Close()
	if err := gob.NewDecoder(f).Decode(&s.data); err != nil && err != io.EOF {
		log.Printf("warn: store decode failed (%v), starting fresh", err)
		s.data = make(map[string]*Worker)
	}
	return s, nil
}

func (s *Store) flush() error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s.data); err != nil {
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

func (s *Store) Save(w *Worker) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[w.ID] = w
	return s.flush()
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, id)
	return s.flush()
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
//  webhookRouter — dynamic swappable dispatch table
//
//  Root cause of the bug:
//    http.ServeMux.HandleFunc panics on duplicate pattern.
//    Worker restart / update would try to re-register the same path → panic or silent-drop.
//
//  Fix:
//    Register ONE catch-all "/" on ServeMux that delegates here.
//    This map is mutable at runtime: register/unregister freely.
// ─────────────────────────────────────────────

type hookEntry struct {
	method   string
	workerID string
	arrived  chan struct{}
}

type webhookRouter struct {
	mu      sync.RWMutex
	entries map[string]*hookEntry // path → entry
}

func newWebhookRouter() *webhookRouter {
	return &webhookRouter{entries: make(map[string]*hookEntry)}
}

func (wr *webhookRouter) register(path, method, workerID string, arrived chan struct{}) {
	wr.mu.Lock()
	defer wr.mu.Unlock()
	wr.entries[path] = &hookEntry{method: method, workerID: workerID, arrived: arrived}
	log.Printf("[worker:%s] WEBHOOK registered  %s  %s", workerID, method, path)
}

func (wr *webhookRouter) unregister(path string) {
	wr.mu.Lock()
	defer wr.mu.Unlock()
	delete(wr.entries, path)
}

// ServeHTTP is mounted as the fallback "/" handler on the main mux.
// API routes (/create, /list, …) are registered with explicit paths
// and take priority over "/" in ServeMux, so they never land here.
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
	body, _ := io.ReadAll(r.Body)
	log.Printf("[worker:%s] WEBHOOK %s %s | body: %s",
		entry.workerID, r.Method, r.URL.Path, string(body))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, `{"status":"received"}`)
	select {
	case entry.arrived <- struct{}{}:
	default:
	}
}

// ─────────────────────────────────────────────
//  Runtime — goroutine per worker
// ─────────────────────────────────────────────

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
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancels[w.ID] = cancel
	go runWorker(ctx, w, store, hooks)
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

func runWorker(ctx context.Context, w *Worker, _ *Store, hooks *webhookRouter) {
	log.Printf("[worker:%s] started — %s", w.ID, w.Name)
	for _, step := range w.Steps {
		select {
		case <-ctx.Done():
			log.Printf("[worker:%s] stopped", w.ID)
			return
		default:
		}
		switch step.Type {
		case "webhook":
			if step.Webhook != nil {
				execWebhook(ctx, w.ID, step.Webhook, hooks)
			}
		case "log":
			if step.Log != nil {
				log.Printf("[worker:%s] LOG → %s", w.ID, step.Log.Message)
			}
		case "call_api":
			if step.CallAPI != nil {
				execCallAPI(ctx, w.ID, step.CallAPI)
			}
		default:
			log.Printf("[worker:%s] unknown step type: %q", w.ID, step.Type)
		}
	}
	log.Printf("[worker:%s] workflow completed", w.ID)
}

// ─────────────────────────────────────────────
//  Step executors
// ─────────────────────────────────────────────

func execWebhook(ctx context.Context, workerID string, cfg *WebhookStep, hooks *webhookRouter) {
	// Full path: /<workerID><path>
	// e.g. workerID=a3f2c1d4, path=/hook/order -> /a3f2c1d4.../hook/order
	// Guarantees no clash between workers sharing the same path string.
	fullPath := "/" + workerID + cfg.Path

	arrived := make(chan struct{}, 1)
	hooks.register(fullPath, string(cfg.Method), workerID, arrived)
	defer hooks.unregister(fullPath)

	select {
	case <-arrived:
		log.Printf("[worker:%s] WEBHOOK triggered, continuing workflow", workerID)
	case <-ctx.Done():
		log.Printf("[worker:%s] WEBHOOK cancelled", workerID)
	}
}

func execCallAPI(ctx context.Context, workerID string, cfg *CallAPIStep) {
	var reqBody io.Reader
	if cfg.Method == MethodPOST && len(cfg.Body) > 0 {
		b, _ := json.Marshal(cfg.Body)
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, string(cfg.Method), cfg.URL, reqBody)
	if err != nil {
		log.Printf("[worker:%s] CALL_API build error: %v", workerID, err)
		return
	}
	if cfg.Method == MethodPOST {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[worker:%s] CALL_API error: %v", workerID, err)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("[worker:%s] CALL_API %s %s → HTTP %d | %s",
		workerID, cfg.Method, cfg.URL, resp.StatusCode, string(respBody))
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

// uniqueID returns a random UUID v4 (RFC 4122).
// Uses crypto/rand — collision probability is negligible (2^122 space).
func uniqueID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// POST /create
func (a *App) handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var worker Worker
	if err := json.NewDecoder(r.Body).Decode(&worker); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if worker.ID == "" {
		worker.ID = uniqueID()
	}
	worker.Created = time.Now()
	worker.Updated = time.Now()
	if err := a.store.Save(&worker); err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
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
	if err := a.store.Save(&updated); err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	if err := a.store.Delete(id); err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
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
		writeJSON(w, http.StatusOK, map[string]string{"status": "already running"})
		return
	}
	worker.Running = true
	worker.Updated = time.Now()
	_ = a.store.Save(worker)
	a.runtime.Start(worker, a.store, a.hooks)
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
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
	_ = a.store.Save(worker)
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// ─────────────────────────────────────────────
//  Entry point
// ─────────────────────────────────────────────

func main() {
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
	// Catch-all: semua path lain → webhookRouter
	mux.Handle("/", hooks)

	// auto-start workers that were running before server restart
	for _, w := range store.List() {
		if w.Running {
			log.Printf("auto-starting worker %s (%s)", w.ID, w.Name)
			app.runtime.Start(w, store, hooks)
		}
	}

	addr := ":8080"
	log.Printf("worker-engine listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
