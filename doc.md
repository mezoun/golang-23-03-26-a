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
12. [File Manager](#12-file-manager)
13. [LogManager — Per-Worker Log](#13-logmanager--per-worker-log)
14. [Workflow JSON — Semua Variasi](#14-workflow-json--semua-variasi)
15. [Lifecycle Worker](#15-lifecycle-worker)
16. [Concurrency & Thread Safety](#16-concurrency--thread-safety)
17. [Graceful Shutdown](#17-graceful-shutdown)
18. [Build & Run](#18-build--run)
19. [Keterbatasan & Catatan](#19-keterbatasan--catatan)

---

## 1. Overview

Worker Engine adalah HTTP server yang memungkinkan pendefinisian **workflow** dalam bentuk JSON dan menjalankannya sebagai worker independen. Setiap worker berjalan di goroutine tersendiri, terisolasi penuh — panic di satu worker tidak mempengaruhi worker lain.

**Fitur utama:**

| Fitur | Keterangan |
|-------|------------|
| Webhook | Terima HTTP request (GET/POST); body, query params, dan headers masuk pipeline |
| Log | Tulis ke file log per-worker (`log/{workerID}.log`), mendukung template `{{stepId.field}}` |
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
| File Manager | Upload, list, view, delete, dan auto-save file output agent via `/files/*` |
| LogManager | Buffer 32 KB per worker, flush background tiap 3 detik, file handle lazy-open |

**Stack:**
```
Language    : Go 1.18+ (standard library only)
Storage     : encoding/gob + atomic write (CreateTemp → Rename)
HTTP        : net/http (ServeMux + custom webhookRouter)
Concurrency : goroutine + context.WithCancel + sync.RWMutex + recover()
ID Worker   : UUID v4 via crypto/rand
ID Step     : 8-char hex via crypto/rand
Template    : custom renderer — zero dependency
Hashing     : hash/fnv (FNV-64a) untuk step comparison
Log         : bufio.Writer 32 KB per worker → log/{workerID}.log
File output : output/ folder, auto-generated filename untuk agent save
```

---

## 2. Arsitektur Sistem

```
┌──────────────────────────────────────────────────────────────┐
│                     HTTP Server :8080                        │
│  ServeMux                                                    │
│  ├── /create /list /get /status /update /delete /run /stop   │
│  ├── /files/upload /files/list /files/view                   │
│  ├── /files/delete /files/save  ──► File Manager (output/)  │
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
│    log             → tulis ke log/{workerID}.log             │
├──────────────────────────────────────────────────────────────┤
│  LogManager  (buffered per-worker log)                       │
│  handles map[workerID → *workerLog]                          │
│  bufio.Writer 32 KB — background flush tiap 3 detik          │
│  Lazy open: file handle dibuka saat pertama kali Writef()    │
│  Close: flush + tutup saat worker berhenti                   │
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
  → "AI jawab: {{ai.result.response}}" → tulis ke log/{workerID}.log
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

Setelah `call_api` selesai dan output disimpan, `output` mapping di-render dan **override** entry pipeline untuk step ini. Akibatnya `{{agent.raw_text}}` tersedia untuk step berikutnya.

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

Jika retry habis: output berisi `_error` (pesan error) dan `_attempts` (jumlah percobaan).

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
        globalLog.Writef(workerID, "%s\n%s", errMsg, stack)
        // update status: running=false, last_error=errMsg
        // update store: running=false
        // bebaskan slot dari cancels map
        globalLog.Close(workerID)
    }
}()
```

Worker yang panic → berhenti bersih, error tersimpan di `/status`, slot dibebaskan, log file di-flush dan ditutup. Worker lain tidak terganggu.

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
    delay := time.Duration(staggerMinMs+mrand.Intn(staggerMaxMs)) * time.Millisecond * time.Duration(i+1)
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
5. `globalLog.Close(workerID)` — flush dan tutup file handle log

`/list` dan `/status` selalu mencerminkan state real.

---

## 9. Batas Sistem & Multi-Tenant Safety

### Konstanta

```go
const (
    // Multi-tenant
    maxConcurrentWorkers = 50   // max worker aktif bersamaan
    maxStoredWorkers     = 500  // max worker tersimpan
    maxStepsPerWorker    = 100  // max steps per worker
    maxConcurrentCalls   = 20   // max concurrent call_api
    maxVarsBuckets       = 20   // max bucket di vars
    maxVarsPerBucket     = 50   // max key per bucket

    // Body size
    maxBodyBytes         = 128 << 10  // 128 KB — /create, /update
    maxWebhookBytes      = 64 << 10   // 64 KB — webhook body

    // Retry
    maxRetryCount        = 5

    // Sleep
    maxSleepMs           = 60_000     // 60 detik

    // Loop interval
    defaultLoopIntervalMs = 500
    minLoopIntervalMs     = 200

    // Jump limit per run (proteksi infinite loop)
    jumpLimit             = 1000

    // File manager
    maxUploadBytes        = 10 << 20  // 10 MB per file upload
    maxSaveBytes          = 5 << 20   // 5 MB per /files/save
    maxOutputFiles        = 200       // max file di folder output/
    maxListFiles          = 500       // batas iterasi ReadDir

    // LogManager
    logBufSize            = 32 << 10        // 32 KB buffer per worker
    logFlushInterval      = 3 * time.Second // interval flush background

    // Staggered start
    staggerMinMs          = 10
    staggerMaxMs          = 40
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
| **LogManager bufio** | 32 KB buffer per worker — satu flush per 3 detik vs satu write per log |
| **Lazy log open** | File handle log baru dibuka saat pertama kali dibutuhkan |
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
| POST | `/files/upload` | Upload file ke `output/` |
| GET | `/files/list` | List semua file di `output/` |
| GET | `/files/view?name=<f>` | Download / tampilkan file |
| DELETE | `/files/delete?name=<f>` | Hapus file dari `output/` |
| POST | `/files/save` | Simpan output agent dengan nama auto-generated |
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
| 404 | Worker / file tidak ditemukan |
| 405 | Method salah |
| 413 | File terlalu besar (upload > 10 MB, save > 5 MB) |
| 415 | Content-Type bukan application/json |
| 429 | Webhook: worker busy |
| 503 | Limit concurrent atau stored tercapai |
| 507 | Folder output penuh (> 200 file) |

---

## 12. File Manager

File Manager menyediakan pengelolaan file output berbasis HTTP. Semua file disimpan di folder `output/` yang dibuat otomatis saat pertama kali digunakan.

### Endpoints

#### `POST /files/upload`

Upload file via `multipart/form-data`. Field name wajib: `file`. Maksimum 10 MB per file.

- Melakukan path sanitization — mencegah directory traversal
- Cek kuota sebelum simpan (`maxOutputFiles = 200`)
- Response HTTP 201: `{"name":"...", "size_bytes":..., "path":"output/..."}`

#### `GET /files/list`

Kembalikan array JSON semua file di `output/`. Setiap entry:

```json
{ "name": "report.json", "size_bytes": 2048, "modified": "2026-03-26T14:30:00Z" }
```

Batas iterasi: `maxListFiles = 500`. Subdirektori diabaikan.

#### `GET /files/view?name=<filename>`

Mengirim file sebagai attachment (`Content-Disposition: attachment`). Cocok untuk download langsung:

```bash
curl -O -J "http://localhost:8080/files/view?name=report.json"
```

#### `DELETE /files/delete?name=<filename>`

Menghapus file dari `output/`. Method DELETE wajib.

```bash
curl -X DELETE "http://localhost:8080/files/delete?name=report.json"
```

Response: `{"deleted": "report.json"}`

#### `POST /files/save`

Endpoint khusus untuk dipanggil dari dalam pipeline worker via step `call_api`. Menerima JSON body:

```json
{ "tag": "recipe", "content": "{\"title\":\"Rendang\",...}" }
```

| Field | Tipe | Keterangan |
|-------|------|-----------|
| `tag` | string | Opsional. Label yang muncul di nama file. Hanya huruf, angka, dash. Maks 32 karakter. |
| `content` | string | Wajib. Isi file yang akan disimpan. Maks 5 MB. |

**Naming convention file:**

```
tag tidak kosong : agent-{tag}_{YYYYMMDD-HHMMSS}_{rand8hex}.json
tag kosong       : agent_{YYYYMMDD-HHMMSS}_{rand8hex}.json
```

Contoh: `agent-recipe_20260326-143022_a1b2c3d4.json`

Response HTTP 201:

```json
{
  "name":       "agent-recipe_20260326-143022_a1b2c3d4.json",
  "size_bytes": 3821,
  "view_url":   "/files/view?name=agent-recipe_20260326-143022_a1b2c3d4.json"
}
```

### Penggunaan dalam Pipeline

Dari dalam step `call_api` di pipeline worker:

```json
{
  "id":   "save_file",
  "type": "call_api",
  "value": {
    "method":  "POST",
    "url":     "http://localhost:8080/files/save",
    "headers": { "Content-Type": "application/json" },
    "body": {
      "tag":     "result",
      "content": "{{json:prev_step.result.response}}"
    },
    "retry": { "max": 2, "delay_ms": 500, "backoff": false }
  }
}
```

Akses `view_url` di step berikutnya: `{{save_file.result.response}}`  
(karena `/files/save` me-return JSON, field `view_url` tersedia via pipeline)

### Keamanan Path

Semua filename di-sanitize via `filepath.Base(filepath.Clean(name))`. Nama dengan karakter `/`, `\`, atau nama `.` / `..` ditolak dengan HTTP 400.

---

## 13. LogManager — Per-Worker Log

### Arsitektur

```go
type workerLog struct {
    mu sync.Mutex
    f  *os.File
    bw *bufio.Writer  // 32 KB buffer
}

type LogManager struct {
    mu      sync.RWMutex
    handles map[string]*workerLog  // workerID → file handle
    done    chan struct{}
    wg      sync.WaitGroup
}
```

### Cara Kerja

**Lazy open:** File handle dibuka pertama kali `Writef()` dipanggil untuk worker tersebut. Tidak ada file yang dibuat untuk worker yang tidak pernah menulis log.

**Path log:** `log/{workerID}.log`  
Folder `log/` dibuat otomatis saat `newLogManager()` dipanggil (saat server start).

**Format baris:**
```
2026-03-26T14:30:22.123+07:00 [step:s01(s01_webhook)] WEBHOOK triggered
```

**Buffer & flush:**
- Buffer 32 KB per worker (`bufio.Writer`)
- Background goroutine flush semua handle setiap 3 detik
- Flush + tutup file saat worker berhenti (`globalLog.Close(workerID)`)
- Flush + tutup semua file saat server shutdown (`globalLog.Shutdown()`)

### API LogManager

```go
// Tulis satu baris log — thread-safe
globalLog.Writef(workerID, "[step:%s] %s", stepLabel, message)

// Flush + tutup file handle — dipanggil saat worker berhenti
globalLog.Close(workerID)

// Flush + tutup semua handle — dipanggil saat server shutdown
globalLog.Shutdown()
```

### Contoh Isi File Log

```
2026-03-26T14:30:22.001+07:00 [step:-] WEBHOOK registered POST /recipe-agent-v1/recipe
2026-03-26T14:30:22.500+07:00 [step:01 – Receive Input(s01_webhook)] WEBHOOK triggered
2026-03-26T14:30:23.100+07:00 [step:02 – Input Classification(s02_detect)] CALL_API POST https://... → HTTP 200
2026-03-26T14:30:24.000+07:00 [step:03 – Route(s03_branch_type)] BRANCH → no case matched — sequential
2026-03-26T14:30:25.800+07:00 workflow completed
```

### Akses Log

File log hanya tersedia melalui filesystem — tidak ada HTTP endpoint untuk membacanya.

```bash
# Ikuti log secara real-time
tail -f log/recipe-agent-v1.log

# Cari error
grep "PANIC\|_error\|ERROR" log/recipe-agent-v1.log

# Log beberapa hari terakhir
ls -lt log/
```

---

## 14. Workflow JSON — Semua Variasi

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

### Save ke File Manager (dari pipeline)

```json
{
  "id": "simpan",
  "type": "call_api",
  "value": {
    "method":  "POST",
    "url":     "http://localhost:8080/files/save",
    "headers": { "Content-Type": "application/json" },
    "body": {
      "tag":     "output",
      "content": "{{json:prev_step.result.response}}"
    }
  }
}
```

Akses URL file: `{{simpan.result.response}}` (berisi JSON `{"name":"...","view_url":"..."}`)

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

## 15. Lifecycle Worker

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
      │                             globalLog.Close(workerID)
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
                                    globalLog.Close(workerID)
```

---

## 16. Concurrency & Thread Safety

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
| `LogManager.handles` read | `sync.RWMutex` RLock | Lazy lookup per Writef |
| `LogManager.handles` write | `sync.RWMutex` Lock | Lazy open & Close |
| `workerLog.bw` write | `sync.Mutex` | Satu writer per handle |

---

## 17. Graceful Shutdown

```go
sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
<-sigCtx.Done()

// 1. Stop semua worker (cancel semua context)
rt.StopAll()

// 2. Shutdown HTTP server (tunggu max 5 detik)
srv.Shutdown(shutdownCtx)

// 3. Trigger final flush store
store.shutdown()

// 4. Flush & tutup semua file handle log worker
globalLog.Shutdown()
```

Urutan penting: worker stop dulu → server stop → store flush → log flush.  
Dengan urutan ini, tidak ada request yang diterima setelah worker berhenti, dan tidak ada log yang hilang.

---

## 18. Build & Run

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

### File & folder runtime

```
workers.gob          ← persistent store (auto-dibuat pada run pertama)
workers-*.gob.tmp    ← temporary saat write (langsung hilang)
log/                 ← folder log per-worker (auto-dibuat)
  └─ <workerID>.log  ← satu file per worker, buffered 32 KB
output/              ← folder file manager (auto-dibuat saat pertama upload/save)
  └─ agent-recipe_20260326-143022_a1b2c3d4.json
```

Port default `:8080` — ubah konstanta `addr` di `main()`.

---

## 19. Keterbatasan & Catatan

**Webhook menunggu 1 request per step.** Mode `loop` menangani ini otomatis. Jika request datang saat worker masih memproses, server balas HTTP 429 — caller bisa retry.

**`call_api` body hanya JSON.** `form-urlencoded` dan `multipart` tidak didukung.

**Vars bersifat statik per-run.** Vars dibaca fresh dari store setiap iterasi loop, tapi tidak berubah di tengah eksekusi satu run. Untuk nilai yang berubah dinamis dalam satu run, gunakan output step (`{{stepId.field}}`) atau `set_var`.

**pipelineCtx tidak persisten.** Data pipeline hilang saat worker selesai atau restart. Hanya vars yang persisten.

**Status tidak persisten.** `run_count`, `last_error`, `last_run_at` di `/status` reset saat server restart.

**Log hanya filesystem.** Tidak ada HTTP endpoint untuk membaca file `log/{workerID}.log`. Akses langsung via filesystem atau tail command.

**File manager tidak punya autentikasi.** Siapapun yang bisa reach port 8080 bisa upload/download/hapus file di `output/`.

**Storage flat file.** Untuk write frequency sangat tinggi, pertimbangkan SQLite atau BoltDB.

**Tidak ada autentikasi.** Tambahkan middleware atau reverse proxy sebelum expose ke internet.

**Satu proses.** Tidak dirancang untuk multi-instance atau distributed deployment.

**Jump limit 1000.** Satu run tidak bisa melakukan lebih dari 1000 lompatan (branch/next). Ini proteksi infinite loop — workflow normal tidak akan mendekati limit ini.