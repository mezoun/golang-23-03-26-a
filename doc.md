# Worker Engine — Dokumentasi Lengkap

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
9. [Teknik Efisiensi](#9-teknik-efisiensi)
10. [API Reference](#10-api-reference)
11. [Workflow JSON — Semua Variasi](#11-workflow-json--semua-variasi)
12. [Lifecycle Worker](#12-lifecycle-worker)
13. [Concurrency & Thread Safety](#13-concurrency--thread-safety)
14. [Build & Run](#14-build--run)
15. [Keterbatasan & Catatan](#15-keterbatasan--catatan)

---

## 1. Overview

Worker Engine adalah HTTP server yang memungkinkan pendefinisian **workflow** dalam bentuk JSON dan menjalankannya sebagai worker independen. Setiap worker berjalan di goroutine tersendiri dan mengeksekusi steps secara sekuensial.

**Fitur utama:**

| Fitur | Keterangan |
|-------|------------|
| Webhook | Terima HTTP request (GET/POST), body otomatis masuk pipeline |
| Console Log | Cetak pesan ke stdout, mendukung template `{{stepId.field}}` |
| Call API | Panggil URL eksternal, response body masuk pipeline untuk step berikutnya |
| Pipeline | Output setiap step (webhook body, API response) dirujuk step lain by ID |
| Vars | Konfigurasi statik multi-wadah, antar-wadah bisa saling merujuk |
| Worker Mode | `once` — jalan 1x; `loop` — restart otomatis |
| Persistent | Worker tersimpan di file gob, survive server restart |
| CRUD penuh | Create, Read, Update, Delete + Run/Stop |

**Stack:**
```
Language   : Go (standard library only)
Storage    : encoding/gob + atomic write
HTTP       : net/http (ServeMux + custom webhookRouter)
Concurrency: goroutine + context.WithCancel + sync.RWMutex
ID Worker  : UUID v4 via crypto/rand
ID Step    : 8-char hex via crypto/rand
Template   : custom renderer — zero dependency
```

---

## 2. Arsitektur Sistem

```
┌──────────────────────────────────────────────────────────────┐
│                     HTTP Server :8080                        │
│                                                              │
│  ServeMux                                                    │
│  ├── /create /list /get /update /delete /run /stop           │
│  └── /  ──────────────────────► webhookRouter                │
│                                   map[path → hookEntry]      │
│                                   chan map[string]interface{} │
├──────────────────────────────────────────────────────────────┤
│  Runtime  (goroutine per worker)                             │
│                                                              │
│  worker-A:                                                   │
│    webhook(id=hook) → pctx.Steps["hook"] = body             │
│    log              → renderTemplate("{{hook.usr}}")         │
│    call_api(id=ai)  → pctx.Steps["ai"] = response           │
│    log              → renderTemplate("{{ai.result.answer}}") │
├──────────────────────────────────────────────────────────────┤
│  Store                                                       │
│  map[id → *Worker]  in-memory  sync.RWMutex                  │
│  flush: gob encode → CreateTemp → fsync → Rename             │
└──────────────────────────────────────────────────────────────┘
```

---

## 3. Domain Model

### Worker

```go
type Worker struct {
    ID      string                       // UUID v4
    Name    string                       // nama deskriptif
    Mode    WorkerMode                   // "once" | "loop"
    Vars    map[string]map[string]string // konfigurasi statik multi-wadah
    Steps   []Step                       // urutan langkah
    Running bool                         // status runtime (sync dengan goroutine)
    Created time.Time
    Updated time.Time
}
```

### WorkerMode

| Mode | Setelah complete | Stop |
|------|-----------------|------|
| `"once"` (default) | `running → false` | Otomatis / `/stop` |
| `"loop"` | Restart otomatis | Hanya via `/stop` |

### Step

```go
type Step struct {
    ID    string     // 8-char hex, auto-generate jika kosong
    Name  string     // opsional, untuk log readability
    Type  string     // "webhook" | "log" | "call_api"
    Value *StepValue // config sesuai type
}
```

Step ID wajib diisi jika outputnya akan dirujuk step lain. Scope unik per-worker — worker lain boleh punya step ID yang sama.

### StepValue

```go
type StepValue struct {
    // log
    Message string

    // webhook & call_api
    Method HTTPMethod  // "GET" | "POST"

    // webhook
    Path string        // suffix, actual path = /<workerID><path>

    // call_api
    URL     string
    Headers map[string]string
    Body    json.RawMessage
}
```

Seluruh string field di StepValue mendukung template `{{...}}`.

### pipelineCtx

```go
type pipelineCtx struct {
    Steps map[string]map[string]interface{} // stepID → output data
}
```

Diisi secara progresif selama workflow berjalan. Step `webhook` menyimpan request body, step `call_api` menyimpan response body.

---

## 4. Template Engine

### Dua Namespace

| Syntax | Sumber | Kapan diisi |
|--------|--------|-------------|
| `{{wadah.key}}` | `vars["wadah"]["key"]` | Saat worker dibuat (statik) |
| `{{stepId.field}}` | `pctx.Steps[stepId]["field"]` | Runtime, saat step itu selesai |
| `{{stepId.a.b}}` | nested dot-notation | Runtime |

### `{{.field}}` Dihapus

Tidak ada lagi referensi tanpa parent. Semua akses harus eksplisit:
- `{{hook.usr}}` bukan `{{.usr}}`
- `{{ai.result.response}}` bukan shorthand apapun

Ini memaksa workflow menjadi eksplisit dan mudah di-trace.

### Resolusi Vars (antar-wadah)

Vars diresolution secara iteratif maksimal 10x, memungkinkan:

```json
"vars": {
  "pvt": { "cf_id": "abc123" },
  "glb": {
    "base_url": "https://api.example.com/{{pvt.cf_id}}",
    "full_url":  "{{glb.base_url}}/ai/run"
  }
}
```

`{{glb.full_url}}` akan resolved menjadi `https://api.example.com/abc123/ai/run`.

### Dimana Template Bisa Dipakai

| Step | Field |
|------|-------|
| `log` | `message` |
| `call_api` | `url`, semua `headers` values, seluruh `body` (raw JSON string) |
| `vars` values | boleh embed `{{wadah.key}}` dari wadah lain |

---

## 5. Pipeline — Data Antar Step

### Alur Data

```
Step: webhook (id="hook")
  ← menerima POST body: {"sys":"...", "usr":"..."}
  → pctx.Steps["hook"] = {"sys":"...", "usr":"..."}

Step: call_api (id="ai")
  → request body: {"messages":[{"content":"{{hook.usr}}"}]}
       ↑ template resolved dari pctx.Steps["hook"]["usr"]
  ← response: {"result":{"response":"Pizza!"},"success":true}
  → pctx.Steps["ai"] = {"result":{"response":"Pizza!"},"success":true}

Step: log
  → message: "AI jawab: {{ai.result.response}}"
       ↑ resolved → "AI jawab: Pizza!"
```

### Output call_api

Response JSON di-parse otomatis menjadi map yang bisa dirujuk:

```
Response: {"result":{"response":"Pizza!"},"success":true}

{{ai.result.response}}  →  "Pizza!"
{{ai.success}}          →  "true"
```

Jika response **bukan JSON**, fallback ke:
```
{{stepId.raw}}    →  raw string response body
{{stepId.status}} →  HTTP status code (int)
```

### Step Tanpa ID

Step yang tidak punya `id` tetap dieksekusi, tapi outputnya **tidak disimpan** ke pipeline. Berguna untuk step `log` yang tidak perlu direferensikan.

---

## 6. Storage — Gob + Atomic Write

### Kenapa Gob

- Binary format — lebih kecil dan lebih cepat dari JSON
- `json.RawMessage` di `StepValue.Body` disimpan sebagai `[]byte` — gob encode langsung tanpa overhead interface{}
- Semua read dari in-memory map setelah startup — zero disk I/O saat runtime

### Atomic Write

```
gob.Encode → os.CreateTemp → Write → Sync(fsync) → os.Rename
```

`os.Rename` atomic di semua OS — crash mid-write tidak corrupt `workers.gob`.

### Schema Migration

Saat startup, worker dengan step `Value == nil` (format lama) otomatis di-drop dengan log pesan. Server tidak crash — hanya worker lama yang tidak kompatibel dibuang.

---

## 7. Webhook Router

### Masalah

`http.ServeMux.HandleFunc` panic pada duplikasi path — masalah saat worker restart.

### Solusi

Satu catch-all `"/"` di ServeMux mendelegasikan ke `webhookRouter` — map dinamis yang bisa di-mutate bebas:

```go
type webhookRouter struct {
    mu      sync.RWMutex
    entries map[string]*hookEntry
}

type hookEntry struct {
    method   string
    workerID string
    arrived  chan map[string]interface{}  // kirim body langsung
}
```

### Path Isolation

```
JSON path    : /food
Worker ID    : a3f2c1d4-...
Actual path  : /a3f2c1d4-.../food
```

Dua worker dengan path `/food` tidak pernah bentrok.

### Body Flow

```
HTTP Request datang
    ↓
webhookRouter.ServeHTTP()
    ↓ json.Unmarshal(body) → map[string]interface{}
    ↓
entry.arrived <- bodyMap   (buffered chan size 1, non-blocking)
    ↓
execWebhook() return bodyMap
    ↓
runWorker: pctx.store(step.ID, bodyMap)
```

---

## 8. Runtime — Goroutine per Worker

### Self-Cleanup

```go
go func() {
    for {
        runWorker(ctx, w, store, hooks)

        select {
        case <-ctx.Done():
            // Stop dipanggil — update Running=false di store
            delete(rt.cancels, w.ID)
            return
        default:
        }

        if mode == ModeLoop {
            // ctx-aware sleep 50ms — cegah spin jika tidak ada step blocking
            select {
            case <-ctx.Done(): return
            case <-time.After(50ms):
            }
            continue
        }

        // once: update Running=false, exit
        delete(rt.cancels, w.ID)
        return
    }
}()
```

`Running` di store selalu di-update setelah goroutine selesai — `/list` mencerminkan state real.

### pipelineCtx Reset

Setiap iterasi loop membuat `pipelineCtx` baru — data dari iterasi sebelumnya tidak bocor ke iterasi berikutnya.

---

## 9. Teknik Efisiensi

| Teknik | Detail |
|--------|--------|
| **Gob binary** | Lebih kecil & cepat dari JSON untuk storage |
| **In-memory map** | Read O(1), zero disk I/O saat runtime |
| **Atomic rename** | Crash-safe write di semua OS |
| **sync.RWMutex** | Banyak reader paralel, write eksklusif |
| **Buffered channel** | `chan map[string]interface{}` size 1 — signal + data sekaligus, non-blocking |
| **context.WithCancel** | Stop propagates ke HTTP in-flight request otomatis |
| **webhookRouter O(1)** | map lookup vs prefix-matching ServeMux |
| **Template fast path** | Jika tidak ada `{{` — return langsung tanpa processing |
| **Goroutine self-cleanup** | Tidak ada goroutine leak — `cancels` map selalu akurat |
| **Zero external dep** | stdlib only — no CVE, no version conflict |

---

## 10. API Reference

| Method | Endpoint | Deskripsi |
|--------|----------|-----------|
| POST | `/create` | Buat worker |
| GET | `/list` | List semua worker |
| GET | `/get?id=<id>` | Get satu worker |
| PUT | `/update` | Update worker (full replace) |
| DELETE | `/delete?id=<id>` | Hapus worker |
| POST | `/run?id=<id>` | Jalankan worker |
| POST | `/stop?id=<id>` | Hentikan worker |
| ANY | `/<workerID><path>` | Trigger webhook step |

### Worker Object

```jsonc
{
  "id":      "a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d",
  "name":    "AI Worker",
  "mode":    "loop",
  "vars": {
    "glb": { "api_url": "https://..." },
    "pvt": { "token": "..." }
  },
  "steps": [
    { "id": "hook", "name": "terima", "type": "webhook", "value": { "method": "POST", "path": "/ai" } },
    { "id": "ai",   "name": "query",  "type": "call_api", "value": { "url": "{{glb.api_url}}", ... } },
    { "name": "log", "type": "log", "value": { "message": "{{ai.result.response}}" } }
  ],
  "running": true,
  "created": "2026-03-26T03:47:00Z",
  "updated": "2026-03-26T03:47:00Z"
}
```

### POST /create

| Field | Tipe | Wajib | Keterangan |
|-------|------|-------|------------|
| `id` | string | tidak | UUID custom; auto-generate jika kosong |
| `name` | string | ya | Nama deskriptif |
| `mode` | string | tidak | `"once"` (default) atau `"loop"` |
| `vars` | object | tidak | Wadah variabel statik |
| `running` | bool | tidak | Jalankan langsung setelah dibuat |
| `steps` | array | ya | Daftar langkah |

---

## 11. Workflow JSON — Semua Variasi

### Webhook step

```json
{ "id": "hook", "name": "terima", "type": "webhook", "value": { "method": "POST", "path": "/trigger" } }
```

### Log step (dengan template)

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

Akses response: `{{ip.ip}}`

### Call API — POST dengan template

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
        { "role": "user",   "content": "{{hook.usr}}" }
      ]
    }
  }
}
```

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
  },
  "env": {
    "timeout": "30"
  }
}
```

---

## 12. Lifecycle Worker

```
POST /create
      │
      ▼
  [ STORED ]  running: false
      │
      │ running:true / POST /run
      ▼
  [ RUNNING ]  goroutine aktif
      │
      ├── POST /stop ────────────► running: false
      │
      ├── mode=once, complete ───► running: false  (auto)
      │                            POST /run untuk ulangi
      │
      └── mode=loop, complete ───► restart otomatis
                                   running: tetap true
```

---

## 13. Concurrency & Thread Safety

| Komponen | Mekanisme | Alasan |
|----------|-----------|--------|
| `Store.data` read | `sync.RWMutex` RLock | Banyak goroutine baca paralel |
| `Store.data` write | `sync.RWMutex` Lock | Eksklusif |
| `Runtime.cancels` | `sync.Mutex` | Map tidak concurrency-safe |
| `webhookRouter.entries` read | `sync.RWMutex` RLock | Setiap HTTP request |
| `webhookRouter.entries` write | `sync.RWMutex` Lock | Register/unregister |
| Worker cancel | `context.WithCancel` | Stop tanpa shared state |
| Webhook payload | `chan map[string]interface{}` buf 1 | Data + signal sekaligus |
| `pipelineCtx` | goroutine-local | Tidak di-share antar goroutine |

---

## 14. Build & Run

```bash
# Jalankan langsung
go run main.go

# Build binary
go build -o worker-engine main.go

# Build kecil
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
workers.gob         ← persistent store (auto-dibuat)
workers-*.gob.tmp   ← temporary saat write (langsung hilang)
```

Port default `:8080` — ubah konstanta `addr` di `main()`.

---

## 15. Keterbatasan & Catatan

**Webhook menunggu 1 request per step.** Mode `loop` menangani ini otomatis — path re-register setiap iterasi.

**`call_api` body hanya JSON.** `form-urlencoded` dan `multipart` tidak didukung.

**Vars bersifat statik.** Vars tidak bisa diubah saat worker running. Untuk nilai dinamis, gunakan output step (`{{stepId.field}}`).

**pipelineCtx tidak persisten.** Data pipeline hilang saat worker selesai atau di-restart. Hanya vars yang persisten.

**Storage flat file.** Untuk write frequency sangat tinggi, pertimbangkan SQLite atau BoltDB.

**Tidak ada autentikasi.** Tambahkan middleware sebelum expose ke internet.

**Satu proses.** Tidak dirancang untuk multi-instance.