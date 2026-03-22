<div align="center">

<img src="https://raw.githubusercontent.com/devicons/devicon/master/icons/go/go-original-wordmark.svg" alt="Go" width="80" height="80"/>

# ⚙️ Worker Engine

**Lightweight HTTP-based workflow runner — built entirely on Go standard library.**

[![Go Version](https://img.shields.io/badge/Go-1.18%2B-00ADD8?style=flat-square&logo=go&logoColor=white)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)](LICENSE)
[![Zero Dependencies](https://img.shields.io/badge/dependencies-zero-brightgreen?style=flat-square)](#)
[![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20Linux%20%7C%20macOS-lightgrey?style=flat-square)](#)
[![Single File](https://img.shields.io/badge/source-single%20file-blueviolet?style=flat-square)](#)

<br/>

Define workflows in JSON. Run them as isolated workers.  
**Webhook → Log → Call API** — in one file, zero `go get`.

<br/>

[**Quick Start**](#-quick-start) · [**API Reference**](#-api-reference) · [**Workflow Examples**](#-workflow-examples) · [**How It Works**](#-how-it-works) · [**Documentation**](DOCUMENTATION.md)

</div>

---

## ✨ Features

| Feature | Description |
|---------|-------------|
| 🪝 **Webhook** | Accept incoming `GET` or `POST` requests as workflow triggers |
| 🖨️ **Console Log** | Print messages to server stdout at any step |
| 🌐 **Call API** | Hit external URLs — any method, headers, body |
| 💾 **Persistent** | Workers survive server restarts via `gob` storage |
| ♻️ **Auto-restart** | Running workers resume automatically on startup |
| 🔁 **Full CRUD** | Create, read, update, delete workers via REST |
| ▶️ **Run / Stop** | Start or stop any worker at runtime |
| 🔒 **No Collisions** | Webhook paths are namespaced by worker UUID |

---

## 🚀 Quick Start

### 1. Run

```bash
go run main.go
```

> Server starts on `:8080`. No config needed. `workers.gob` is created automatically.

### 2. Create a worker

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
    "name": "My First Worker",
    "running": true,
    "steps": [
      { "type": "webhook",  "webhook":  { "method": "POST", "path": "/hook/hello" } },
      { "type": "log",      "log":      { "message": "Webhook received!" } },
      { "type": "call_api", "call_api": { "method": "GET", "url": "https://httpbin.org/get" } }
    ]
  }'
```

Copy the `id` from the response.

### 3. Trigger the webhook

```bash
# Replace <id> with the UUID returned from /create
curl -X POST http://localhost:8080/<id>/hook/hello \
  -H "Content-Type: application/json" \
  -d '{"hello": "world"}'
```

### 4. Watch the server console

```
[worker:a3f2c1d4-...] started — My First Worker
[worker:a3f2c1d4-...] WEBHOOK registered  POST  /a3f2c1d4-.../hook/hello
[worker:a3f2c1d4-...] WEBHOOK POST /a3f2c1d4-.../hook/hello | body: {"hello":"world"}
[worker:a3f2c1d4-...] WEBHOOK triggered, continuing workflow
[worker:a3f2c1d4-...] LOG → Webhook received!
[worker:a3f2c1d4-...] CALL_API GET https://httpbin.org/get → HTTP 200 | ...
[worker:a3f2c1d4-...] workflow completed
```

---

## 📐 Architecture

```
┌──────────────────────────────────────────────────────────┐
│                    HTTP Server :8080                     │
│                                                          │
│  ServeMux                                                │
│  ├─ /create /list /get /update /delete /run /stop        │
│  └─ /  ──────────────────────► webhookRouter             │
│                                  map[path → hookEntry]   │
│                                  sync.RWMutex  O(1)      │
├──────────────────────────────────────────────────────────┤
│  Runtime          (goroutine per worker)                 │
│  ├─ worker-A  ──  step① → step② → step③                 │
│  ├─ worker-B  ──  step① → step②                         │
│  └─ worker-C  ──  step① → ...                           │
├──────────────────────────────────────────────────────────┤
│  Store                                                   │
│  map[id → *Worker]  in-memory  sync.RWMutex              │
│  flush: gob encode → CreateTemp → fsync → Rename         │
└──────────────────────────────────────────────────────────┘
```

---

## ⚡ How It Works

<details>
<summary><strong>Storage — Gob + Atomic Write</strong></summary>

<br/>

Workers are serialized using `encoding/gob` (binary, faster and smaller than JSON) into `workers.gob`.

Every write follows a **crash-safe atomic pattern**:

```
encode → CreateTemp → Write → fsync → Rename (atomic)
```

`os.Rename` is atomic on all platforms — a crash mid-write never corrupts the live file.

After startup, **all reads come from an in-memory map** — zero disk I/O at runtime.

</details>

<details>
<summary><strong>webhookRouter — Dynamic Dispatch Table</strong></summary>

<br/>

`http.ServeMux` panics on duplicate path registration — a problem when workers restart or update.

The fix: **one `"/"` catch-all** on ServeMux delegates to a custom `webhookRouter`:

```go
type webhookRouter struct {
    mu      sync.RWMutex
    entries map[string]*hookEntry   // O(1) lookup
}
```

Paths are registered and unregistered freely at runtime. No panics. No stale entries — `defer hooks.unregister(fullPath)` ensures cleanup after each step.

</details>

<details>
<summary><strong>Webhook Path Isolation</strong></summary>

<br/>

The `path` field in your JSON is a **suffix**, not the final path. At runtime:

```
fullPath = "/" + workerID + cfg.Path
```

Two workers with `"path": "/hook/order"` get completely separate endpoints:

```
/a1b2c3d4-..../hook/order   ← Worker A
/e5f6a7b8-..../hook/order   ← Worker B
```

Guaranteed zero collision, even with identical workflow definitions.

</details>

<details>
<summary><strong>Concurrency Model</strong></summary>

<br/>

| Component | Primitive | Reason |
|-----------|-----------|--------|
| `Store` reads | `sync.RWMutex` RLock | Parallel reads, no contention |
| `Store` writes | `sync.RWMutex` Lock | Exclusive, safe flush |
| `Runtime` map | `sync.Mutex` | Prevent concurrent start/stop |
| `webhookRouter` reads | `sync.RWMutex` RLock | Every HTTP request reads the map |
| `webhookRouter` writes | `sync.RWMutex` Lock | Register/unregister on worker events |
| Worker cancellation | `context.WithCancel` | Clean stop without shared state |
| Webhook signal | `chan struct{}` (buf 1) | Non-blocking, **zero allocation** |

</details>

<details>
<summary><strong>UUID v4 — Collision-free IDs</strong></summary>

<br/>

IDs are generated using `crypto/rand` (CSPRNG), not `math/rand`:

```go
var b [16]byte
rand.Read(b[:])
b[6] = (b[6] & 0x0f) | 0x40  // version 4
b[8] = (b[8] & 0x3f) | 0x80  // RFC 4122 variant
```

Collision probability: **1 in 2¹²²** — negligible for any practical scale.

</details>

---

## 📡 API Reference

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/create` | Create a new worker |
| `GET` | `/list` | List all workers |
| `GET` | `/get?id=<id>` | Get a single worker |
| `PUT` | `/update` | Full-replace a worker |
| `DELETE` | `/delete?id=<id>` | Delete a worker permanently |
| `POST` | `/run?id=<id>` | Start a stopped worker |
| `POST` | `/stop?id=<id>` | Stop a running worker |
| `ANY` | `/<id><path>` | Trigger a webhook step |

### Worker Object

```jsonc
{
  "id":      "a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d",  // UUID v4, auto-generated if omitted
  "name":    "My Worker",
  "running": true,
  "steps":   [...],
  "created": "2026-03-23T10:00:00Z",
  "updated": "2026-03-23T10:00:00Z"
}
```

---

## 🔧 Workflow Examples

### Minimal — Log only

```json
{
  "name": "Hello Worker",
  "running": true,
  "steps": [
    { "type": "log", "log": { "message": "Hello from worker!" } }
  ]
}
```

### Webhook → Notify Slack

```json
{
  "name": "Slack Notifier",
  "running": true,
  "steps": [
    { "type": "webhook",  "webhook":  { "method": "POST", "path": "/hook/event" } },
    { "type": "log",      "log":      { "message": "Event received, notifying Slack..." } },
    { "type": "call_api", "call_api": {
        "method": "POST",
        "url":    "https://hooks.slack.com/services/XXX/YYY/ZZZ",
        "body":   { "text": "New event triggered!" }
    }}
  ]
}
```

### Webhook → Authenticated API

```json
{
  "name": "CRM Sync",
  "running": true,
  "steps": [
    { "type": "webhook",  "webhook":  { "method": "POST", "path": "/hook/contact" } },
    { "type": "log",      "log":      { "message": "Syncing contact to CRM..." } },
    { "type": "call_api", "call_api": {
        "method":  "POST",
        "url":     "https://api.crm.example.com/v1/contacts",
        "headers": { "Authorization": "Bearer TOKEN", "X-Source": "worker-engine" },
        "body":    { "source": "webhook", "auto": true }
    }}
  ]
}
```

### GET Trigger → GET External API

```json
{
  "name": "Weather Checker",
  "running": true,
  "steps": [
    { "type": "webhook",  "webhook":  { "method": "GET", "path": "/check" } },
    { "type": "log",      "log":      { "message": "Checking weather..." } },
    { "type": "call_api", "call_api": { "method": "GET", "url": "https://wttr.in/Surabaya?format=3" } }
  ]
}
```

---

## 🖥️ curl Cheatsheet

```bash
ID="paste-your-uuid-here"

# Create
curl -s -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" -d '{...}'

# Trigger webhook  (path = /<id><json_path>)
curl -X POST http://localhost:8080/$ID/hook/order \
  -H "Content-Type: application/json" -d '{"key":"val"}'

# List all
curl -s http://localhost:8080/list | jq

# Get one
curl -s "http://localhost:8080/get?id=$ID" | jq

# Update
curl -X PUT http://localhost:8080/update \
  -H "Content-Type: application/json" -d '{"id":"'$ID'", ...}'

# Run / Stop
curl -X POST "http://localhost:8080/run?id=$ID"
curl -X POST "http://localhost:8080/stop?id=$ID"

# Delete
curl -X DELETE "http://localhost:8080/delete?id=$ID"
```

---

## 🔄 Worker Lifecycle

```
POST /create
      │
      ▼
  [ STORED ]  ── running: false
      │
      │  POST /run  (or create with running: true)
      ▼
  [ RUNNING ] ── goroutine active, webhook registered
      │
      ├──── POST /stop ──────────────► [ STOPPED ]
      │                                     │
      └──── all steps done ──► [ COMPLETED ]│ POST /run
                                            ▼
                                        [ RUNNING ]
                                            │
                                   DELETE /delete
                                            │
                                            ▼
                                       (removed)
```

---

## 🏗️ Build

```bash
# Run directly
go run main.go

# Build binary
go build -o worker-engine main.go

# Build optimized (smaller binary)
go build -ldflags="-s -w" -o worker-engine main.go
```

### Cross-compile

```bash
# Windows
GOOS=windows GOARCH=amd64 go build -o worker-engine.exe main.go

# Linux
GOOS=linux   GOARCH=amd64 go build -o worker-engine main.go

# macOS Apple Silicon
GOOS=darwin  GOARCH=arm64 go build -o worker-engine main.go
```

---

## 📦 Project Structure

```
.
├── main.go          # Entire application — single file
├── workers.gob      # Persistent store (auto-created on first run)
├── DOCUMENTATION.md # In-depth technical documentation
└── README.md
```

---

## ⚠️ Limitations

- **Webhook waits for exactly 1 request** per step. Re-run the worker with `/run` to listen again.
- **No authentication** on API endpoints — add middleware before exposing to the internet.
- **Single process** — not designed for multi-instance or distributed deployment.
- **`call_api` body is JSON only** — `form-urlencoded` and `multipart` are not supported.
- **Storage is a flat file** — for very high write frequency, consider migrating to SQLite or BoltDB.

---

## 📄 License

```
MIT License — free to use, modify, and distribute.
```

---

<div align="center">

Built with pure Go · No dependencies · Cross-platform

</div>