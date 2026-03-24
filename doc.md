# Worker Engine — Dokumentasi Lengkap

> Single-file Go service untuk membuat dan menjalankan **workflow workers** berbasis HTTP.  
> Zero external dependency — murni Go standard library.

---

## Daftar Isi

1. [Overview](#1-overview)
2. [Arsitektur Sistem](#2-arsitektur-sistem)
3. [Teknik Efisiensi](#3-teknik-efisiensi)
4. [Domain Model](#4-domain-model)
5. [Storage — Gob + Atomic Write](#5-storage--gob--atomic-write)
6. [Webhook Router](#6-webhook-router)
7. [Runtime — Goroutine per Worker](#7-runtime--goroutine-per-worker)
8. [UUID v4 — ID Generator](#8-uuid-v4--id-generator)
9. [Pipeline Context & Template Rendering](#9-pipeline-context--template-rendering)
10. [API Reference](#10-api-reference)
11. [curl Guide](#11-curl-guide)
12. [Workflow JSON — Semua Variasi](#12-workflow-json--semua-variasi)
13. [Webhook Path Convention](#13-webhook-path-convention)
14. [Lifecycle Worker](#14-lifecycle-worker)
15. [Concurrency & Thread Safety](#15-concurrency--thread-safety)
16. [Build & Run](#16-build--run)
17. [Keterbatasan & Catatan](#17-keterbatasan--catatan)

---

## 1. Overview

Worker Engine adalah HTTP server yang memungkinkan pengguna mendefinisikan **workflow** dalam bentuk JSON, lalu menjalankannya sebagai worker independen. Setiap worker berjalan di goroutine tersendiri dan mengeksekusi langkah-langkah (steps) secara sekuensial.

**Fitur utama:**

| Fitur | Keterangan |
|-------|------------|
| Webhook | Terima HTTP request (GET/POST) sebagai trigger |
| Console Log | Cetak pesan ke stdout server, mendukung template `{{.field}}` |
| Call API | Panggil URL eksternal (GET/POST, header & body bebas), mendukung template |
| Pipeline Context | Data dari webhook body mengalir ke semua step berikutnya |
| Worker Mode | `once` — jalan 1x; `loop` — restart otomatis selamanya |
| Persistent | Worker tersimpan di file, survive server restart |
| CRUD penuh | Create, Read, Update, Delete worker |
| Run / Stop | Jalankan atau hentikan worker kapan saja |
| Auto-restart | Worker yang `running: true` otomatis jalan saat server restart |

**Stack:**

```
Language   : Go (standard library only)
Storage    : encoding/gob + atomic file write
HTTP       : net/http (ServeMux + custom webhookRouter)
Concurrency: goroutine + context.WithCancel + sync.RWMutex
ID         : UUID v4 via crypto/rand
Template   : custom renderer strings.Replace — zero dependency
```

---

## 2. Arsitektur Sistem

```
┌──────────────────────────────────────────────────────────┐
│                    HTTP Server :8080                     │
│                                                          │
│  ServeMux                                                │
│  ├── /create   POST   → handleCreate                     │
│  ├── /list     GET    → handleList                       │
│  ├── /get      GET    → handleGet                        │
│  ├── /update   PUT    → handleUpdate                     │
│  ├── /delete   DELETE → handleDelete                     │
│  ├── /run      POST   → handleRun                        │
│  ├── /stop     POST   → handleStop                       │
│  └── /         *      → webhookRouter (catch-all)        │
│                            │                             │
│                     ┌──────▼──────────────────┐          │
│                     │     webhookRouter        │          │
│                     │  map[path → hookEntry]   │          │
│                     │  chan pipelineCtx        │          │
│                     │  sync.RWMutex  O(1)      │          │
│                     └──────────────────────────┘          │
├──────────────────────────────────────────────────────────┤
│                      Runtime                             │
│   map[workerID → context.CancelFunc]                     │
│                                                          │
│   goroutine worker-A:                                    │
│     webhook → pipelineCtx → log({{.x}}) → call_api      │
│   goroutine worker-B: step① → step②                     │
├──────────────────────────────────────────────────────────┤
│                  Store (in-memory + disk)                 │
│   map[workerID → *Worker]  ← sync.RWMutex               │
│   Flush: gob encode → CreateTemp → fsync → Rename        │
│   File: workers.gob                                      │
└──────────────────────────────────────────────────────────┘
```

### Alur Request Create → Run → Pipeline

```
POST /create
     │
     ▼
Decode JSON → assign UUID → Store.Save()
     │                           │
     │                    gob encode → tmp file
     │                    fsync → rename (atomic)
     │
     ├── running: true?
     │        │
     │        ▼
     │   Runtime.Start() → go runWorker()
     │        │
     │        ▼
     │   pipelineCtx = {} (kosong)
     │   for each step:
     │     "webhook"  → register path di webhookRouter
     │                  block sampai request datang
     │                  pipelineCtx ← body + headers
     │     "log"      → renderTemplate(message, pipelineCtx)
     │     "call_api" → renderTemplate(url/body/headers, pipelineCtx)
     │                  http.NewRequestWithContext → Do()
     │
     ▼
Response 201 + JSON worker
```

---

## 3. Teknik Efisiensi

### 3.1 Gob Encoding (bukan JSON untuk storage)

`encoding/gob` digunakan untuk serialisasi ke disk, bukan JSON. Alasannya:

- **Lebih kecil**: gob menggunakan binary format, ukuran file lebih kecil dari JSON
- **Lebih cepat**: encode/decode biner lebih cepat dari text parsing
- **Type-aware**: gob memahami tipe Go secara native
- **In-memory map**: data selalu ada di RAM — read langsung dari map, zero disk I/O saat runtime

```
Read  → O(1) dari map (in-memory), zero disk I/O
Write → encode gob → atomic write ke disk
```

> **Catatan:** `CallAPIStep.Body` disimpan sebagai `json.RawMessage` (`[]byte`) bukan `map[string]interface{}` — menghindari error gob pada nested `[]interface{}` dan sekaligus zero-parse overhead.

### 3.2 Atomic Write (write-then-rename)

```go
os.CreateTemp()              // buat file temporary
tmp.Write(data)              // tulis data ke tmp
tmp.Sync()                   // fsync — pastikan data di disk
os.Rename(tmp, storeFile)    // atomic replace
```

- `os.Rename` di semua OS adalah operasi **atomic** di level filesystem
- Crash mid-write tidak pernah corrupt `workers.gob`
- Tidak ada partial write

### 3.3 sync.RWMutex (read-write lock)

```go
// Read: banyak goroutine boleh baca bersamaan
func (s *Store) Get(id string) (*Worker, bool) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    ...
}

// Write: eksklusif, blok semua reader
func (s *Store) Save(w *Worker) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    ...
}
```

`GET` dan `LIST` jauh lebih sering dari write — multiple request bisa baca data secara paralel tanpa blocking.

### 3.4 Buffered Channel di Webhook — zero-alloc signal

```go
arrived := make(chan pipelineCtx, 1)  // buffer size 1
```

Buffer size 1 memastikan signal tidak hilang jika goroutine belum siap menerima. `pipelineCtx` membawa data payload langsung lewat channel — tidak ada shared memory, tidak ada mutex tambahan.

### 3.5 context.WithCancel (cooperative cancellation)

Setiap worker berjalan dengan `context.WithCancel`. Ketika `/stop` dipanggil:

- `execWebhook`: `select { case <-ctx.Done() }` menghentikan wait
- `execCallAPI`: `http.NewRequestWithContext(ctx, ...)` membatalkan HTTP request in-flight otomatis

Tidak ada busy-loop, tidak ada polling — murni event-driven.

### 3.6 webhookRouter — O(1) dispatch

`map[string]*hookEntry` dengan lookup O(1) per request. Jauh lebih efisien dari prefix matching `http.ServeMux` untuk banyak webhook, dan sekaligus menghindari panic duplikasi.

### 3.7 Template Rendering — fast path

```go
func renderTemplate(s string, pctx pipelineCtx) string {
    if !strings.Contains(s, "{{") {
        return s  // fast path — tidak ada template, langsung return
    }
    ...
}
```

Step yang tidak mengandung `{{` sama sekali tidak diproses — zero overhead untuk step statis.

### 3.8 Goroutine Self-Cleanup

```go
go func() {
    runWorker(ctx, w, store, hooks)
    // hapus diri dari cancels map setelah selesai natural
    rt.mu.Lock()
    delete(rt.cancels, w.ID)
    rt.mu.Unlock()
}()
```

Tidak ada goroutine leak. `Runtime.cancels` selalu akurat mencerminkan goroutine yang benar-benar aktif.

### 3.9 Zero External Dependency

```
bytes, context, crypto/rand, encoding/gob, encoding/json,
fmt, io, log, net/http, os, path/filepath, reflect, strings, sync, time
```

Tidak ada `go.mod` dengan `require`. Binary lebih kecil, compile lebih cepat, tidak ada CVE dari dependency pihak ketiga.

---

## 4. Domain Model

### Worker

```go
type Worker struct {
    ID      string     // UUID v4, unik global
    Name    string     // nama deskriptif
    Mode    WorkerMode // "once" | "loop"
    Steps   []Step     // urutan langkah workflow
    Running bool       // status runtime saat ini (sync dengan goroutine)
    Created time.Time  // waktu dibuat
    Updated time.Time  // waktu terakhir diubah
}
```

### WorkerMode

```go
type WorkerMode string

const (
    ModeOnce WorkerMode = "once"  // default: jalan 1x lalu berhenti
    ModeLoop WorkerMode = "loop"  // restart otomatis setelah setiap completion
)
```

| Mode | Setelah complete | Untuk stop |
|------|-----------------|------------|
| `once` | `running` → `false`, bisa `/run` lagi | Otomatis / `/stop` |
| `loop` | Restart otomatis, `running` tetap `true` | Hanya via `/stop` |

### Step

```go
type Step struct {
    Type    string       // "webhook" | "log" | "call_api"
    Webhook *WebhookStep
    Log     *LogStep
    CallAPI *CallAPIStep
}
```

Pointer digunakan agar field yang tidak relevan tidak mengalokasikan memori dan tidak muncul di JSON (`omitempty`).

### WebhookStep

```go
type WebhookStep struct {
    Method HTTPMethod  // "GET" | "POST"
    Path   string      // suffix, e.g. "/hook/order"
}
```

Path aktual di runtime: `/<workerID><path>`

### CallAPIStep

```go
type CallAPIStep struct {
    Method  HTTPMethod        // "GET" | "POST"
    URL     string            // mendukung {{.field}}
    Headers map[string]string // values mendukung {{.field}}
    Body    json.RawMessage   // raw JSON, mendukung {{.field}} di dalamnya
}
```

### pipelineCtx

```go
type pipelineCtx struct {
    Body    map[string]interface{} // parsed JSON body dari webhook
    Headers map[string]string      // incoming request headers
}
```

Dibuat fresh setiap kali worker mulai, diisi oleh step `webhook`, dikonsumsi oleh step berikutnya.

---

## 5. Storage — Gob + Atomic Write

### File

```
workers.gob          ← data aktif
workers-*.gob.tmp    ← temporary (hilang setelah rename berhasil)
```

### Siklus Write

```
1. mu.Lock()
2. update in-memory map
3. gob.NewEncoder(&buf).Encode(s.data)
4. os.CreateTemp(dir, "workers-*.gob.tmp")
5. tmp.Write(buf.Bytes())
6. tmp.Sync()                         ← fsync
7. os.Rename(tmp, "workers.gob")      ← atomic
8. mu.Unlock()
```

### Siklus Read (startup)

```
1. os.Open("workers.gob")
2. gob.NewDecoder(f).Decode(&s.data)
3. map siap di RAM
```

Setelah startup, semua read langsung dari map — **zero disk I/O saat runtime**.

### Crash Safety

| Skenario | Hasil |
|----------|-------|
| Crash sebelum `Write` selesai | `workers.gob` tidak tersentuh |
| Crash setelah `Write`, sebelum `Rename` | `workers.gob` tidak tersentuh, `.tmp` bisa dibersihkan manual |
| Crash setelah `Rename` | Data baru tersimpan sempurna |

---

## 6. Webhook Router

### Masalah dengan http.ServeMux

`http.ServeMux.HandleFunc` panic jika path didaftarkan dua kali (Go 1.22+). Ini masalah saat worker restart atau update karena mencoba re-register path yang sama.

### Solusi: webhookRouter

```go
type webhookRouter struct {
    mu      sync.RWMutex
    entries map[string]*hookEntry
}

type hookEntry struct {
    method   string
    workerID string
    arrived  chan pipelineCtx  // membawa payload, bukan sekadar signal
}
```

Satu handler `"/"` (catch-all) di ServeMux meneruskan semua ke `webhookRouter.ServeHTTP()`, yang lookup O(1) ke map.

### Payload Flow

```
HTTP Request datang
    │
    ▼
webhookRouter.ServeHTTP()
    │
    ├── parse body → map[string]interface{}
    ├── collect headers → map[string]string
    ├── buat pipelineCtx
    │
    ▼
entry.arrived <- pipelineCtx   ← kirim ke goroutine worker
    │
    ▼
execWebhook() menerima pipelineCtx
    │
    ▼
runWorker() meneruskan ke step berikutnya (log, call_api, ...)
```

### Lifecycle Entry

```
execWebhook dipanggil
    │
    ▼
hooks.register(fullPath, ...)   ← masuk ke map
    │
    ▼
block: select { arrived / ctx.Done }
    │
    ▼
defer hooks.unregister(fullPath) ← otomatis keluar dari map
```

Tidak ada path zombie.

---

## 7. Runtime — Goroutine per Worker

### Struktur

```go
type Runtime struct {
    mu      sync.Mutex
    cancels map[string]context.CancelFunc
}
```

### Mode Loop vs Once

```go
go func() {
    for {
        runWorker(ctx, w, store, hooks)

        // cek cancel (Stop dipanggil)
        select {
        case <-ctx.Done():
            // update Running=false di store
            delete(rt.cancels, w.ID)
            return
        default:
        }

        if mode == ModeLoop {
            continue  // restart otomatis
        }

        // once: selesai, update Running=false
        delete(rt.cancels, w.ID)
        return
    }
}()
```

### Running Flag Sync

`running` di store selalu di-update sinkron dengan state goroutine:

| Event | `running` di store |
|-------|-------------------|
| `/run` atau create dengan `running:true` | `true` |
| `once` — workflow selesai natural | `false` ← otomatis |
| `/stop` dipanggil | `false` ← otomatis |
| `loop` — sedang berjalan | `true` (selalu) |

---

## 8. UUID v4 — ID Generator

```go
func uniqueID() string {
    var b [16]byte
    rand.Read(b[:])                    // crypto/rand — CSPRNG
    b[6] = (b[6] & 0x0f) | 0x40       // version 4
    b[8] = (b[8] & 0x3f) | 0x80       // RFC 4122 variant
    return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
        b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
```

- `crypto/rand` — tidak bisa diprediksi, aman secara kriptografis
- Peluang collision: **1 dalam 2¹²²**
- Format: `xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx`
- ID custom tetap bisa dikirim di body JSON jika ingin

---

## 9. Pipeline Context & Template Rendering

### Konsep

Data yang dikirim ke webhook bisa digunakan di step-step berikutnya menggunakan syntax template `{{.namaField}}`.

```
Webhook body: {"sys": "jawab singkat", "usr": "apa itu kopi?"}
                │
                ▼
         pipelineCtx.Body
                │
     ┌──────────┴──────────┐
     ▼                     ▼
  log message          call_api body/url/headers
  "{{.sys}}"    →   {"messages":[{"content":"{{.sys}}"}]}
```

### Syntax

| Syntax | Keterangan |
|--------|------------|
| `{{.field}}` | Ambil field top-level dari webhook body |
| `{{.field.sub}}` | Nested field (dot-notation) |

Field yang tidak ditemukan render sebagai string kosong (silent, tidak error).

### Didukung di

| Step | Field yang didukung |
|------|---------------------|
| `log` | `message` |
| `call_api` | `url`, semua `headers` values, seluruh `body` (raw JSON string) |

### Contoh

Webhook body diterima:
```json
{ "sys": "jawab max 5 kata", "usr": "makanan terenak di dunia?" }
```

Step log:
```json
{ "type": "log", "log": { "message": "sys={{.sys}} usr={{.usr}}" } }
```
Output: `sys=jawab max 5 kata usr=makanan terenak di dunia?`

Step call_api:
```json
{
  "type": "call_api",
  "call_api": {
    "method": "POST",
    "url": "https://api.example.com/{{.endpoint}}",
    "body": {
      "messages": [
        { "role": "system", "content": "{{.sys}}" },
        { "role": "user",   "content": "{{.usr}}" }
      ]
    }
  }
}
```

### Implementasi

```go
func renderTemplate(s string, pctx pipelineCtx) string {
    if !strings.Contains(s, "{{") {
        return s  // fast path
    }
    // iterasi ganti semua {{.key}} dengan nilai dari pctx.Body
}

func resolveKey(key string, data map[string]interface{}) interface{} {
    parts := strings.SplitN(key, ".", 2)
    // support dot-notation rekursif
}
```

---

## 10. API Reference

Base URL: `http://localhost:8080`

### Tabel Endpoint

| Method | Path | Deskripsi |
|--------|------|-----------|
| POST | `/create` | Buat worker baru |
| GET | `/list` | Ambil semua worker |
| GET | `/get?id=<id>` | Ambil satu worker |
| PUT | `/update` | Update worker (full replace) |
| DELETE | `/delete?id=<id>` | Hapus worker permanen |
| POST | `/run?id=<id>` | Jalankan worker |
| POST | `/stop?id=<id>` | Hentikan worker |
| ANY | `/<workerID><path>` | Trigger webhook step |

### Status Code

| Code | Situasi |
|------|---------|
| 200 | OK |
| 201 | Created |
| 400 | JSON invalid |
| 404 | Worker tidak ditemukan |
| 405 | Method tidak sesuai |
| 500 | Gagal simpan ke store |

### Worker Object

```jsonc
{
  "id":      "a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d",
  "name":    "My Worker",
  "mode":    "once",        // "once" | "loop"
  "running": true,
  "steps":   [...],
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
| `running` | bool | tidak | Jalankan langsung setelah dibuat |
| `steps` | array | ya | Daftar langkah workflow |

### PUT /update

Jika steps berubah dan worker sedang running, worker di-restart otomatis dengan steps baru.

### POST /run

Response menyertakan `mode` aktif:
```json
{ "status": "started", "mode": "loop" }
```

---

## 11. curl Guide

### Create Worker (UUID auto-generate)

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{"name":"My Worker","mode":"once","running":true,"steps":[...]}'
```

### Trigger Webhook

Format: `/<workerID><path_dari_json>`

```bash
# POST dengan body untuk pipeline
curl -X POST http://localhost:8080/<id>/hook/order \
  -H "Content-Type: application/json" \
  -d '{"sys":"jawab singkat","usr":"apa itu kopi?"}'

# GET webhook
curl http://localhost:8080/<id>/hook/ping
```

### List, Get, Run, Stop, Delete

```bash
ID="paste-uuid-disini"

curl http://localhost:8080/list
curl "http://localhost:8080/get?id=$ID"
curl -X POST "http://localhost:8080/run?id=$ID"
curl -X POST "http://localhost:8080/stop?id=$ID"
curl -X DELETE "http://localhost:8080/delete?id=$ID"
```

### Update Worker

```bash
curl -X PUT http://localhost:8080/update \
  -H "Content-Type: application/json" \
  -d '{"id":"'$ID'","name":"Updated","mode":"loop","running":true,"steps":[...]}'
```

### CLI-friendly (satu baris)

```bash
curl -X POST http://localhost:8080/create -H "Content-Type: application/json" -d '{"name":"test","mode":"loop","running":true,"steps":[{"type":"webhook","webhook":{"method":"POST","path":"/hook"}},{"type":"log","log":{"message":"got: {{.msg}}"}}]}'
```

---

## 12. Workflow JSON — Semua Variasi

### Step: webhook

```json
{ "type": "webhook", "webhook": { "method": "POST", "path": "/hook/order" } }
{ "type": "webhook", "webhook": { "method": "GET",  "path": "/ping" } }
```

### Step: log (dengan template)

```json
{ "type": "log", "log": { "message": "Pesan statis" } }
{ "type": "log", "log": { "message": "User: {{.usr}} | Sys: {{.sys}}" } }
{ "type": "log", "log": { "message": "nested: {{.user.name}}" } }
```

### Step: call_api — GET

```json
{ "type": "call_api", "call_api": { "method": "GET", "url": "https://api.ipify.org/?format=json" } }
```

### Step: call_api — POST dengan template body

```json
{
  "type": "call_api",
  "call_api": {
    "method": "POST",
    "url": "https://api.cloudflare.com/client/v4/accounts/ACCOUNT/ai/run/@cf/meta/llama-3.1-8b-instruct-fast",
    "headers": { "Authorization": "Bearer TOKEN" },
    "body": {
      "messages": [
        { "role": "system", "content": "{{.sys}}" },
        { "role": "user",   "content": "{{.usr}}" }
      ]
    }
  }
}
```

### Step: call_api — URL dinamis

```json
{
  "type": "call_api",
  "call_api": {
    "method": "GET",
    "url": "https://api.example.com/users/{{.user_id}}/profile"
  }
}
```

### Workflow Lengkap — Dynamic AI via Webhook

```bash
# Create (CLI-friendly)
curl -X POST http://localhost:8080/create -H "Content-Type: application/json" \
  -d '{"name":"AI Dynamic","mode":"loop","running":true,"steps":[{"type":"webhook","webhook":{"method":"POST","path":"/ai"}},{"type":"log","log":{"message":"Received: sys={{.sys}} usr={{.usr}}"}},{"type":"call_api","call_api":{"method":"POST","url":"https://api.cloudflare.com/client/v4/accounts/ACCOUNT/ai/run/@cf/meta/llama-3.1-8b-instruct-fast","headers":{"Authorization":"Bearer TOKEN"},"body":{"messages":[{"role":"system","content":"{{.sys}}"},{"role":"user","content":"{{.usr}}"}]}}},{"type":"log","log":{"message":"AI call done"}}]}'

# Trigger dengan data dinamis
curl -X POST http://localhost:8080/<id>/ai \
  -H "Content-Type: application/json" \
  -d '{"sys":"jawab max 5 kata","usr":"ibu kota Indonesia?"}'
```

---

## 13. Webhook Path Convention

Path di JSON adalah **suffix**. Path aktual di runtime:

```
/<workerID><path>
```

| JSON `path` | Worker ID | Actual endpoint |
|-------------|-----------|-----------------|
| `/hook/order` | `a3f2c1d4-...` | `/a3f2c1d4-.../hook/order` |
| `/hook/order` | `b7c8d9e0-...` | `/b7c8d9e0-.../hook/order` |
| `/ping` | `c1d2e3f4-...` | `/c1d2e3f4-.../ping` |

Dua worker dengan path JSON identik tidak pernah bentrok — UUID selalu unik.

---

## 14. Lifecycle Worker

```
              POST /create
                   │
                   ▼
            ┌─────────────┐
            │   STORED    │  running: false
            └─────────────┘
                   │
       running:true│ atau POST /run
                   ▼
            ┌─────────────┐
            │   RUNNING   │  goroutine aktif
            └─────────────┘
                   │
       ┌───────────┴────────────────┐
       │                            │
  POST /stop                  Workflow selesai
       │                            │
       ▼                    ┌───────┴────────┐
┌─────────────┐         mode=once        mode=loop
│   STOPPED   │             │                │
│running:false│             ▼                ▼
└─────────────┘      running:false     restart otomatis
       │             (POST /run        (running: true
  DELETE /delete      untuk ulang)      selamanya)
       │
       ▼
  (dihapus)
```

---

## 15. Concurrency & Thread Safety

| Komponen | Mekanisme | Alasan |
|----------|-----------|--------|
| `Store.data` (read) | `sync.RWMutex` — `RLock` | Banyak goroutine baca bersamaan |
| `Store.data` (write) | `sync.RWMutex` — `Lock` | Write eksklusif |
| `Runtime.cancels` | `sync.Mutex` | Map tidak concurrency-safe |
| `webhookRouter.entries` (read) | `sync.RWMutex` — `RLock` | Setiap HTTP request baca map |
| `webhookRouter.entries` (write) | `sync.RWMutex` — `Lock` | Register/unregister |
| Worker cancellation | `context.WithCancel` | Stop signal tanpa shared state |
| Webhook payload | `chan pipelineCtx` buffer 1 | Non-blocking, data langsung lewat channel |

---

## 16. Build & Run

### Requirement

- Go 1.18+
- Tidak ada external package

```bash
# Jalankan langsung
go run main.go

# Build binary
go build -o worker-engine main.go

# Build kecil (stripped)
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

### Port default

```go
addr := ":8080"  // ubah di main()
```

---

## 17. Keterbatasan & Catatan

**Webhook menunggu 1 request per step.** Setelah triggered, step selesai dan path di-unregister. Mode `loop` menangani ini secara otomatis — path re-register setiap iterasi.

**Template hanya membaca webhook body.** `pipelineCtx` saat ini hanya diisi dari step `webhook`. Step `call_api` response belum masuk ke pipeline (future feature).

**`call_api` body hanya JSON.** `form-urlencoded` dan `multipart` tidak didukung.

**Storage bukan database.** Untuk ribuan worker dengan write frekuensi sangat tinggi, pertimbangkan SQLite atau BoltDB.

**Tidak ada autentikasi.** Tambahkan middleware API key / JWT sebelum expose ke internet.

**Satu proses, satu server.** Tidak dirancang untuk multi-instance / distributed.

**Kompatibilitas OS.** Sepenuhnya cross-platform — tidak ada syscall platform-specific.