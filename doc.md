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
9. [API Reference](#9-api-reference)
10. [curl Guide](#10-curl-guide)
11. [Workflow JSON — Semua Variasi](#11-workflow-json--semua-variasi)
12. [Webhook Path Convention](#12-webhook-path-convention)
13. [Lifecycle Worker](#13-lifecycle-worker)
14. [Concurrency & Thread Safety](#14-concurrency--thread-safety)
15. [Build & Run](#15-build--run)
16. [Keterbatasan & Catatan](#16-keterbatasan--catatan)

---

## 1. Overview

Worker Engine adalah HTTP server yang memungkinkan pengguna mendefinisikan **workflow** dalam bentuk JSON, lalu menjalankannya sebagai worker independen. Setiap worker berjalan di goroutine tersendiri dan mengeksekusi langkah-langkah (steps) secara sekuensial.

**Fitur utama:**

| Fitur | Keterangan |
|-------|------------|
| Webhook | Terima HTTP request (GET/POST) sebagai trigger |
| Console Log | Cetak pesan ke stdout server |
| Call API | Panggil URL eksternal (GET/POST, header & body bebas) |
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
```

---

## 2. Arsitektur Sistem

```
┌──────────────────────────────────────────────────────────┐
│                    HTTP Server :8080                     │
│                                                          │
│  ServeMux                                                │
│  ├── /create   POST  → handleCreate                      │
│  ├── /list     GET   → handleList                        │
│  ├── /get      GET   → handleGet                         │
│  ├── /update   PUT   → handleUpdate                      │
│  ├── /delete   DELETE→ handleDelete                      │
│  ├── /run      POST  → handleRun                         │
│  ├── /stop     POST  → handleStop                        │
│  └── /         *     → webhookRouter (catch-all)         │
│                           │                              │
│                    ┌──────▼──────────────────┐           │
│                    │     webhookRouter        │           │
│                    │  map[path → hookEntry]   │           │
│                    │  sync.RWMutex            │           │
│                    └──────────────────────────┘           │
├──────────────────────────────────────────────────────────┤
│                      Runtime                             │
│   map[workerID → context.CancelFunc]                     │
│                                                          │
│   goroutine worker-A: step1 → step2 → step3             │
│   goroutine worker-B: step1 → step2                      │
│   goroutine worker-C: step1 → ...                        │
├──────────────────────────────────────────────────────────┤
│                  Store (in-memory + disk)                 │
│   map[workerID → *Worker]  ← sync.RWMutex               │
│   Flush: gob encode → CreateTemp → Sync → Rename         │
│   File: workers.gob                                      │
└──────────────────────────────────────────────────────────┘
```

### Alur Request Create → Run

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
     │   for each step:
     │     "webhook"  → register path di webhookRouter
     │                  block sampai request datang
     │     "log"      → log.Printf langsung
     │     "call_api" → http.NewRequestWithContext → Do()
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
- **Type-aware**: gob memahami tipe Go secara native, tidak perlu reflection seperti `json.Marshal`
- **In-memory map**: data disimpan di `map[string]*Worker` yang selalu ada di RAM — read langsung dari map, tidak pernah hit disk saat read

```
Read  → O(1) dari map (in-memory), zero disk I/O
Write → encode gob → atomic write ke disk
```

### 3.2 Atomic Write (write-then-rename)

```go
os.CreateTemp()    // buat file temporary
tmp.Write(data)    // tulis data ke tmp
tmp.Sync()         // fsync — pastikan data di disk (bukan hanya OS buffer)
os.Rename(tmp, storeFile) // atomic replace
```

Kenapa ini efisien dan aman:
- `os.Rename` di semua OS (Linux, Windows, macOS) adalah operasi **atomic** di level filesystem
- Jika proses crash di tengah write, file lama **tidak pernah rusak** — crash hanya meninggalkan file `.tmp` yang bisa dibersihkan
- Tidak ada partial write yang bisa corrupt `workers.gob`

### 3.3 sync.RWMutex (read-write lock)

Store menggunakan `sync.RWMutex`, bukan `sync.Mutex` biasa:

```go
// Read: banyak goroutine boleh baca bersamaan
func (s *Store) Get(id string) (*Worker, bool) {
    s.mu.RLock()        // shared lock
    defer s.mu.RUnlock()
    ...
}

// Write: eksklusif, blok semua reader
func (s *Store) Save(w *Worker) error {
    s.mu.Lock()         // exclusive lock
    defer s.mu.Unlock()
    ...
}
```

Ini penting karena operasi `GET` dan `LIST` jauh lebih sering dari write — multiple request bisa baca data secara paralel tanpa blocking.

### 3.4 Buffered Channel di Webhook (non-blocking signal)

```go
arrived := make(chan struct{}, 1)  // buffer size 1
```

Buffer size 1 memastikan bahwa jika HTTP handler selesai kirim signal **sebelum** goroutine worker siap menerimanya, signal tidak hilang dan handler tidak block. `struct{}` dipilih karena ukurannya **0 byte** — tidak mengalokasikan memori apapun, murni sinyal.

### 3.5 context.WithCancel (cooperative cancellation)

Setiap worker berjalan dengan `context.WithCancel`. Ketika `/stop` dipanggil, `cancel()` dipanggil — ini menyebarkan sinyal stop ke semua operasi yang sedang berjalan:

- `execWebhook`: `select { case <-ctx.Done() }` menghentikan wait
- `execCallAPI`: `http.NewRequestWithContext(ctx, ...)` membatalkan HTTP request in-flight secara otomatis

Tidak ada busy-loop, tidak ada polling — murni event-driven lewat channel.

### 3.6 webhookRouter — O(1) dispatch

webhookRouter menggunakan `map[string]*hookEntry` dengan lookup O(1) per request, vs `http.ServeMux` yang melakukan prefix matching. Untuk jumlah webhook yang banyak ini lebih efisien dan sekaligus menghindari panic duplikasi yang ada di ServeMux.

### 3.7 Zero External Dependency

Tidak ada `go.mod` yang memerlukan `go get`. Semua menggunakan stdlib:

```
bytes, context, crypto/rand, encoding/gob, encoding/json,
fmt, io, log, net/http, os, path/filepath, reflect, sync, time
```

Artinya: tidak ada dependency tree, tidak ada version conflict, binary lebih kecil, compile lebih cepat.

---

## 4. Domain Model

### Worker

```go
type Worker struct {
    ID      string    // UUID v4, unik global
    Name    string    // nama deskriptif
    Steps   []Step    // urutan langkah workflow
    Running bool      // status runtime saat ini
    Created time.Time // waktu dibuat
    Updated time.Time // waktu terakhir diubah
}
```

### Step

```go
type Step struct {
    Type    string       // "webhook" | "log" | "call_api"
    Webhook *WebhookStep // isi jika Type == "webhook"
    Log     *LogStep     // isi jika Type == "log"
    CallAPI *CallAPIStep // isi jika Type == "call_api"
}
```

Pointer (`*WebhookStep` dll) digunakan agar field yang tidak relevan tidak mengalokasikan memori dan tidak muncul di JSON output (`omitempty`).

### WebhookStep

```go
type WebhookStep struct {
    Method HTTPMethod  // "GET" | "POST"
    Path   string      // e.g. "/hook/order"
}
```

Path aktual yang terdaftar di runtime: `/<workerID><path>` — bukan `<path>` mentah.

### CallAPIStep

```go
type CallAPIStep struct {
    Method  HTTPMethod             // "GET" | "POST"
    URL     string                 // full URL termasuk scheme
    Headers map[string]string      // optional, bebas key-value
    Body    map[string]interface{} // optional, hanya untuk POST
}
```

---

## 5. Storage — Gob + Atomic Write

### File

```
workers.gob   ← data aktif
workers-*.gob.tmp  ← file temporary (hilang setelah rename berhasil)
```

### Siklus Write

```
1. mu.Lock()
2. update in-memory map
3. gob.NewEncoder(&buf).Encode(s.data)
4. os.CreateTemp(dir, "workers-*.gob.tmp")
5. tmp.Write(buf.Bytes())
6. tmp.Sync()          ← fsync, guarantee durability
7. os.Rename(tmp, "workers.gob")   ← atomic
8. mu.Unlock()
```

### Siklus Read (startup)

```
1. os.Open("workers.gob")
2. gob.NewDecoder(f).Decode(&s.data)
3. map siap digunakan di RAM
```

Setelah startup, semua read (`Get`, `List`) langsung dari map di RAM — **tidak ada disk I/O saat runtime**.

### Crash Safety

| Skenario | Hasil |
|----------|-------|
| Crash sebelum `tmp.Write` selesai | `workers.gob` tidak tersentuh, data lama aman |
| Crash setelah `Write` tapi sebelum `Rename` | `workers.gob` tidak tersentuh, `.tmp` bisa dibersihkan manual |
| Crash setelah `Rename` | Data baru sudah tersimpan sempurna |

---

## 6. Webhook Router

### Masalah dengan http.ServeMux

`http.ServeMux.HandleFunc` akan **panic** jika path yang sama didaftarkan dua kali (Go 1.22+) atau diam-diam mengabaikan registrasi kedua (Go versi lebih lama). Ini menjadi masalah ketika worker di-restart atau di-update karena akan mencoba mendaftarkan ulang path yang sama.

### Solusi: webhookRouter

```go
type webhookRouter struct {
    mu      sync.RWMutex
    entries map[string]*hookEntry   // path → entry
}
```

Satu handler `"/"` (catch-all) didaftarkan ke ServeMux. Semua request yang tidak cocok dengan route API akan diteruskan ke `webhookRouter.ServeHTTP()`, yang melakukan lookup ke map secara O(1).

```
ServeMux routing priority:
  /create, /list, /get, /update, /delete, /run, /stop  ← exact match, priority tinggi
  /                                                     ← catch-all, priority rendah
```

### Path Isolation per Worker

Webhook path di JSON (`"path"`) hanya digunakan sebagai suffix. Path aktual di runtime:

```
fullPath = "/" + workerID + cfg.Path
```

Contoh:

```
Worker A  id=a1b2c3d4-...   path=/hook/order
Worker B  id=e5f6a7b8-...   path=/hook/order

Registered:
  /a1b2c3d4-.../hook/order  → Worker A
  /e5f6a7b8-.../hook/order  → Worker B
```

Dua worker dengan path JSON identik tidak pernah bentrok.

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
defer hooks.unregister(fullPath) ← keluar dari map otomatis
```

Entry **otomatis dihapus** setelah webhook triggered atau worker distop — tidak ada path zombie.

---

## 7. Runtime — Goroutine per Worker

### Struktur

```go
type Runtime struct {
    mu      sync.Mutex
    cancels map[string]context.CancelFunc
}
```

Setiap worker aktif punya satu entry di map ini. `CancelFunc` adalah tombol stop-nya.

### Start

```go
ctx, cancel := context.WithCancel(context.Background())
rt.cancels[w.ID] = cancel
go runWorker(ctx, w, store, hooks)
```

### Stop

```go
cancel()                    // broadcast cancel ke semua operasi dalam worker
delete(rt.cancels, w.ID)    // hapus dari map
```

### runWorker — Sequential Step Execution

```go
for _, step := range w.Steps {
    select {
    case <-ctx.Done():
        return   // hentikan jika stop dipanggil di antara step
    default:
    }
    // eksekusi step...
}
```

Pengecekan `ctx.Done()` dilakukan **di antara setiap step**, bukan hanya saat blocking. Ini memastikan worker berhenti dengan bersih bahkan jika step non-blocking (seperti `log`) sedang berjalan.

### Auto-restart saat Server Restart

```go
for _, w := range store.List() {
    if w.Running {
        app.runtime.Start(w, store, hooks)
    }
}
```

Semua worker yang tersimpan dengan `Running: true` dijalankan kembali secara otomatis saat server startup.

---

## 8. UUID v4 — ID Generator

```go
func uniqueID() string {
    var b [16]byte
    rand.Read(b[:])           // 16 byte dari crypto/rand (CSPRNG)
    b[6] = (b[6] & 0x0f) | 0x40  // set version = 4
    b[8] = (b[8] & 0x3f) | 0x80  // set variant = RFC 4122
    return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
        b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
```

- Menggunakan `crypto/rand` (CSPRNG) bukan `math/rand` (PRNG) — tidak bisa diprediksi
- Format output: `xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx`
- Peluang collision: **1 dalam 2¹²²** ≈ mustahil secara praktis
- Fallback ke `UnixNano` hanya jika OS gagal provide entropy (tidak pernah terjadi pada kondisi normal)

ID custom tetap bisa dikirim di body JSON — sistem hanya generate UUID jika field `id` kosong.

---

## 9. API Reference

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
| ANY | `/<workerID><path>` | Trigger webhook worker |

### Response Format

Semua response menggunakan `Content-Type: application/json`.

**Sukses:**
```json
{ ...object... }
```

**Error:**
```json
{ "error": "pesan error" }
```

### Status Code

| Code | Situasi |
|------|---------|
| 200 | OK (list, get, run, stop) |
| 201 | Created (create berhasil) |
| 400 | Bad Request (JSON invalid) |
| 404 | Not Found (worker tidak ada) |
| 405 | Method Not Allowed |
| 500 | Internal Server Error (gagal simpan) |

---

### POST /create

Buat worker baru. Jika `running: true`, worker langsung dijalankan.

**Request body:**

| Field | Tipe | Wajib | Keterangan |
|-------|------|-------|------------|
| `id` | string | tidak | UUID custom; auto-generate jika kosong |
| `name` | string | ya | Nama deskriptif worker |
| `running` | bool | tidak | Jalankan langsung setelah dibuat |
| `steps` | array | ya | Daftar langkah workflow |

**Response:** `201 Created`

```json
{
  "id": "a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d",
  "name": "Order Notification",
  "steps": [...],
  "running": true,
  "created": "2026-03-23T10:00:00Z",
  "updated": "2026-03-23T10:00:00Z"
}
```

---

### GET /list

Kembalikan array semua worker yang tersimpan.

**Response:** `200 OK`

```json
[
  { "id": "...", "name": "...", "running": true, ... },
  { "id": "...", "name": "...", "running": false, ... }
]
```

---

### GET /get?id=\<id\>

Ambil satu worker berdasarkan ID.

**Response:** `200 OK` atau `404 Not Found`

---

### PUT /update

Update worker secara penuh (full replacement). Field `id` wajib ada dan harus sudah terdaftar. Jika steps berubah dan worker sedang running, worker akan di-restart otomatis.

**Behavior update:**

| Kondisi | Aksi |
|---------|------|
| Steps tidak berubah, `running` tetap | Tidak ada restart |
| Steps berubah, worker running | Stop → Save → Start ulang |
| `running: false` dikirim | Worker distop jika sedang jalan |
| `running: true`, worker belum jalan | Worker dijalankan |

---

### DELETE /delete?id=\<id\>

Stop worker (jika running) lalu hapus dari store permanen.

**Response:** `200 OK`

```json
{ "deleted": "a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d" }
```

---

### POST /run?id=\<id\>

Jalankan worker yang sedang berhenti. Jika sudah running, kembalikan status tanpa error.

**Response:** `200 OK`

```json
{ "status": "started" }
// atau
{ "status": "already running" }
```

---

### POST /stop?id=\<id\>

Hentikan worker yang sedang berjalan. State `running` diupdate di store.

**Response:** `200 OK`

```json
{ "status": "stopped" }
```

---

## 10. curl Guide

### Create Worker (UUID auto-generate)

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Order Notification",
    "running": true,
    "steps": [
      {
        "type": "webhook",
        "webhook": { "method": "POST", "path": "/hook/order" }
      },
      {
        "type": "log",
        "log": { "message": "Order baru diterima!" }
      },
      {
        "type": "call_api",
        "call_api": {
          "method": "POST",
          "url": "https://webhook.site/your-id",
          "headers": { "Authorization": "Bearer TOKEN" },
          "body": { "text": "Ada order baru!" }
        }
      }
    ]
  }'
```

Simpan `id` dari response, contoh: `a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d`

---

### Trigger Webhook

Format path: `/<workerID><path_dari_json>`

```bash
# Contoh: workerID=a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d, path=/hook/order
curl -X POST http://localhost:8080/a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d/hook/order \
  -H "Content-Type: application/json" \
  -d '{"order_id": "ORD-001", "total": 150000}'

# GET webhook
curl http://localhost:8080/a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d/hook/ping
```

---

### List Semua Worker

```bash
curl http://localhost:8080/list
```

---

### Get Worker by ID

```bash
curl "http://localhost:8080/get?id=a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d"
```

---

### Update Worker

```bash
curl -X PUT http://localhost:8080/update \
  -H "Content-Type: application/json" \
  -d '{
    "id": "a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d",
    "name": "Order Notification v2",
    "running": true,
    "steps": [
      {
        "type": "webhook",
        "webhook": { "method": "POST", "path": "/hook/order" }
      },
      {
        "type": "log",
        "log": { "message": "v2 — Order diterima!" }
      }
    ]
  }'
```

---

### Run Worker

```bash
curl -X POST "http://localhost:8080/run?id=a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d"
```

---

### Stop Worker

```bash
curl -X POST "http://localhost:8080/stop?id=a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d"
```

---

### Delete Worker

```bash
curl -X DELETE "http://localhost:8080/delete?id=a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d"
```

---

## 11. Workflow JSON — Semua Variasi

### Step: webhook

```json
{
  "type": "webhook",
  "webhook": {
    "method": "POST",
    "path": "/hook/order"
  }
}
```

```json
{
  "type": "webhook",
  "webhook": {
    "method": "GET",
    "path": "/hook/ping"
  }
}
```

### Step: log

```json
{
  "type": "log",
  "log": {
    "message": "Pesan apapun yang ingin dicetak ke console"
  }
}
```

### Step: call_api — GET tanpa body

```json
{
  "type": "call_api",
  "call_api": {
    "method": "GET",
    "url": "https://wttr.in/Surabaya?format=3"
  }
}
```

### Step: call_api — POST dengan body

```json
{
  "type": "call_api",
  "call_api": {
    "method": "POST",
    "url": "https://api.example.com/notify",
    "body": {
      "message": "hello",
      "status": "ok"
    }
  }
}
```

### Step: call_api — POST dengan header + body

```json
{
  "type": "call_api",
  "call_api": {
    "method": "POST",
    "url": "https://api.example.com/data",
    "headers": {
      "Authorization": "Bearer TOKEN_RAHASIA",
      "X-Source": "worker-engine",
      "X-Custom-Header": "nilai-bebas"
    },
    "body": {
      "key": "value",
      "nested": { "a": 1 }
    }
  }
}
```

### Contoh Workflow Lengkap — Webhook → Log → Call API

```json
{
  "name": "Order to Slack",
  "running": true,
  "steps": [
    {
      "type": "webhook",
      "webhook": { "method": "POST", "path": "/hook/order" }
    },
    {
      "type": "log",
      "log": { "message": "Order masuk, kirim ke Slack..." }
    },
    {
      "type": "call_api",
      "call_api": {
        "method": "POST",
        "url": "https://hooks.slack.com/services/XXX/YYY/ZZZ",
        "body": { "text": "Ada order baru dari webhook!" }
      }
    }
  ]
}
```

### Contoh Workflow — Log Only (tanpa webhook)

```json
{
  "name": "Simple Logger",
  "running": true,
  "steps": [
    {
      "type": "log",
      "log": { "message": "Worker ini langsung selesai setelah log ini." }
    }
  ]
}
```

---

## 12. Webhook Path Convention

Path yang didaftarkan di JSON adalah **suffix**, bukan path final. Path aktual di runtime:

```
/<workerID><path>
```

| JSON `path` | Worker ID | Actual endpoint |
|-------------|-----------|-----------------|
| `/hook/order` | `a3f2c1d4-...` | `/a3f2c1d4-.../hook/order` |
| `/hook/order` | `b7c8d9e0-...` | `/b7c8d9e0-.../hook/order` |
| `/ping` | `c1d2e3f4-...` | `/c1d2e3f4-.../ping` |

Dua worker dengan path JSON sama tidak akan pernah bentrok karena worker ID selalu unik (UUID v4).

**Tip:** Gunakan path yang pendek dan semantik di JSON, misalnya `/hook/order`, `/ping`, `/trigger`. Prefix UUID ditambahkan otomatis — tidak perlu dipikirkan saat mendefinisikan workflow.

---

## 13. Lifecycle Worker

```
              POST /create
                   │
                   ▼
            ┌─────────────┐
            │   STORED    │  running: false
            └─────────────┘
                   │
         POST /run │ atau create dengan running: true
                   ▼
            ┌─────────────┐
            │   RUNNING   │  goroutine aktif, webhook terdaftar
            └─────────────┘
                   │
       ┌───────────┴───────────┐
       │                       │
  POST /stop            Workflow selesai
  (manual)           (semua step tuntas)
       │                       │
       ▼                       ▼
┌─────────────┐         ┌─────────────┐
│   STOPPED   │         │  COMPLETED  │
│running:false│         │running:true │  ← flag tidak otomatis false
└─────────────┘         └─────────────┘
       │
  DELETE /delete
       │
       ▼
  (dihapus dari store)
```

**Catatan:** Setelah workflow selesai secara natural (semua step tuntas), flag `running` di store tidak otomatis diset `false`. Untuk menjalankan ulang workflow yang sama, panggil `/run?id=<id>`.

---

## 14. Concurrency & Thread Safety

| Komponen | Mekanisme | Alasan |
|----------|-----------|--------|
| `Store.data` (read) | `sync.RWMutex` — `RLock` | Banyak goroutine bisa baca bersamaan |
| `Store.data` (write) | `sync.RWMutex` — `Lock` | Write eksklusif, blok semua reader |
| `Runtime.cancels` | `sync.Mutex` | Map tidak concurrency-safe by default |
| `webhookRouter.entries` (read) | `sync.RWMutex` — `RLock` | Setiap HTTP request butuh baca map |
| `webhookRouter.entries` (write) | `sync.RWMutex` — `Lock` | Register/unregister saat worker start/stop |
| Worker goroutine | `context.WithCancel` | Stop signal tanpa shared state |
| Webhook signal | `chan struct{}` buffer 1 | Non-blocking, zero-alloc signal |

Tidak ada race condition: semua akses shared state dilindungi mutex yang tepat.

---

## 15. Build & Run

### Requirement

- Go 1.18+ (untuk generics support, meskipun tidak digunakan — untuk future compatibility)
- Tidak ada external package

### Jalankan langsung

```bash
go run main.go
```

### Build binary

```bash
go build -o worker-engine main.go
./worker-engine
```

### Build binary kecil (stripped)

```bash
go build -ldflags="-s -w" -o worker-engine main.go
```

Flag `-s -w` menghapus debug symbol dan DWARF info — mengecilkan ukuran binary secara signifikan.

### Cross-compile

```bash
# Windows dari Linux/Mac
GOOS=windows GOARCH=amd64 go build -o worker-engine.exe main.go

# Linux dari Windows/Mac
GOOS=linux GOARCH=amd64 go build -o worker-engine main.go

# macOS ARM (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -o worker-engine main.go
```

### File yang dihasilkan saat runtime

```
worker-engine       ← binary (atau main.go jika go run)
workers.gob         ← persistent store (auto-dibuat)
workers-*.gob.tmp   ← temporary saat write (langsung hilang)
```

### Port default

Server berjalan di `:8080`. Untuk mengubah, edit baris ini di `main()`:

```go
addr := ":8080"
```

---

## 16. Keterbatasan & Catatan

**Webhook hanya menunggu 1 request per step.** Setelah satu request diterima, step selesai dan workflow lanjut ke step berikutnya. Untuk webhook yang terus-menerus (loop), jalankan ulang worker dengan `/run` setelah workflow selesai.

**Path webhook di ServeMux tidak bisa unregister.** Ini bukan masalah karena webhook tidak menggunakan ServeMux — menggunakan `webhookRouter` dengan map yang bisa di-mutate bebas.

**Storage bukan database.** `workers.gob` adalah single file flat. Untuk ribuan worker dengan write frekuensi sangat tinggi, pertimbangkan migrasi ke SQLite atau BoltDB.

**Tidak ada autentikasi.** API endpoint tidak dilindungi. Untuk production, tambahkan middleware autentikasi (API key / JWT) di depan handler.

**`call_api` body hanya mendukung JSON.** `map[string]interface{}` selalu di-encode sebagai JSON. Form-urlencoded atau multipart tidak didukung.

**Satu proses, satu server.** Tidak dirancang untuk multi-instance / distributed. Semua state ada di RAM dan satu file.

**Kompatibilitas OS.** Sepenuhnya cross-platform: Windows, Linux, macOS. Tidak ada syscall platform-specific.