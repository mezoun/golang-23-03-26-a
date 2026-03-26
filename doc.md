# Worker Engine — Dokumentasi Teknis Lengkap

> Single-file Go service untuk membuat dan menjalankan **workflow workers** berbasis HTTP.  
> Zero external dependency — murni Go standard library.

---

## Daftar Isi

1. [Overview](#1-overview)
2. [Arsitektur Sistem](#2-arsitektur-sistem)
3. [Domain Model](#3-domain-model)
4. [Template Engine](#4-template-engine)
5. [Pipeline — Data Antar Step](#5-pipeline--data-antar-step)
6. [Storage — Gob + Atomic Write](#6-storage--gob--atomic-write)
7. [Webhook Router](#7-webhook-router)
8. [Runtime — Goroutine per Worker](#8-runtime--goroutine-per-worker)
9. [Batas Sistem & Multi-Tenant Safety](#9-batas-sistem--multi-tenant-safety)
10. [Teknik Efisiensi](#10-teknik-efisiensi)
11. [API Reference](#11-api-reference)
12. [Workflow JSON — Semua Variasi](#12-workflow-json--semua-variasi)
13. [Lifecycle Worker](#13-lifecycle-worker)
14. [Concurrency & Thread Safety](#14-concurrency--thread-safety)
15. [Graceful Shutdown](#15-graceful-shutdown)
16. [Build & Run](#16-build--run)
17. [Keterbatasan & Catatan](#17-keterbatasan--catatan)

---

## 1. Overview

Worker Engine adalah HTTP server yang memungkinkan pendefinisian **workflow** dalam bentuk JSON dan menjalankannya sebagai worker independen. Setiap worker berjalan di goroutine tersendiri, terisolasi penuh — panic di satu worker tidak mempengaruhi worker lain.

**Fitur utama:**

| Fitur | Keterangan |
|-------|------------|
| Webhook | Terima HTTP request (GET/POST); body, query params, dan headers masuk pipeline |
| Log | Cetak ke stdout, mendukung template `{{stepId.field}}` |
| Call API | Panggil URL eksternal, response masuk pipeline; mendukung retry + backoff |
| Branch | Routing kondisional — evaluasi berurutan, cocok pertama menang |
| Sleep | Delay eksplisit hingga 60 detik, cancellable via ctx |
| Set Var | Simpan nilai computed ke pipeline tanpa call API |
| Pipeline | Output setiap step tersimpan by ID, dapat dirujuk step berikutnya |
| Vars | Konfigurasi statik multi-bucket, antar-bucket bisa saling merujuk |
| Worker Mode | `once` — jalan 1x; `loop` — restart otomatis dengan interval konfigurasi |
| Persistent | Worker tersimpan di gob, survive server restart |
| Status API | `GET /status` — info runtime, run count, last error, tanpa baca log |
| Isolasi | Panic per-worker di-recover, error tersimpan, slot dibebaskan |

**Stack:**
```
Language   : Go 1.18+ (standard library only)
Storage    : encoding/gob + atomic write (CreateTemp → Rename)
HTTP       : net/http (ServeMux + custom webhookRouter)
Concurrency: goroutine + context.WithCancel + sync.RWMutex + recover()
ID Worker  : UUID v4 via crypto/rand
ID Step    : 8-char hex via crypto/rand
Template   : custom renderer — zero dependency
Hashing    : hash/fnv (FNV-64a) untuk step comparison
```

---

## 2. Arsitektur Sistem

```
┌──────────────────────────────────────────────────────────────┐
│                     HTTP Server :8080                        │
│  ServeMux                                                    │
│  ├── /create /list /get /status /update /delete /run /stop   │
│  └── /  ──────────────────────► webhookRouter                │
│                                   map[path → hookEntry]      │
│                                   HTTP 429 saat worker busy  │
├──────────────────────────────────────────────────────────────┤
│  Runtime  (goroutine per worker)                             │
│                                                              │
│  Per goroutine: defer recover() → isolasi panic              │
│  Global semaphore: max 20 concurrent call_api                │
│                                                              │
│  worker-A:                                                   │
│    webhook(id=hook) → pctx.Steps["hook"] = {body+_query+_headers}│
│    branch          → goto step berdasarkan kondisi           │
│    call_api(id=ai) → pctx.Steps["ai"] = response            │
│    sleep           → delay 500ms, cancellable                │
│    set_var(id=prep)→ pctx.Steps["prep"] = {key: value}       │
│    log             → renderTemplate("{{ai.result.answer}}")  │
├──────────────────────────────────────────────────────────────┤
│  Store                                                       │
│  map[id → *Worker]  in-memory  sync.RWMutex                  │
│  flush: snapshot copy → release lock → gob encode → Rename  │
└──────────────────────────────────────────────────────────────┘
```

---

## 3. Domain Model

### Worker

```go
type Worker struct {
    ID             string                       // UUID v4
    Name           string                       // nama deskriptif
    Mode           WorkerMode                   // "once" | "loop"
    Vars           map[string]map[string]string // konfigurasi statik multi-bucket
    Steps          []Step                       // urutan langkah
    Running        bool                         // status runtime
    LoopIntervalMs int                          // interval loop (0 = default 500ms, min 200ms)
    Created        time.Time
    Updated        time.Time
}
```

### WorkerMode

| Mode | Setelah complete | Stop |
|------|-----------------|------|
| `"once"` (default) | `running → false` | Otomatis / `/stop` |
| `"loop"` | Restart otomatis dengan `loop_interval_ms` | Hanya via `/stop` |

### Step

```go
type Step struct {
    ID     string          // 8-char hex, auto-generate jika kosong
    Name   string          // opsional, untuk log readability
    Type   string          // "webhook"|"log"|"call_api"|"branch"|"sleep"|"set_var"
    Value  *StepValue      // config sesuai type
    Output json.RawMessage // mapping output ke field baru (opsional)
    Next   string          // paksa lompat ke step ID ini setelah eksekusi (opsional)
}
```

### StepValue

```go
type StepValue struct {
    // log
    Message string

    // webhook & call_api
    Method HTTPMethod  // "GET" | "POST"

    // webhook
    Path string        // suffix: actual path = /<workerID><path>

    // call_api
    URL     string
    Headers map[string]string
    Body    json.RawMessage
    Retry   *RetryConfig

    // branch
    Cases []BranchCase

    // sleep
    SleepMs      int  // delay dalam milidetik
    SleepSeconds int  // delay dalam detik (dikonversi ke ms)

    // set_var
    SetKey   string  // nama field di pipeline
    SetValue string  // nilai, mendukung template {{...}}
}

type RetryConfig struct {
    Max     int  // max retry (hard limit: 5)
    DelayMs int  // delay awal antar retry
    Backoff bool // exponential backoff
}
```

### WorkerStatus (in-memory)

```go
type WorkerStatus struct {
    ID        string
    Running   bool
    LastRunAt time.Time
    LastRunOk bool
    LastError string    // kosong jika ok
    RunCount  int64     // total run sejak server start
}
```

Reset saat server restart — tidak disimpan ke gob.

### pipelineCtx

```go
type pipelineCtx struct {
    Steps map[string]map[string]interface{} // stepID → output data
}
```

Goroutine-local. Di-reset setiap iterasi loop. Tidak ada data leaks antar run.

---

## 4. Template Engine

### Namespace

| Syntax | Sumber | Kapan diisi |
|--------|--------|-------------|
| `{{bucket.key}}` | `vars["bucket"]["key"]` | Saat worker dibuat (statik) |
| `{{stepId.field}}` | `pctx.Steps[stepId]["field"]` | Runtime, setelah step selesai |
| `{{stepId.a.b.c}}` | Nested dot-notation | Runtime |
| `{{json:stepId.field}}` | Nilai di-escape untuk JSON | Runtime |

### Prefix `json:` — JSON-safe Injection

Gunakan saat menginject nilai ke dalam string di dalam JSON body:

```json
"body": { "message": "{{json:hook.user_input}}" }
```

Jika `hook.user_input` = `He said "hello"`, tanpa `json:` akan menghasilkan JSON malformed.  
Dengan `json:`, nilai di-escape otomatis: `He said \"hello\"`.

Prefix ini hanya escape untuk context string JSON — bukan encode JSON penuh.

### Resolusi Vars (antar-bucket)

Vars diresolution secara iteratif maksimal 20 pass, memungkinkan chaining antar bucket:

```json
"vars": {
  "pvt": { "cf_id": "abc123" },
  "glb": {
    "base_url": "https://api.example.com/{{pvt.cf_id}}",
    "full_url":  "{{glb.base_url}}/ai/run"
  }
}
```

`{{glb.full_url}}` → `https://api.example.com/abc123/ai/run`

Circular reference aman — iterasi dibatasi 20 pass.

### Dimana Template Bisa Dipakai

| Step | Field yang di-render |
|------|----------------------|
| `log` | `message` |
| `call_api` | `url`, semua values di `headers`, seluruh `body` (raw JSON string) |
| `branch` | `when.left`, `when.right` |
| `set_var` | `set_value` |
| `vars` values | boleh embed `{{bucket.key}}` dari bucket lain |

---

## 5. Pipeline — Data Antar Step

### Alur Data

```
Step: webhook (id="hook")
  ← menerima POST body: {"type":"judul","data":"Rendang"}
  → pctx.Steps["hook"] = {
      "type": "judul",
      "data": "Rendang",
      "_query": {"lang": "id"},
      "_headers": {"Content-Type": "application/json"}
    }

Step: set_var (id="prompt")
  → set_value: "Buat resep untuk: {{hook.data}}"
  → pctx.Steps["prompt"] = {"full_text": "Buat resep untuk: Rendang"}

Step: call_api (id="ai")
  → url: "{{glb.api_url}}"
  → body rendered: {"messages":[{"content":"{{prompt.full_text}}"}]}
  ← response: {"result":{"response":"...resep..."},"success":true}
  → pctx.Steps["ai"] = {"result":{"response":"..."},"success":true}

Step: log
  → "AI jawab: {{ai.result.response}}" → "AI jawab: ...resep..."
```

### Output Mapping (`output` field)

Step dapat memetakan output ke field baru:

```json
{
  "id": "agent",
  "type": "call_api",
  "value": { ... },
  "output": { "raw_text": "{{agent.result.response}}" }
}
```

Setelah `call_api` selesai dan output disimpan, `output` mapping di-render dan **override** entry pipeline untuk step ini. Akibatnya `{{agent.raw_text}}` tersedia untuk step berikutnya, sedangkan `{{agent.result.response}}` tetap tersedia.

**Aturan penting:** field `output` boleh self-reference (`{{agent.result.response}}` di step `agent`) karena raw response masuk ke pipeline lebih dulu. Yang tidak boleh adalah referensi ke step yang belum dieksekusi.

### Output `call_api`

Response JSON di-parse otomatis:
```
Response: {"result":{"response":"Pizza!"},"success":true}

{{ai.result.response}}  →  "Pizza!"
{{ai.success}}          →  "true"
{{ai._status}}          →  HTTP status code (selalu ada)
```

Jika response **bukan JSON**, fallback:
```
{{stepId.raw}}    →  raw string response body
{{stepId.status}} →  HTTP status code
```

### Webhook Auto Fields

```
POST /workerID/hook?lang=id
Header: X-Request-Id: abc123
Body: {"type":"judul","data":"Rendang"}

pctx.Steps["hook"] = {
  "type": "judul",
  "data": "Rendang",
  "_query": {"lang": "id"},
  "_headers": {"X-Request-Id": "abc123", "Content-Type": "application/json"}
}
```

Header sensitif (`Authorization`, `Cookie`, `Set-Cookie`) otomatis difilter.

---

## 6. Storage — Gob + Atomic Write

### Format Gob

Binary format — lebih kecil dan lebih cepat dari JSON.  
`json.RawMessage` di `StepValue.Body` disimpan sebagai `[]byte`.

**gob.Register** dipanggil di `init()` untuk semua concrete types yang dipakai di `interface{}`:

```go
func init() {
    gob.Register(map[string]interface{}{})
    gob.Register([]interface{}{})
    gob.Register(map[string]string{})
    gob.Register(float64(0))
    gob.Register(bool(false))
    gob.Register("")
}
```

Tanpa ini, decode setelah restart gagal untuk nested map/slice — data loss silent.

### Atomic Write

```
snapshot copy (di bawah RLock) → release RLock
→ gob.Encode (tanpa lock)
→ os.CreateTemp
→ Write
→ fsync
→ os.Rename  ← atomic di semua OS
```

Lock tidak ditahan selama encoding. Encoding bisa lambat (>100ms untuk 500 workers) — dengan pola ini tidak ada contention saat banyak `Save()` bersamaan.

### Debounced Flush

```
Save() → markDirty() → wake channel
flusher: sleep 300ms → drain wake → flushNow()
```

Multiple `Save()` dalam 300ms window digabung jadi satu write ke disk.

### Graceful Final Flush

Saat SIGTERM/Ctrl+C: `store.shutdown()` menutup `done` channel → flusher melakukan flush terakhir sebelum exit.

### Schema Migration

Saat startup, worker dengan step `Value == nil` (format lama) otomatis di-drop dengan log pesan. Server tidak crash.

---

## 7. Webhook Router

### Arsitektur

`http.ServeMux` tidak mendukung runtime path registration/deregistration (panic pada duplikat). Solusi: satu catch-all `"/"` di ServeMux yang delegate ke `webhookRouter` — map yang bisa diubah bebas di runtime.

```go
type hookEntry struct {
    method   string
    workerID string
    arrived  chan map[string]interface{}  // buffered size 1
}
```

### Path Scoping

Path webhook selalu diprefix dengan worker ID:

```
step value path: "/ask"
actual path:     "/a3f2c1d4-.../ask"
```

Collision-free by design — dua worker bisa punya path yang sama.

### Request Flow

```
webhookRouter.ServeHTTP()
    ↓ json.Unmarshal(body) → map
    ↓ tambah _query, _headers
    ↓
select {
  case entry.arrived <- bodyMap:
    → 200 {"status":"received"}
  default:
    → 429 {"error":"worker busy, retry later"}
}
```

### HTTP 429 — Worker Busy

Jika worker masih memproses request sebelumnya (channel penuh), request baru mendapat HTTP 429. Tidak ada request yang hilang diam-diam.

---

## 8. Runtime — Goroutine per Worker

### Isolasi via `recover()`

Setiap goroutine worker dimulai dengan:

```go
defer func() {
    if r := recover(); r != nil {
        stack := debug.Stack()
        errMsg := fmt.Sprintf("PANIC: %v", r)
        log.Printf("[worker:%s] %s\n%s", workerID, errMsg, stack)
        // update status: running=false, last_error=errMsg
        // update store: running=false
        // bebaskan slot dari cancels map
    }
}()
```

Worker yang panic → berhenti bersih, error tersimpan di `/status`, slot dibebaskan. Worker lain tidak terganggu.

### Live Vars Refresh

```go
for {
    latestW, ok := store.Get(workerID)  // copy terbaru dari store
    if !ok { break }
    runWorker(ctx, latestW, preparedSteps, stepIndex, hooks, &pctx)
    // preparedSteps tetap immutable (sudah di-parse sekali)
    // hanya Vars yang di-refresh dari store
}
```

Update vars via `/update` langsung efektif di iterasi berikutnya tanpa restart worker.

### Loop Interval

```go
func (w *Worker) loopDelay() time.Duration {
    ms := w.LoopIntervalMs
    if ms < minLoopIntervalMs {
        ms = defaultLoopIntervalMs  // 500ms
    }
    return time.Duration(ms) * time.Millisecond
}
```

Minimum 200ms — tidak bisa lebih cepat dari ini. Default 500ms.

### Staggered Boot

Saat server start dan ada worker dengan `running: true`:

```go
for i, w := range runningWorkers {
    delay := (10 + rand.Intn(40)) * ms * (i+1)
    time.AfterFunc(delay, func() { runtime.Start(w, ...) })
}
```

Worker ke-N ditunda `N × (10–50ms)` — mencegah thundering herd ke API eksternal di detik pertama boot.

### Self-Cleanup

Saat worker selesai (mode once atau di-stop):
1. `running = false` di store
2. `updated = now` di store
3. Entry di `rt.cancels` dihapus
4. Entry di `rt.statuses` di-update

`/list` dan `/status` selalu mencerminkan state real.

---

## 9. Batas Sistem & Multi-Tenant Safety

### Konstanta

```go
const (
    maxConcurrentWorkers = 50   // max worker aktif bersamaan
    maxStoredWorkers     = 500  // max worker tersimpan
    maxStepsPerWorker    = 100  // max steps per worker
    maxConcurrentCalls   = 20   // max concurrent call_api
    maxVarsBuckets       = 20   // max bucket di vars
    maxVarsPerBucket     = 50   // max key per bucket
    maxBodyBytes         = 128 << 10  // 128 KB
    maxWebhookBytes      = 64 << 10   // 64 KB
    maxRetryCount        = 5    // max retry call_api
    maxSleepMs           = 60_000     // 60 detik
    minLoopIntervalMs    = 200
    defaultLoopIntervalMs = 500
)
```

Semua bisa diubah di atas `main()` tanpa menyentuh logic.

### Global Semaphore `call_api`

```go
var callAPISem = make(chan struct{}, maxConcurrentCalls)
```

Setiap `call_api` acquire slot sebelum HTTP call, release setelah selesai. Jika penuh, step menunggu (tidak skip, tidak error) dengan respect ke `ctx.Done()`. Mencegah 50 workers × N steps = ratusan koneksi simultan.

### HTTP Client Pool

```go
var apiClient = &http.Client{
    Timeout: 30 * time.Second,
    Transport: &http.Transport{
        MaxIdleConns:        50,
        MaxIdleConnsPerHost: 5,
        IdleConnTimeout:     30 * time.Second,
        DialContext: (&net.Dialer{
            Timeout:   10 * time.Second,
            KeepAlive: 30 * time.Second,
        }).DialContext,
    },
}
```

Satu pool global — semua `call_api` dari semua worker berbagi pool ini. Mencegah file descriptor exhaustion.

### Validasi Saat Create/Update

| Validasi | Error |
|----------|-------|
| `len(steps) > 100` | HTTP 400 |
| Duplikat step ID | HTTP 400 |
| `len(vars) > 20` | HTTP 400 |
| `len(bucket) > 50` | HTTP 400 |
| `store.Count() >= 500` | HTTP 503 |
| `runtime.ActiveCount() >= 50` | HTTP 503 (untuk /run) |
| Body > 128KB | truncated (LimitReader) |

### Content-Type Validation

`POST /create` dan `PUT /update` mensyaratkan `Content-Type: application/json`. Request tanpa header ini mendapat HTTP 415.

---

## 10. Teknik Efisiensi

| Teknik | Detail |
|--------|--------|
| **Gob binary** | Lebih kecil & cepat dari JSON untuk storage |
| **In-memory map** | Semua read O(1), zero disk I/O saat runtime |
| **Atomic rename** | Crash-safe write di semua OS |
| **Snapshot before encode** | Lock dilepas sebelum gob encode — no contention |
| **atomic.AddInt64 count** | Count() adalah O(1), tidak perlu lock |
| **FNV-64 hash** | Deteksi perubahan steps lebih cepat dari reflect.DeepEqual |
| **sync.RWMutex** | Banyak reader paralel, write eksklusif |
| **Buffered channel size 1** | `chan map[string]interface{}` — signal + data sekaligus |
| **context.WithCancel** | Stop propagates ke HTTP in-flight request |
| **webhookRouter O(1)** | Map lookup vs prefix-matching ServeMux |
| **Template fast path** | Jika tidak ada `{{` → return langsung |
| **pipelineCtx pre-alloc** | `make(map, stepCount)` — kurangi rehash |
| **pipelineCtx reset** | In-place clear, tidak alokasi map baru tiap loop |
| **Semaphore call_api** | Batasi koneksi simultan tanpa mutex overhead |
| **recover() per goroutine** | Isolasi panic, tidak ada goroutine leak |
| **Live vars refresh** | Vars di-copy dari store tiap iterasi — immutable selama run |
| **Staggered start** | Spread load saat boot — tidak spike serentak |
| **Loop interval 500ms default** | vs 50ms lama — 10x lebih hemat scheduler wakeup |
| **Zero external dep** | stdlib only — no CVE, no version conflict |

---

## 11. API Reference

### Endpoints

| Method | Endpoint | Deskripsi |
|--------|----------|-----------|
| POST | `/create` | Buat worker baru |
| GET | `/list` | List semua worker |
| GET | `/get?id=<id>` | Get satu worker |
| GET | `/status?id=<id>` | Runtime status worker |
| PUT | `/update` | Update worker (full replace) |
| DELETE | `/delete?id=<id>` | Hapus worker |
| POST | `/run?id=<id>` | Jalankan worker |
| POST | `/stop?id=<id>` | Hentikan worker |
| ANY | `/<workerID><path>` | Trigger webhook step |

### Worker Object

```jsonc
{
  "id":               "a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d",
  "name":             "AI Worker",
  "mode":             "loop",
  "loop_interval_ms": 1000,
  "vars": {
    "glb": { "api_url": "https://..." },
    "pvt": { "token": "..." }
  },
  "steps": [...],
  "running": true,
  "created": "2026-03-26T03:47:00Z",
  "updated": "2026-03-26T03:47:00Z"
}
```

### POST /create — Field

| Field | Tipe | Wajib | Keterangan |
|-------|------|-------|------------|
| `id` | string | tidak | UUID custom; auto-generate jika kosong |
| `name` | string | ya | Nama deskriptif |
| `mode` | string | tidak | `"once"` (default) atau `"loop"` |
| `loop_interval_ms` | int | tidak | Interval loop (min 200ms, default 500ms) |
| `vars` | object | tidak | Bucket variabel statik |
| `running` | bool | tidak | Jalankan langsung setelah dibuat |
| `steps` | array | ya | Daftar langkah (max 100) |

### GET /status?id= — Response

```jsonc
{
  "id":          "a3f2c1d4-...",
  "running":     true,
  "last_run_at": "2026-03-26T10:00:00Z",
  "last_run_ok": true,
  "last_error":  "",
  "run_count":   42
}
```

Data in-memory — reset saat server restart.

### Error Responses

| HTTP | Kondisi |
|------|---------|
| 400 | JSON invalid, duplicate step ID, validasi gagal |
| 404 | Worker tidak ditemukan |
| 405 | Method salah |
| 415 | Content-Type bukan application/json |
| 429 | Webhook: worker busy |
| 503 | Limit concurrent atau stored tercapai |

---

## 12. Workflow JSON — Semua Variasi

### Webhook step

```json
{ "id": "hook", "name": "terima", "type": "webhook", "value": { "method": "POST", "path": "/trigger" } }
```

### Webhook dengan akses query & header

```bash
# Request: POST /workerID/trigger?source=mobile
# Header: X-User-Id: u123
# Body: {"action":"buy","item":"pizza"}
```

```json
"{{hook.action}}"           →  "buy"
"{{hook._query.source}}"    →  "mobile"
"{{hook._headers.X-User-Id}}" →  "u123"
```

### Log step

```json
{ "type": "log", "value": { "message": "User: {{hook.user}} | Score: {{score.result.value}}" } }
```

### Call API — GET

```json
{
  "id": "ip",
  "type": "call_api",
  "value": { "method": "GET", "url": "https://api.ipify.org/?format=json" }
}
```

Akses: `{{ip.ip}}`

### Call API — POST dengan retry

```json
{
  "id": "ai",
  "type": "call_api",
  "value": {
    "method": "POST",
    "url": "{{glb.api_url}}",
    "headers": { "Authorization": "Bearer {{pvt.token}}" },
    "body": {
      "messages": [
        { "role": "system", "content": "{{hook.sys}}" },
        { "role": "user",   "content": "{{json:hook.usr}}" }
      ]
    },
    "retry": { "max": 3, "delay_ms": 1000, "backoff": true }
  }
}
```

Delay sequence dengan backoff: 1000ms, 2000ms, 4000ms.

### Branch

```json
{
  "id": "check",
  "type": "branch",
  "value": {
    "cases": [
      { "when": { "left": "{{hook.type}}", "op": "==",       "right": "admin"    }, "goto": "admin_step"   },
      { "when": { "left": "{{score.val}}", "op": ">",        "right": "90"       }, "goto": "high_score"   },
      { "when": { "left": "{{hook.name}}", "op": "starts_with", "right": "Dr."   }, "goto": "doctor_flow"  },
      { "goto": "default_step" }
    ]
  }
}
```

### Sleep

```json
{ "type": "sleep", "value": { "sleep_ms": 500 } }
{ "type": "sleep", "value": { "sleep_seconds": 2 } }
```

### Set Var

```json
{
  "id": "prep",
  "type": "set_var",
  "value": {
    "set_key":   "full_prompt",
    "set_value": "Context: {{hook.ctx}} | Q: {{hook.question}}"
  }
}
```

Akses selanjutnya: `{{prep.full_prompt}}`

### Output Mapping

```json
{
  "id": "agent",
  "type": "call_api",
  "value": { "method": "POST", "url": "...", "body": { ... } },
  "output": { "answer": "{{agent.result.response}}" }
}
```

`{{agent.answer}}` tersedia untuk step berikutnya.

### Next — Force Jump

```json
{ "id": "step_a", "type": "call_api", "value": { ... }, "next": "merge_step" }
```

Setelah `step_a` selesai, lompat langsung ke `merge_step` — mengabaikan step di antaranya.

### Vars dengan cross-reference

```json
"vars": {
  "pvt": {
    "cf_id":   "56667d302...",
    "cf_auth": "cfut_Kpdk..."
  },
  "glb": {
    "ai_model": "@cf/meta/llama-3.1-8b-instruct-fast",
    "api_url":  "https://api.cloudflare.com/client/v4/accounts/{{pvt.cf_id}}/ai/run/{{glb.ai_model}}"
  }
}
```

---

## 13. Lifecycle Worker

```
POST /create
      │
      ▼
  [ STORED ]  running: false
      │
      │ running:true / POST /run
      │ (cek: activeCount < 50)
      ▼
  [ RUNNING ]  goroutine aktif
      │
      ├── POST /stop ─────────────► running: false, goroutine exit
      │
      ├── mode=once, complete ────► running: false (auto)
      │                             POST /run untuk ulangi
      │
      ├── mode=loop, complete ────► restart dengan loop_interval_ms delay
      │                             running: tetap true
      │
      └── panic / error fatal ────► recover() tangkap
                                    running: false (auto)
                                    last_error tersimpan di /status
                                    slot dibebaskan
```

---

## 14. Concurrency & Thread Safety

| Komponen | Mekanisme | Alasan |
|----------|-----------|--------|
| `Store.data` read | `sync.RWMutex` RLock | Banyak goroutine baca paralel |
| `Store.data` write | `sync.RWMutex` Lock | Eksklusif |
| `Store.count` | `atomic.AddInt64` | O(1) tanpa lock |
| `Store.dirty` | `atomic.StoreInt32/LoadInt32` | Tanpa lock |
| `Runtime.cancels` | `sync.Mutex` | Map tidak concurrency-safe |
| `Runtime.statuses` | `sync.Mutex` | Sama dengan cancels |
| `webhookRouter.entries` read | `sync.RWMutex` RLock | Setiap HTTP request |
| `webhookRouter.entries` write | `sync.RWMutex` Lock | Register/unregister |
| Worker goroutine | `context.WithCancel` | Stop tanpa shared state |
| Webhook payload | `chan map[string]interface{}` buf 1 | Data + signal sekaligus |
| `pipelineCtx` | goroutine-local | Tidak di-share, tidak perlu lock |
| `call_api` concurrency | buffered channel semaphore | Max 20 simultan |
| Panic isolation | `defer recover()` | Per-goroutine, tidak propagate |

---

## 15. Graceful Shutdown

```go
sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
<-sigCtx.Done()

// 1. Stop semua worker (cancel semua context)
rt.StopAll()

// 2. Shutdown HTTP server (tunggu max 5 detik)
srv.Shutdown(shutdownCtx)

// 3. Trigger final flush store
store.shutdown()
time.Sleep(500ms)  // beri waktu flusher selesai
```

Urutan penting: worker stop dulu → server stop → store flush.  
Dengan urutan ini, tidak ada request yang diterima setelah worker berhenti.

---

## 16. Build & Run

```bash
# Jalankan langsung
go run main.go

# Build binary
go build -o worker-engine main.go

# Build kecil (strip debug symbols)
go build -ldflags="-s -w" -o worker-engine main.go
```

### Cross-compile

```bash
GOOS=windows GOARCH=amd64 go build -o worker-engine.exe main.go
GOOS=linux   GOARCH=amd64 go build -o worker-engine main.go
GOOS=darwin  GOARCH=arm64 go build -o worker-engine main.go
```

### File runtime

```
workers.gob         ← persistent store (auto-dibuat pada run pertama)
workers-*.gob.tmp   ← temporary saat write (langsung hilang)
```

Port default `:8080` — ubah konstanta `addr` di `main()`.

---

## 17. Keterbatasan & Catatan

**Webhook menunggu 1 request per step.** Mode `loop` menangani ini otomatis. Jika request datang saat worker masih memproses, server balas HTTP 429 — caller bisa retry.

**`call_api` body hanya JSON.** `form-urlencoded` dan `multipart` tidak didukung.

**Vars bersifat statik per-run.** Vars dibaca fresh dari store setiap iterasi loop, tapi tidak berubah di tengah eksekusi satu run. Untuk nilai yang berubah dinamis dalam satu run, gunakan output step (`{{stepId.field}}`) atau `set_var`.

**pipelineCtx tidak persisten.** Data pipeline hilang saat worker selesai atau restart. Hanya vars yang persisten.

**Status tidak persisten.** `run_count`, `last_error`, `last_run_at` di `/status` reset saat server restart.

**Storage flat file.** Untuk write frequency sangat tinggi, pertimbangkan SQLite atau BoltDB.

**Tidak ada autentikasi.** Tambahkan middleware atau reverse proxy sebelum expose ke internet.

**Satu proses.** Tidak dirancang untuk multi-instance atau distributed deployment.

**Jump limit 1000.** Satu run tidak bisa melakukan lebih dari 1000 lompatan (branch/next). Ini proteksi infinite loop — workflow normal tidak akan mendekati limit ini.