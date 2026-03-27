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
**Webhook → Branch → Call API → Log** — with full pipeline, vars, retry, sleep, file manager, and zero `go get`.

<br/>

[**Quick Start**](#-quick-start) · [**Pipeline**](#-pipeline--data-flow) · [**Step Types**](#-step-types) · [**Vars**](#-vars-system) · [**File Manager**](#-file-manager) · [**API**](#-api-reference) · [**Docs**](doc.md) · [**curl Guide**](curl.md)

</div>

---

## ✨ Features

| | Feature | Description |
|-|---------|-------------|
| 🪝 | **Webhook** | Accept `GET`/`POST` — body, query params, and headers flow into pipeline |
| 🌐 | **Call API** | Hit external URLs — response flows into pipeline; built-in retry & backoff |
| 🖨️ | **Log** | Print messages to per-worker log file with `{{stepId.field}}` templates |
| 🔀 | **Branch** | Conditional routing — `==` `!=` `>` `<` `>=` `<=` `contains` `starts_with` `ends_with` |
| ⏱️ | **Sleep** | Explicit delay up to 60s — cancellable, for rate limiting and backoff |
| 📝 | **Set Var** | Store computed values inline — no API call needed for data transforms |
| 🔄 | **Data Pipeline** | Every step output stored by ID, accessible via `{{stepId.field}}` |
| 📦 | **Vars System** | Multi-bucket static config — cross-reference between buckets freely |
| 🔁 | **Worker Modes** | `once` — run once; `loop` — restart automatically with configurable interval |
| 💾 | **Persistent** | Workers survive restarts via `gob` storage with atomic crash-safe write |
| ♻️ | **Auto-resume** | Running workers restart automatically on boot with staggered delay |
| 🛡️ | **Isolated** | Each worker in its own goroutine — panic in one never affects others |
| 📊 | **Status API** | `GET /status` — runtime info, run count, last error, without reading logs |
| 🚦 | **Multi-tenant** | Hard limits: 50 concurrent · 500 stored · 100 steps · 20 API calls |
| 📁 | **File Manager** | Upload, list, view, delete, and agent-save files via `/files/*` endpoints |
| 📋 | **Per-Worker Logs** | Buffered log file per worker at `log/{workerID}.log` — 32 KB buffer, flush every 3s |

---

## 🚀 Quick Start

```bash
go run main.go
```

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "AI Worker",
  "mode": "loop",
  "running": true,
  "vars": {
    "pvt": { "cf_id": "YOUR_ID", "token": "YOUR_TOKEN" },
    "glb": {
      "model": "@cf/meta/llama-3.1-8b-instruct-fast",
      "url": "https://api.cloudflare.com/client/v4/accounts/{{pvt.cf_id}}/ai/run/{{glb.model}}"
    }
  },
  "steps": [
    { "id": "hook", "type": "webhook", "value": { "method": "POST", "path": "/ask" } },
    { "id": "ai",   "type": "call_api", "value": {
        "method": "POST",
        "url": "{{glb.url}}",
        "headers": { "Authorization": "Bearer {{pvt.token}}" },
        "body": { "messages": [{ "role": "user", "content": "{{hook.question}}" }] },
        "retry": { "max": 3, "delay_ms": 500, "backoff": true }
    }},
    { "type": "log", "value": { "message": "Answer: {{ai.result.response}}" } }
  ]
}'
```

```bash
curl -X POST http://localhost:8080/<workerID>/ask \
  -H "Content-Type: application/json" \
  -d '{"question": "What is the best food?"}'
```

```
[worker:...] WEBHOOK triggered
[worker:...] CALL_API POST https://... → HTTP 200 | {"result":{"response":"Pizza!"}...}
[worker:...] LOG → Answer: Pizza!
[worker:...] workflow completed
```

> Semua log tersimpan di `log/<workerID>.log` — file per worker, tidak hanya stdout.

---

## 🔀 Pipeline & Data Flow

Every step that produces output stores it by `id`. All subsequent steps can reference any previous step's output.

```
Step: webhook  (id="hook")
  ← POST body: {"question": "best food?"}
  → pipeline["hook"] = {"question":"best food?", "_query":{...}, "_headers":{...}}

Step: call_api  (id="ai")
  → body rendered: {{hook.question}} → "best food?"
  ← response: {"result": {"response": "Pizza!"}, "success": true}
  → pipeline["ai"] = {"result": {"response": "Pizza!"}, "success": true}

Step: log
  → "{{ai.result.response}}" renders to "Pizza!"
```

### Template Syntax

| Syntax | Source | Example |
|--------|--------|---------|
| `{{stepId.field}}` | Step output by ID | `{{hook.username}}` |
| `{{stepId.a.b.c}}` | Nested dot-notation | `{{ai.result.response}}` |
| `{{bucket.key}}` | `vars["bucket"]["key"]` | `{{glb.api_url}}` |
| `{{json:stepId.field}}` | JSON-safe escaped value | `{{json:hook.message}}` |

> Use `{{json:step.field}}` when injecting a value that may contain quotes, newlines, or backslashes **inside a JSON body string**. This prevents malformed JSON.

### Webhook — Auto Fields

Every webhook step automatically exposes:

| Field | Content |
|-------|---------|
| `{{hookId.field}}` | Parsed JSON body fields |
| `{{hookId._query.param}}` | URL query parameters |
| `{{hookId._headers.X-Custom}}` | Request headers (Authorization/Cookie filtered) |

---

## 🧩 Step Types

### `webhook` — Wait for HTTP request

```json
{
  "id": "hook",
  "type": "webhook",
  "value": { "method": "POST", "path": "/trigger" }
}
```

Blocks until a request arrives. Path is scoped to worker: `/<workerID>/trigger`.  
Returns HTTP 429 `{"error":"worker busy, retry later"}` if the worker is still processing the previous request.

---

### `call_api` — HTTP request to external URL

```json
{
  "id": "result",
  "type": "call_api",
  "value": {
    "method": "POST",
    "url": "{{glb.api_url}}",
    "headers": { "Authorization": "Bearer {{pvt.token}}" },
    "body": { "prompt": "{{hook.text}}" },
    "retry": { "max": 3, "delay_ms": 500, "backoff": true }
  }
}
```

**Retry config** (optional):

| Field | Type | Description |
|-------|------|-------------|
| `max` | int | Max retry attempts (hard limit: 5) |
| `delay_ms` | int | Initial delay between retries in ms |
| `backoff` | bool | Exponential backoff if true |

Retries on: network error, HTTP 429, HTTP 5xx. On exhaustion, output contains `_error` and `_attempts`.  
`_status` (HTTP status code) is always present in the output.

---

### `log` — Write to per-worker log file

```json
{ "type": "log", "value": { "message": "User: {{hook.user}} | Score: {{score.value}}" } }
```

Output is written to `log/<workerID>.log` with ISO-8601 timestamp prefix.

---

### `branch` — Conditional routing

```json
{
  "id": "check",
  "type": "branch",
  "value": {
    "cases": [
      { "when": { "left": "{{hook.type}}", "op": "==", "right": "admin" }, "goto": "admin_flow" },
      { "when": { "left": "{{score.value}}", "op": ">", "right": "90" },   "goto": "high_score" },
      { "goto": "default_flow" }
    ]
  }
}
```

Cases are evaluated in order — first match wins. A case without `when` is an `else` (always matches).  
`goto` is a step `id`. Empty `goto` means continue sequentially.

**Operators:** `==` `!=` `>` `<` `>=` `<=` `contains` `starts_with` `ends_with`

---

### `sleep` — Explicit delay

```json
{ "type": "sleep", "value": { "sleep_ms": 500 } }
```

Or in seconds:
```json
{ "type": "sleep", "value": { "sleep_seconds": 2 } }
```

Maximum 60 seconds. Cancellable — stops immediately when worker is stopped.

---

### `set_var` — Store computed value into pipeline

```json
{
  "id": "prepared",
  "type": "set_var",
  "value": {
    "set_key": "full_prompt",
    "set_value": "Context: {{hook.context}} | Question: {{hook.question}}"
  }
}
```

Stores `full_prompt` in `pipeline["prepared"]`. Access later as `{{prepared.full_prompt}}`.  
Use this to transform, concat, or rename fields between steps — no API call needed.

---

### `next` field — Force jump

Any step can have a `next` field to unconditionally jump to another step after execution:

```json
{ "id": "agent_a", "type": "call_api", "value": { ... }, "next": "merge_results" }
```

If `branch` already jumped, `next` is ignored. Jump limit: 1000 per run (protects against infinite loops).

---

## 📦 Vars System

Static configuration in named buckets — cross-reference freely between buckets.

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
  "env": { "region": "asia" }
}
```

Buckets can reference each other — resolved iteratively up to 20 passes.  
Vars are read fresh from store on each loop iteration, so updating vars via `/update` takes effect on the next run without restarting the worker.

**Limits:** max 20 buckets, max 50 keys per bucket.

---

## 📁 File Manager

Worker Engine menyediakan file manager sederhana berbasis HTTP untuk menyimpan, mengelola, dan mengakses file output dari worker/agent. Semua file disimpan di folder `output/`.

### `POST /files/upload` — Upload file

Upload file via `multipart/form-data`, field name `file`. Maksimum 10 MB per file.

```bash
curl -X POST http://localhost:8080/files/upload \
     -F "file=@/path/to/yourfile.txt"
```

**Response (HTTP 201):**
```json
{ "name": "yourfile.txt", "size_bytes": 1234, "path": "output/yourfile.txt" }
```

---

### `GET /files/list` — List semua file

```bash
curl http://localhost:8080/files/list
```

**Response:**
```json
[
  { "name": "report.json", "size_bytes": 2048, "modified": "2026-03-26T14:30:00Z" },
  { "name": "agent-recipe_20260326-143022_a1b2c3d4.json", "size_bytes": 3821, "modified": "2026-03-26T14:30:22Z" }
]
```

---

### `GET /files/view?name=<filename>` — Download / tampilkan file

```bash
# Download otomatis (nama file sesuai server)
curl -O -J "http://localhost:8080/files/view?name=report.json"

# Tampilkan ke stdout
curl "http://localhost:8080/files/view?name=report.json"
```

---

### `DELETE /files/delete?name=<filename>` — Hapus file

```bash
curl -X DELETE "http://localhost:8080/files/delete?name=report.json"
```

**Response:**
```json
{ "deleted": "report.json" }
```

---

### `POST /files/save` — Simpan output agent

Endpoint ini dirancang untuk dipanggil dari dalam pipeline worker via step `call_api`. Nama file di-generate otomatis dengan format `agent-{tag}_{YYYYMMDD-HHMMSS}_{rand8hex}.json`.

```bash
curl -X POST http://localhost:8080/files/save \
     -H "Content-Type: application/json" \
     -d '{"tag": "recipe", "content": "{\"title\":\"Rendang\"}"}'
```

**Response (HTTP 201):**
```json
{
  "name": "agent-recipe_20260326-143022_a1b2c3d4.json",
  "size_bytes": 25,
  "view_url": "/files/view?name=agent-recipe_20260326-143022_a1b2c3d4.json"
}
```

| Field | Keterangan |
|-------|-----------|
| `tag` | Label opsional — muncul di nama file; hanya huruf, angka, dash (maks 32 karakter) |
| `content` | Isi file — wajib, tidak boleh kosong (maks 5 MB) |

> `tag` kosong menghasilkan nama: `agent_{YYYYMMDD-HHMMSS}_{rand8hex}.json`

---

## 📐 Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                    HTTP Server :8080                         │
│  ServeMux                                                    │
│  ├─ /create /list /get /status /update /delete /run /stop    │
│  ├─ /files/upload /files/list /files/view                    │
│  ├─ /files/delete /files/save  ──► File Manager (output/)   │
│  └─ /  ──────────────────────► webhookRouter  O(1) map       │
│                                  → HTTP 429 if worker busy   │
├──────────────────────────────────────────────────────────────┤
│  Runtime  (goroutine per worker, recover() per goroutine)    │
│  Global semaphore: max 20 concurrent call_api                │
│  pipelineCtx: goroutine-local, reset each loop               │
│  Vars snapshot: refreshed each iteration (live update)       │
├──────────────────────────────────────────────────────────────┤
│  LogManager  (buffered per-worker log)                       │
│  log/{workerID}.log — 32 KB buffer, flush setiap 3 detik    │
│  Lazy open file handle — ditutup saat worker berhenti        │
├──────────────────────────────────────────────────────────────┤
│  Store                                                       │
│  in-memory map + snapshot copy + gob atomic write            │
│  flush lock released before encode — no contention           │
└──────────────────────────────────────────────────────────────┘
```

---

## 🏗️ System Limits

All limits are constants at the top of `main.go` — change to suit your needs.

| Limit | Value | Description |
|-------|-------|-------------|
| `maxConcurrentWorkers` | 50 | Max workers running at the same time |
| `maxStoredWorkers` | 500 | Max workers in the store |
| `maxStepsPerWorker` | 100 | Max steps per worker |
| `maxConcurrentCalls` | 20 | Max simultaneous `call_api` HTTP requests |
| `maxVarsBuckets` | 20 | Max buckets in `vars` |
| `maxVarsPerBucket` | 50 | Max keys per bucket |
| `maxBodyBytes` | 128 KB | Max request body for `/create`, `/update` |
| `maxWebhookBytes` | 64 KB | Max webhook body |
| `maxRetryCount` | 5 | Max retry attempts for `call_api` |
| `maxSleepMs` | 60 000 | Max sleep duration (60s) |
| `minLoopIntervalMs` | 200 | Minimum loop interval |
| `defaultLoopIntervalMs` | 500 | Default loop interval |
| `maxUploadBytes` | 10 MB | Max file size for `/files/upload` |
| `maxSaveBytes` | 5 MB | Max content size for `/files/save` |
| `maxOutputFiles` | 200 | Max files in `output/` folder |
| `jumpLimit` | 1 000 | Max branch/next jumps per run |

---

## ⚡ Worker Loop Interval

Control how fast a `loop` worker restarts:

```json
{ "loop_interval_ms": 1000 }
```

Minimum: 200ms. Default: 500ms. Set to `0` to use the default.

---

## 📡 API Reference

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/create` | Create a new worker |
| `GET` | `/list` | List all workers |
| `GET` | `/get?id=<id>` | Get a single worker |
| `GET` | `/status?id=<id>` | Get runtime status (running, last error, run count) |
| `PUT` | `/update` | Full-replace a worker (auto-restart if steps changed) |
| `DELETE` | `/delete?id=<id>` | Delete permanently |
| `POST` | `/run?id=<id>` | Start a stopped worker |
| `POST` | `/stop?id=<id>` | Stop a running worker |
| `POST` | `/files/upload` | Upload file ke `output/` (multipart/form-data) |
| `GET` | `/files/list` | List semua file di `output/` |
| `GET` | `/files/view?name=<f>` | Download / tampilkan file |
| `DELETE` | `/files/delete?name=<f>` | Hapus file dari `output/` |
| `POST` | `/files/save` | Simpan output agent dengan nama auto-generated |
| `ANY` | `/<workerID><path>` | Trigger a webhook step |

All `POST`/`PUT` management endpoints require `Content-Type: application/json`.

### `GET /status?id=<id>` Response

```json
{
  "id": "a3f2c1d4-...",
  "running": true,
  "last_run_at": "2026-03-26T10:00:00Z",
  "last_run_ok": true,
  "last_error": "",
  "run_count": 42
}
```

Status is in-memory only — resets on server restart.

---

## 🔄 Worker Lifecycle

```
POST /create
      │
      ▼
  [ STORED ]  running: false
      │
      │ running:true / POST /run
      ▼
  [ RUNNING ]
      │
      ├── POST /stop ─────────────► running: false
      ├── mode=once, complete ────► running: false  →  POST /run to restart
      ├── mode=loop, complete ────► auto restart, running stays true
      └── panic / fatal error ────► running: false (auto), error in /status
```

---

## 🛡️ Reliability & Isolation

- **Panic recovery** — each worker goroutine has `recover()`. A panic sets `running: false`, stores the error in `/status`, and frees the slot. Other workers are unaffected.
- **Graceful shutdown** — `SIGTERM`/`Ctrl+C` stops all workers, flushes the store and all log files, then exits cleanly.
- **Staggered boot** — workers with `running: true` start with a random 10–50ms × index delay to prevent thundering herd on external APIs.
- **Live vars** — vars are read from the store at each loop iteration, so you can update vars via `/update` without restarting the worker.
- **Crash-safe storage** — `CreateTemp → fsync → Rename` ensures `workers.gob` is never corrupted on crash.
- **Buffered logs** — `LogManager` keeps a 32 KB buffer per worker and flushes every 3 seconds or on worker stop, minimizing disk I/O.

---

## 🏗️ Build

```bash
go run main.go
go build -o worker-engine main.go
go build -ldflags="-s -w" -o worker-engine main.go

# Cross-compile
GOOS=windows GOARCH=amd64 go build -o worker-engine.exe main.go
GOOS=linux   GOARCH=amd64 go build -o worker-engine main.go
GOOS=darwin  GOARCH=arm64 go build -o worker-engine main.go
```

---

## 📦 Project Structure

```
.
├── main.go        # entire application — single file, zero dependencies
├── workers.gob    # persistent store (auto-created on first run)
├── log/           # per-worker log files (auto-created)
│   └── <workerID>.log
├── output/        # file manager output folder (auto-created)
│   └── agent-recipe_20260326-143022_a1b2c3d4.json
├── doc.md         # in-depth technical documentation
├── curl.md        # curl examples for all endpoints
└── README.md
```

---

## ⚠️ Limitations

- **Webhook waits for 1 request per step.** `mode: "loop"` handles this automatically. Returns HTTP 429 if the worker is still busy with the previous request.
- **`call_api` body is JSON only.** `form-urlencoded` / `multipart` not supported.
- **Pipeline data is not persistent.** Lost when worker completes or restarts. Only vars and store data persist.
- **Status data is in-memory.** `run_count`, `last_error`, `last_run_at` reset on server restart.
- **Log files are local filesystem only.** No HTTP endpoint to read log files — access directly via filesystem.
- **No authentication** on API endpoints.
- **Single process** — not designed for distributed deployment.

---

<div align="center">

Built with pure Go · Zero dependencies · Windows / Linux / macOS

</div>