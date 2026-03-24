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
**Webhook → Log → Call API** — with dynamic data pipeline, in one file, zero `go get`.

<br/>

[**Quick Start**](#-quick-start) · [**Pipeline & Templates**](#-pipeline--templates) · [**API Reference**](#-api-reference) · [**Workflow Examples**](#-workflow-examples) · [**How It Works**](#-how-it-works) · [**Documentation**](DOCUMENTATION.md)

</div>

---

## ✨ Features

| Feature | Description |
|---------|-------------|
| 🪝 **Webhook** | Accept incoming `GET` or `POST` requests as workflow triggers |
| 🖨️ **Console Log** | Print messages to server stdout — supports `{{.field}}` templates |
| 🌐 **Call API** | Hit external URLs — method, headers, body, URL all support templates |
| 🔀 **Data Pipeline** | Webhook body flows into every subsequent step via `pipelineCtx` |
| 🔁 **Worker Modes** | `once` — run once; `loop` — restart automatically forever |
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
    "mode": "once",
    "running": true,
    "steps": [
      { "type": "webhook",  "webhook":  { "method": "POST", "path": "/hook/hello" } },
      { "type": "log",      "log":      { "message": "Got message: {{.msg}}" } },
      { "type": "call_api", "call_api": { "method": "GET", "url": "https://httpbin.org/get" } }
    ]
  }'
```

Copy the `id` from the response.

### 3. Trigger the webhook with data

```bash
curl -X POST http://localhost:8080/<id>/hook/hello \
  -H "Content-Type: application/json" \
  -d '{"msg": "hello world"}'
```

### 4. Watch the server console

```
[worker:a3f2c1d4-...] started — My First Worker
[worker:a3f2c1d4-...] WEBHOOK registered  POST  /a3f2c1d4-.../hook/hello
[worker:a3f2c1d4-...] WEBHOOK POST /a3f2c1d4-.../hook/hello | body: {"msg":"hello world"}
[worker:a3f2c1d4-...] WEBHOOK triggered, continuing workflow
[worker:a3f2c1d4-...] LOG → Got message: hello world
[worker:a3f2c1d4-...] CALL_API GET https://httpbin.org/get → HTTP 200 | ...
[worker:a3f2c1d4-...] workflow completed
```

---

## 🔀 Pipeline & Templates

Webhook body is automatically passed to every subsequent step via `pipelineCtx`. Use `{{.field}}` anywhere in `log`, `call_api` URL, headers, or body.

```
POST /hook  ←  {"sys": "answer briefly", "usr": "what is coffee?"}
                                │
                         pipelineCtx
                                │
              ┌─────────────────┼─────────────────┐
              ▼                 ▼                  ▼
         log message      call_api url       call_api body
        "{{.usr}}"    "…/{{.endpoint}}"   {"content":"{{.sys}}"}
```

### Template Syntax

| Syntax | Result |
|--------|--------|
| `{{.field}}` | Top-level field from webhook body |
| `{{.field.sub}}` | Nested field (dot-notation) |
| No `{{` | Returned as-is — zero overhead |

### Supported In

| Step | Supported fields |
|------|-----------------|
| `log` | `message` |
| `call_api` | `url`, all `headers` values, entire `body` JSON |

### Example

```bash
# Create a dynamic AI worker
curl -X POST http://localhost:8080/create -H "Content-Type: application/json" \
  -d '{"name":"AI Worker","mode":"loop","running":true,"steps":[{"type":"webhook","webhook":{"method":"POST","path":"/ai"}},{"type":"log","log":{"message":"sys={{.sys}} usr={{.usr}}"}},{"type":"call_api","call_api":{"method":"POST","url":"https://api.example.com/ai","headers":{"Authorization":"Bearer TOKEN"},"body":{"messages":[{"role":"system","content":"{{.sys}}"},{"role":"user","content":"{{.usr}}"}]}}}]}'

# Trigger with dynamic data — different every time
curl -X POST http://localhost:8080/<id>/ai \
  -H "Content-Type: application/json" \
  -d '{"sys":"answer in max 5 words","usr":"what is the best food?"}'
```

---

## 🔄 Worker Modes

| Mode | `running` | Behavior after completion |
|------|-----------|--------------------------|
| `"once"` *(default)* | `true` | Stops, `running` → `false`. Use `/run` to start again |
| `"loop"` | `true` | Restarts automatically, `running` stays `true` forever |
| *(any)* | `false` | Stored but idle. Use `/run` to start anytime |

```bash
# Run once — great for one-shot workflows
curl -X POST http://localhost:8080/create -H "Content-Type: application/json" \
  -d '{"name":"one shot","mode":"once","running":true,"steps":[...]}'

# Loop forever — great for always-on webhook listeners
curl -X POST http://localhost:8080/create -H "Content-Type: application/json" \
  -d '{"name":"always on","mode":"loop","running":true,"steps":[...]}'

# Start idle — run manually later
curl -X POST http://localhost:8080/create -H "Content-Type: application/json" \
  -d '{"name":"manual","running":false,"steps":[...]}'
curl -X POST "http://localhost:8080/run?id=<id>"
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
│                                  chan pipelineCtx  O(1)  │
├──────────────────────────────────────────────────────────┤
│  Runtime          (goroutine per worker)                 │
│  ├─ worker-A  ──  webhook → pipelineCtx → log → call_api │
│  ├─ worker-B  ──  step① → step②                         │
│  └─ worker-C  ──  loop: restart after each completion    │
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

Workers are serialized using `encoding/gob` (binary — faster and smaller than JSON) into `workers.gob`. `CallAPIStep.Body` is stored as `json.RawMessage` (`[]byte`) to avoid gob's limitation with `[]interface{}`.

Every write follows a **crash-safe atomic pattern**:

```
encode → CreateTemp → Write → fsync → Rename (atomic)
```

`os.Rename` is atomic on all platforms. After startup, **all reads come from an in-memory map** — zero disk I/O at runtime.

</details>

<details>
<summary><strong>Pipeline Context — Data flows between steps</strong></summary>

<br/>

When a webhook step receives a request, it parses the body into a `pipelineCtx`:

```go
type pipelineCtx struct {
    Body    map[string]interface{}
    Headers map[string]string
}
```

This context is passed to every subsequent step. The template engine replaces `{{.field}}` with values from `pipelineCtx.Body` — supporting dot-notation for nested fields.

Fast path: if a string contains no `{{`, it is returned as-is with zero processing.

</details>

<details>
<summary><strong>webhookRouter — Dynamic Dispatch Table</strong></summary>

<br/>

`http.ServeMux` panics on duplicate path registration — a problem when workers restart.

The fix: **one `"/"` catch-all** on ServeMux delegates to a custom `webhookRouter` with a mutable `map[path → hookEntry]`. Paths are registered/unregistered freely at runtime via `defer hooks.unregister(fullPath)` — no panics, no stale entries.

The channel in `hookEntry` is typed as `chan pipelineCtx` — the parsed webhook payload travels directly to the worker goroutine without any shared memory.

</details>

<details>
<summary><strong>Webhook Path Isolation</strong></summary>

<br/>

The `path` field in your JSON is a **suffix**, not the final path:

```
fullPath = "/" + workerID + cfg.Path
```

Two workers with `"path": "/hook/order"` get completely separate endpoints:

```
/a1b2c3d4-..../hook/order   ← Worker A
/e5f6a7b8-..../hook/order   ← Worker B
```

Guaranteed zero collision.

</details>

<details>
<summary><strong>Worker Modes & Running Flag Sync</strong></summary>

<br/>

The `running` field in the store is always kept in sync with the actual goroutine state:

| Event | `running` in store |
|-------|-------------------|
| `/run` or create with `running: true` | `true` |
| `once` — workflow completed naturally | `false` (auto-updated) |
| `/stop` called | `false` (auto-updated) |
| `loop` — running | always `true` |

`/list` always reflects the real runtime state.

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
| Webhook payload | `chan pipelineCtx` (buf 1) | Data delivered via channel, no shared memory |

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
  "id":      "a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d",
  "name":    "My Worker",
  "mode":    "loop",       // "once" | "loop"
  "running": true,
  "steps":   [...],
  "created": "2026-03-26T03:47:00Z",
  "updated": "2026-03-26T03:47:00Z"
}
```

---

## 🔧 Workflow Examples

### Dynamic AI via Webhook (loop)

```json
{
  "name": "AI Chatbot",
  "mode": "loop",
  "running": true,
  "steps": [
    { "type": "webhook",  "webhook": { "method": "POST", "path": "/ai" } },
    { "type": "log",      "log":     { "message": "Q: {{.usr}}" } },
    { "type": "call_api", "call_api": {
        "method": "POST",
        "url": "https://api.cloudflare.com/.../ai/run/@cf/meta/llama-3.1-8b-instruct-fast",
        "headers": { "Authorization": "Bearer TOKEN" },
        "body": {
          "messages": [
            { "role": "system", "content": "{{.sys}}" },
            { "role": "user",   "content": "{{.usr}}" }
          ]
        }
    }},
    { "type": "log", "log": { "message": "AI call done" } }
  ]
}
```

Trigger: `curl -X POST http://localhost:8080/<id>/ai -d '{"sys":"max 5 words","usr":"best food?"}'`

### Webhook → Notify Slack

```json
{
  "name": "Slack Notifier",
  "mode": "loop",
  "running": true,
  "steps": [
    { "type": "webhook",  "webhook":  { "method": "POST", "path": "/event" } },
    { "type": "log",      "log":      { "message": "Notifying Slack: {{.message}}" } },
    { "type": "call_api", "call_api": {
        "method": "POST",
        "url": "https://hooks.slack.com/services/XXX/YYY/ZZZ",
        "body": { "text": "{{.message}}" }
    }}
  ]
}
```

### Minimal — Log only (once)

```json
{
  "name": "Hello",
  "mode": "once",
  "running": true,
  "steps": [
    { "type": "log", "log": { "message": "Hello from worker!" } }
  ]
}
```

---

## 🖥️ curl Cheatsheet

```bash
ID="paste-your-uuid-here"

# Create (once)
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{"name":"test","mode":"once","running":true,"steps":[...]}'

# Create (loop) — CLI friendly single line
curl -X POST http://localhost:8080/create -H "Content-Type: application/json" -d '{"name":"test","mode":"loop","running":true,"steps":[...]}'

# Trigger webhook with pipeline data
curl -X POST http://localhost:8080/$ID/hook/order \
  -H "Content-Type: application/json" \
  -d '{"sys":"be brief","usr":"what is pizza?"}'

# List / Get
curl -s http://localhost:8080/list | jq
curl -s "http://localhost:8080/get?id=$ID" | jq

# Update
curl -X PUT http://localhost:8080/update \
  -H "Content-Type: application/json" \
  -d '{"id":"'$ID'","name":"updated","mode":"loop","running":true,"steps":[...]}'

# Run / Stop / Delete
curl -X POST   "http://localhost:8080/run?id=$ID"
curl -X POST   "http://localhost:8080/stop?id=$ID"
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
      ├──── POST /stop ──────────────► [ STOPPED ] running: false
      │                                     │
      │                               POST /run
      │                                     │
      ├──── mode=once, done ────────► [ IDLE ] running: false
      │                               POST /run to restart
      │
      └──── mode=loop, done ────────► auto restart → [ RUNNING ]
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
GOOS=windows GOARCH=amd64 go build -o worker-engine.exe main.go
GOOS=linux   GOARCH=amd64 go build -o worker-engine main.go
GOOS=darwin  GOARCH=arm64 go build -o worker-engine main.go
```

---

## 📦 Project Structure

```
.
├── main.go            # Entire application — single file
├── workers.gob        # Persistent store (auto-created on first run)
├── DOCUMENTATION.md   # In-depth technical documentation
└── README.md
```

---

## ⚠️ Limitations

- **Webhook waits for 1 request per step.** Use `mode: "loop"` for a permanent listener.
- **Templates read webhook body only.** `call_api` response is not yet in the pipeline.
- **No authentication** on API endpoints — add middleware before exposing to the internet.
- **Single process** — not designed for multi-instance or distributed deployment.
- **`call_api` body is JSON only** — `form-urlencoded` and `multipart` are not supported.
- **Storage is a flat file** — for very high write frequency, consider SQLite or BoltDB.

---

## 📄 License

```
MIT License — free to use, modify, and distribute.
```

---

<div align="center">

Built with pure Go · No dependencies · Cross-platform

</div>