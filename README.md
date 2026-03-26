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
**Webhook → Call API → Log** — with a full data pipeline, vars system, and zero `go get`.

<br/>

[**Quick Start**](#-quick-start) · [**Pipeline**](#-pipeline--data-flow) · [**Vars**](#-vars-system) · [**API**](#-api-reference) · [**Docs**](doc.md) · [**curl Guide**](curl.md)

</div>

---

## ✨ Features

| | Feature | Description |
|-|---------|-------------|
| 🪝 | **Webhook** | Accept `GET`/`POST` — request body flows into pipeline automatically |
| 🌐 | **Call API** | Hit external URLs — response body flows into pipeline for next steps |
| 🖨️ | **Log** | Print messages to stdout with `{{stepId.field}}` templates |
| 🔀 | **Data Pipeline** | Every step's output stored by ID, accessible in all subsequent steps |
| 📦 | **Vars System** | Multi-bucket static config — cross-reference between buckets freely |
| 🔁 | **Worker Modes** | `once` — run once; `loop` — restart automatically forever |
| 💾 | **Persistent** | Workers survive restarts via `gob` storage |
| ♻️ | **Auto-resume** | Running workers restart automatically on server startup |
| 🔒 | **No Collisions** | Webhook paths namespaced by worker UUID |

---

## 🚀 Quick Start

```bash
go run main.go
```

```bash
curl -X POST http://localhost:8080/create -H "Content-Type: application/json" -d '{
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
        "body": { "messages": [{ "role": "user", "content": "{{hook.question}}" }] }
    }},
    { "type": "log", "value": { "message": "Answer: {{ai.result.response}}" } }
  ]
}'
```

```bash
# Trigger — data flows through all steps
curl -X POST http://localhost:8080/<workerID>/ask \
  -H "Content-Type: application/json" \
  -d '{"question": "What is the best food?"}'
```

```
[worker:...] WEBHOOK triggered
[worker:...] CALL_API POST https://... → HTTP 200 | {"result":{"response":"Pizza!"}...}
[worker:...] LOG → Answer: Pizza!
[worker:...] workflow completed
[worker:...] mode=loop, restarting...
```

---

## 🔀 Pipeline & Data Flow

Every step that produces output stores it in the pipeline, keyed by its `id`. Subsequent steps can reference any previous step's output.

```
Step: webhook  (id="hook")
  ← POST body: {"sys":"answer briefly", "usr":"best food?"}
  → pipeline["hook"] = {"sys":"answer briefly", "usr":"best food?"}

Step: call_api  (id="ai")
  → body rendered: {{hook.sys}} → "answer briefly"
  ← response: {"result": {"response": "Pizza!"}, "success": true}
  → pipeline["ai"] = {"result": {"response": "Pizza!"}, "success": true}

Step: log
  → "{{ai.result.response}}"  renders to  "Pizza!"
```

### Template Syntax

| Syntax | Source | Example |
|--------|--------|---------|
| `{{stepId.field}}` | Step output by ID | `{{hook.username}}` |
| `{{stepId.a.b}}` | Nested dot-notation | `{{ai.result.response}}` |
| `{{bucket.key}}` | `vars["bucket"]["key"]` | `{{glb.api_url}}` |

> **There is no `{{.field}}`** — every reference must be explicit. This makes workflows unambiguous and easy to trace.

### call_api Response

```jsonc
// Response: {"result": {"response": "Pizza!"}, "success": true}
"{{ai.result.response}}"  →  "Pizza!"
"{{ai.success}}"          →  "true"

// Non-JSON response fallback:
"{{ai.raw}}"    →  raw string body
"{{ai.status}}" →  HTTP status code
```

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
  "env": { "timeout": "30", "region": "asia" }
}
```

`{{glb.api_url}}` resolves fully — `{{pvt.cf_id}}` inside it is substituted first. Buckets can reference each other, resolved iteratively. Bucket names are just conventions — there is no built-in privacy distinction.

---

## 📐 Architecture

```
┌──────────────────────────────────────────────────────────┐
│                    HTTP Server :8080                     │
│  ServeMux                                                │
│  ├─ /create /list /get /update /delete /run /stop        │
│  └─ /  ──────────────► webhookRouter  O(1) map lookup    │
│                          chan map[string]interface{}      │
├──────────────────────────────────────────────────────────┤
│  Runtime  (goroutine per worker)                         │
│  pipelineCtx: map[stepID → output]  goroutine-local      │
│  vars: resolved once per run, immutable during steps     │
├──────────────────────────────────────────────────────────┤
│  Store                                                   │
│  in-memory map + gob atomic write (CreateTemp→Rename)    │
└──────────────────────────────────────────────────────────┘
```

---

## ⚡ How It Works

<details>
<summary><strong>Data Pipeline — step outputs and how they flow</strong></summary>
<br/>

Each step that produces data stores it in `pipelineCtx.Steps[stepID]`. Only steps with a non-empty `id` are stored — steps without an `id` still execute, their output is just not accessible later.

- **webhook** → stores parsed request JSON body
- **call_api** → stores parsed JSON response body (or `{"raw":"...", "status":N}` for non-JSON)
- **log** → no output, purely a side effect

`pipelineCtx` is goroutine-local and reset each loop iteration — no data leaks between runs.

</details>

<details>
<summary><strong>Vars — iterative cross-bucket resolution</strong></summary>
<br/>

`vars` is `map[string]map[string]string`. Template resolution iterates up to 10 passes, processing all buckets simultaneously:

```
Pass 1: {{pvt.cf_id}} in glb.api_url resolved → "abc123"
Pass 2: {{glb.api_url}} in step url resolved → "https://.../abc123/ai/run/..."
```

Circular references are safe — the iteration cap prevents infinite loops.

</details>

<details>
<summary><strong>webhookRouter — dynamic O(1) dispatch</strong></summary>
<br/>

`http.ServeMux` panics on duplicate path registration — a problem when workers restart or update.

One `"/"` catch-all on ServeMux delegates to `webhookRouter` — a `sync.RWMutex`-protected map that is freely mutable at runtime. Each entry holds a `chan map[string]interface{}` (buffered, size 1). On request arrival, the body is parsed and sent directly through the channel. `defer hooks.unregister(fullPath)` ensures no stale entries.

Path: `/<workerID><path>` — collision-free by design.

</details>

<details>
<summary><strong>Storage — crash-safe atomic gob write</strong></summary>
<br/>

Serialization via `encoding/gob` (binary — faster and smaller than JSON). `Body` fields stored as `json.RawMessage` to avoid gob's limitation with `[]interface{}`.

```
encode → CreateTemp → Write → fsync → Rename (atomic on all platforms)
```

After startup, all reads come from in-memory `map[string]*Worker` — zero disk I/O at runtime.

</details>

<details>
<summary><strong>Running flag sync</strong></summary>
<br/>

`running` in the store is always kept in sync with the actual goroutine state:

| Event | `running` |
|-------|-----------|
| `/run` or create with `running: true` | `true` |
| `once` — completed naturally | `false` (auto) |
| `/stop` called | `false` (auto) |
| `loop` — any time | `true` |

`/list` always reflects the real runtime state.

</details>

---

## 📡 API Reference

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/create` | Create a new worker |
| `GET` | `/list` | List all workers |
| `GET` | `/get?id=<id>` | Get a single worker |
| `PUT` | `/update` | Full-replace (auto-restart if steps changed) |
| `DELETE` | `/delete?id=<id>` | Delete permanently |
| `POST` | `/run?id=<id>` | Start a stopped worker |
| `POST` | `/stop?id=<id>` | Stop a running worker |
| `ANY` | `/<workerID><path>` | Trigger a webhook step |

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
      ├── POST /stop ────────────► running: false
      ├── mode=once, done ───────► running: false  →  POST /run to restart
      └── mode=loop, done ───────► auto restart, running stays true
```

---

## 🏗️ Build

```bash
go run main.go
go build -o worker-engine main.go
go build -ldflags="-s -w" -o worker-engine main.go   # optimized

# Cross-compile
GOOS=windows GOARCH=amd64 go build -o worker-engine.exe main.go
GOOS=linux   GOARCH=amd64 go build -o worker-engine main.go
GOOS=darwin  GOARCH=arm64 go build -o worker-engine main.go
```

---

## 📦 Project Structure

```
.
├── main.go      # entire application — single file, zero dependencies
├── workers.gob  # persistent store (auto-created on first run)
├── doc.md       # in-depth technical documentation
├── curl.md      # curl examples for all endpoints
└── README.md
```

---

## ⚠️ Limitations

- **Webhook waits for 1 request per step.** `mode: "loop"` handles this automatically.
- **`{{.field}}` does not exist.** All references must be explicit.
- **`call_api` body is JSON only.** `form-urlencoded` / `multipart` not supported.
- **Vars are static.** Cannot change while worker is running.
- **Pipeline data is not persistent.** Lost when worker completes or restarts.
- **No authentication** on API endpoints.
- **Single process** — not designed for distributed deployment.

---

## 📄 License

```
MIT License — free to use, modify, and distribute.
```

---

<div align="center">

Built with pure Go · Zero dependencies · Windows / Linux / macOS

</div>