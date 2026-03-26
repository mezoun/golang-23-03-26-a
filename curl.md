# Worker Engine — curl Examples

Panduan lengkap penggunaan semua endpoint dan fitur Worker Engine via `curl`.

---

## Daftar Isi

1. [CRUD Dasar](#1-crud-dasar)
2. [Run & Stop & Status](#2-run--stop--status)
3. [Step Types — Contoh Minimal](#3-step-types--contoh-minimal)
4. [Agentic AI: Resep Masakan](#4-agentic-ai-resep-masakan)
5. [Workflow dengan Retry & Sleep](#5-workflow-dengan-retry--sleep)
6. [Webhook Query Params & Headers](#6-webhook-query-params--headers)
7. [Tips & Troubleshooting](#7-tips--troubleshooting)

---

## 1. CRUD Dasar

### Create Worker (minimal)

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Hello Worker",
  "mode": "once",
  "steps": [
    { "type": "log", "value": { "message": "Hello, world!" } }
  ]
}'
```

### Create Worker + langsung jalankan

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Ping Worker",
  "mode": "loop",
  "loop_interval_ms": 2000,
  "running": true,
  "steps": [
    { "id": "ping", "type": "call_api", "value": { "method": "GET", "url": "https://api.ipify.org/?format=json" } },
    { "type": "log", "value": { "message": "My IP: {{ping.ip}}" } }
  ]
}'
```

### List semua worker

```bash
curl http://localhost:8080/list
```

### Get satu worker

```bash
curl "http://localhost:8080/get?id=WORKER_ID"
```

### Update worker (full replace)

```bash
curl -X PUT http://localhost:8080/update \
  -H "Content-Type: application/json" \
  -d '{
  "id": "WORKER_ID",
  "name": "Ping Worker v2",
  "mode": "loop",
  "loop_interval_ms": 5000,
  "running": true,
  "steps": [
    { "id": "ping", "type": "call_api", "value": { "method": "GET", "url": "https://api.ipify.org/?format=json" } },
    { "type": "log", "value": { "message": "IP: {{ping.ip}} — updated!" } }
  ]
}'
```

> Jika steps berubah, worker otomatis di-restart. Jika hanya vars yang berubah, worker terus berjalan dan membaca vars baru di iterasi berikutnya.

### Delete worker

```bash
curl -X DELETE "http://localhost:8080/delete?id=WORKER_ID"
```

---

## 2. Run & Stop & Status

### Start worker yang sedang berhenti

```bash
curl -X POST "http://localhost:8080/run?id=WORKER_ID"
```

### Stop worker yang sedang berjalan

```bash
curl -X POST "http://localhost:8080/stop?id=WORKER_ID"
```

### Status runtime (running, last error, run count)

```bash
curl "http://localhost:8080/status?id=WORKER_ID"
```

Response:
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

> `run_count` dan `last_error` reset saat server restart — data in-memory only.

---

## 3. Step Types — Contoh Minimal

### `webhook` — menunggu request masuk

```bash
# 1. Buat worker
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Webhook Demo",
  "mode": "once",
  "running": true,
  "steps": [
    { "id": "hook", "type": "webhook", "value": { "method": "POST", "path": "/data" } },
    { "type": "log", "value": { "message": "Got: {{hook.message}}" } }
  ]
}'

# 2. Trigger (ganti WORKER_ID)
curl -X POST http://localhost:8080/WORKER_ID/data \
  -H "Content-Type: application/json" \
  -d '{"message": "hello from trigger"}'
```

### `branch` — routing kondisional

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Branch Demo",
  "mode": "loop",
  "running": true,
  "steps": [
    { "id": "hook", "type": "webhook", "value": { "method": "POST", "path": "/check" } },
    {
      "id": "route",
      "type": "branch",
      "value": {
        "cases": [
          { "when": { "left": "{{hook.role}}", "op": "==", "right": "admin" }, "goto": "admin_log" },
          { "when": { "left": "{{hook.score}}", "op": ">=", "right": "80" }, "goto": "pass_log" },
          { "goto": "fail_log" }
        ]
      }
    },
    { "id": "admin_log", "type": "log", "value": { "message": "ADMIN: {{hook.name}}" }, "next": "end" },
    { "id": "pass_log",  "type": "log", "value": { "message": "PASS: {{hook.name}} ({{hook.score}})" }, "next": "end" },
    { "id": "fail_log",  "type": "log", "value": { "message": "FAIL: {{hook.name}}" }, "next": "end" },
    { "id": "end", "type": "log", "value": { "message": "Done." } }
  ]
}'
```

Trigger:
```bash
curl -X POST http://localhost:8080/WORKER_ID/check \
  -H "Content-Type: application/json" \
  -d '{"role": "admin", "name": "Alice", "score": "95"}'

curl -X POST http://localhost:8080/WORKER_ID/check \
  -H "Content-Type: application/json" \
  -d '{"role": "user", "name": "Bob", "score": "85"}'

curl -X POST http://localhost:8080/WORKER_ID/check \
  -H "Content-Type: application/json" \
  -d '{"role": "user", "name": "Charlie", "score": "55"}'
```

### `sleep` — delay eksplisit

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Sleep Demo",
  "mode": "once",
  "running": true,
  "steps": [
    { "type": "log", "value": { "message": "Mulai..." } },
    { "type": "sleep", "value": { "sleep_seconds": 3 } },
    { "type": "log", "value": { "message": "Selesai setelah 3 detik." } }
  ]
}'
```

### `set_var` — simpan nilai computed

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Set Var Demo",
  "mode": "once",
  "running": true,
  "steps": [
    { "id": "hook", "type": "webhook", "value": { "method": "POST", "path": "/input" } },
    {
      "id": "prep",
      "type": "set_var",
      "value": { "set_key": "greeting", "set_value": "Halo, {{hook.name}}! Skor kamu: {{hook.score}}" }
    },
    { "type": "log", "value": { "message": "{{prep.greeting}}" } }
  ]
}'
```

Trigger:
```bash
curl -X POST http://localhost:8080/WORKER_ID/input \
  -H "Content-Type: application/json" \
  -d '{"name": "Budi", "score": "95"}'
```

Log output: `Halo, Budi! Skor kamu: 95`

### `call_api` dengan retry

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Retry Demo",
  "mode": "once",
  "running": true,
  "steps": [
    {
      "id": "fetch",
      "type": "call_api",
      "value": {
        "method": "GET",
        "url": "https://httpbin.org/status/503",
        "retry": { "max": 3, "delay_ms": 500, "backoff": true }
      }
    },
    { "type": "log", "value": { "message": "Status: {{fetch._status}} | Error: {{fetch._error}} | Attempts: {{fetch._attempts}}" } }
  ]
}'
```

Delay sequence dengan backoff: 500ms → 1000ms → 2000ms.

---

## 4. Agentic AI: Resep Masakan

Worker ini menerima input masakan dalam 3 bentuk (**deskripsi**, **judul**, **bahan**), routing ke agent AI yang sesuai, lalu AI validator memastikan output JSON lengkap 5 field.

### Alur

```
webhook (input)
    │
    ▼
branch (detect_type)
    ├── type == "deskripsi"  ──► agent_deskripsi ──┐
    ├── type == "judul"      ──► agent_judul      ──┤  output: agent_raw
    ├── type == "bahan"      ──► agent_bahan      ──┘
    └── else                 ──► log_undefined ──► end
                                                    │
                                                    ▼
                                              parse_result (validator AI)
                                                    │
                                                    ▼
                                              log_result → end
```

### CREATE WORKER

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Agentic AI: Resep Masakan",
  "mode": "loop",
  "running": true,
  "vars": {
    "glb": {
      "ai_model": "@cf/meta/llama-3.1-8b-instruct-fast",
      "api_url": "https://api.cloudflare.com/client/v4/accounts/{{pvt.cf_id}}/ai/run/{{glb.ai_model}}"
    },
    "pvt": {
      "cf_id": "GANTI_CF_ACCOUNT_ID",
      "cf_auth": "GANTI_CF_API_TOKEN"
    }
  },
  "steps": [
    {
      "id": "input",
      "name": "Terima Input Pengguna",
      "type": "webhook",
      "value": { "method": "POST", "path": "/masakan" }
    },
    {
      "id": "detect_type",
      "name": "Deteksi Tipe Input",
      "type": "branch",
      "value": {
        "cases": [
          { "when": { "left": "{{input.type}}", "op": "==", "right": "deskripsi" }, "goto": "agent_deskripsi" },
          { "when": { "left": "{{input.type}}", "op": "==", "right": "judul"     }, "goto": "agent_judul"     },
          { "when": { "left": "{{input.type}}", "op": "==", "right": "bahan"     }, "goto": "agent_bahan"     },
          { "goto": "log_undefined" }
        ]
      }
    },
    {
      "id": "agent_deskripsi",
      "name": "Agent AI — dari Deskripsi",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url": "{{glb.api_url}}",
        "headers": { "Authorization": "Bearer {{pvt.cf_auth}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "Kamu adalah chef AI. Pengguna memberi DESKRIPSI masakan. Buat resep lengkap. WAJIB jawab HANYA JSON valid tanpa teks tambahan. Format: {\"judul\":\"...\",\"deskripsi\":\"...\",\"bahan\":\"...\",\"cara_masak\":\"...\",\"saran_penyajian\":\"...\"}"
            },
            { "role": "user", "content": "Buat resep dari deskripsi: {{json:input.data}}" }
          ]
        },
        "retry": { "max": 2, "delay_ms": 1000 }
      },
      "output": { "agent_raw": "{{agent_deskripsi.result.response}}" },
      "next": "parse_result"
    },
    {
      "id": "agent_judul",
      "name": "Agent AI — dari Judul",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url": "{{glb.api_url}}",
        "headers": { "Authorization": "Bearer {{pvt.cf_auth}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "Kamu adalah chef AI. Pengguna memberi NAMA masakan. Buat resep lengkap. WAJIB jawab HANYA JSON valid tanpa teks tambahan. Format: {\"judul\":\"...\",\"deskripsi\":\"...\",\"bahan\":\"...\",\"cara_masak\":\"...\",\"saran_penyajian\":\"...\"}"
            },
            { "role": "user", "content": "Buat resep untuk masakan: {{json:input.data}}" }
          ]
        },
        "retry": { "max": 2, "delay_ms": 1000 }
      },
      "output": { "agent_raw": "{{agent_judul.result.response}}" },
      "next": "parse_result"
    },
    {
      "id": "agent_bahan",
      "name": "Agent AI — dari Bahan",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url": "{{glb.api_url}}",
        "headers": { "Authorization": "Bearer {{pvt.cf_auth}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "Kamu adalah chef AI. Pengguna memberi daftar BAHAN. Rekomendasikan dan buat resep terbaik. WAJIB jawab HANYA JSON valid tanpa teks tambahan. Format: {\"judul\":\"...\",\"deskripsi\":\"...\",\"bahan\":\"...\",\"cara_masak\":\"...\",\"saran_penyajian\":\"...\"}"
            },
            { "role": "user", "content": "Bahan yang saya punya: {{json:input.data}}. Buat resep dari bahan ini." }
          ]
        },
        "retry": { "max": 2, "delay_ms": 1000 }
      },
      "output": { "agent_raw": "{{agent_bahan.result.response}}" },
      "next": "parse_result"
    },
    {
      "id": "log_undefined",
      "name": "Tipe Tidak Dikenal",
      "type": "log",
      "value": {
        "message": "[UNDEFINED] Tipe: \"{{input.type}}\" | data: \"{{input.data}}\" | Gunakan: deskripsi / judul / bahan"
      },
      "next": "end"
    },
    {
      "id": "parse_result",
      "name": "Agent AI — Validator JSON",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url": "{{glb.api_url}}",
        "headers": { "Authorization": "Bearer {{pvt.cf_auth}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "Kamu adalah JSON validator. Input adalah teks yang seharusnya berisi JSON resep. Pastikan output JSON valid dengan tepat 5 field: judul, deskripsi, bahan, cara_masak, saran_penyajian. Jika field hilang tambahkan. Buang semua teks di luar JSON. WAJIB jawab HANYA JSON murni TANPA markdown TANPA backtick."
            },
            {
              "role": "user",
              "content": "Validasi JSON ini: {{agent_deskripsi.agent_raw}}{{agent_judul.agent_raw}}{{agent_bahan.agent_raw}}"
            }
          ]
        },
        "retry": { "max": 2, "delay_ms": 1000 }
      },
      "output": { "final": "{{parse_result.result.response}}" },
      "next": "log_result"
    },
    {
      "id": "log_result",
      "name": "Output Akhir",
      "type": "log",
      "value": {
        "message": "[RESEP] type={{input.type}} | input={{input.data}} | result={{parse_result.final}}"
      },
      "next": "end"
    },
    {
      "id": "end",
      "name": "Selesai",
      "type": "log",
      "value": { "message": "[END] Menunggu input berikutnya..." }
    }
  ]
}'
```

> Catat `"id"` dari response JSON → pakai sebagai `WORKER_ID` di bawah.

### TRIGGER WEBHOOK

#### Test 1 — Input `deskripsi`

```bash
curl -X POST http://localhost:8080/WORKER_ID/masakan \
  -H "Content-Type: application/json" \
  -d '{
  "type": "deskripsi",
  "data": "Masakan pedas manis dengan protein tinggi, cocok untuk sarapan, menggunakan telur dan cabai"
}'
```

#### Test 2 — Input `judul`

```bash
curl -X POST http://localhost:8080/WORKER_ID/masakan \
  -H "Content-Type: application/json" \
  -d '{
  "type": "judul",
  "data": "Rendang Daging Sapi"
}'
```

#### Test 3 — Input `bahan`

```bash
curl -X POST http://localhost:8080/WORKER_ID/masakan \
  -H "Content-Type: application/json" \
  -d '{
  "type": "bahan",
  "data": "ayam kampung, santan, serai, daun salam, lengkuas, bawang merah, bawang putih, kemiri, kunyit"
}'
```

#### Test 4 — Tipe undefined (edge case)

```bash
curl -X POST http://localhost:8080/WORKER_ID/masakan \
  -H "Content-Type: application/json" \
  -d '{
  "type": "foto",
  "data": "gambar nasi goreng"
}'
```

Expected log:
```
[UNDEFINED] Tipe: "foto" | data: "gambar nasi goreng" | Gunakan: deskripsi / judul / bahan
```

### Expected Log (sukses — judul)

```
[worker:ID][step:Deteksi Tipe Input] BRANCH case[1] match ("judul" == "judul") → goto "agent_judul"
[worker:ID][step:Agent AI — dari Judul] CALL_API POST https://api.cloudflare.com/... → HTTP 200 | {"result":{"response":"{\"judul\":\"Rendang..."}...}
[worker:ID][step:Agent AI — Validator JSON] CALL_API POST ... → HTTP 200 | ...
[worker:ID][step:Output Akhir] LOG → [RESEP] type=judul | input=Rendang Daging Sapi | result={"judul":"Rendang Daging Sapi",...}
[worker:ID][step:Selesai] LOG → [END] Menunggu input berikutnya...
```

### Catatan: `{{json:input.data}}` vs `{{input.data}}`

Gunakan `{{json:input.data}}` saat menginject nilai user ke dalam string JSON body. Jika `input.data` berisi tanda kutip atau karakter spesial (misalnya `He said "yes"`), tanpa `json:` akan menghasilkan JSON malformed dan call_api gagal dengan HTTP 400.

---

## 5. Workflow dengan Retry & Sleep

Worker polling API setiap 10 detik dengan retry otomatis:

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Polling Worker",
  "mode": "loop",
  "loop_interval_ms": 10000,
  "running": true,
  "vars": {
    "cfg": { "api_url": "https://httpbin.org/get" }
  },
  "steps": [
    {
      "id": "fetch",
      "type": "call_api",
      "value": {
        "method": "GET",
        "url": "{{cfg.api_url}}",
        "retry": { "max": 3, "delay_ms": 2000, "backoff": true }
      }
    },
    { "type": "log", "value": { "message": "Status: {{fetch._status}} | Origin: {{fetch.origin}}" } },
    { "type": "sleep", "value": { "sleep_ms": 500 } },
    { "type": "log", "value": { "message": "Loop selesai, menunggu 10 detik..." } }
  ]
}'
```

---

## 6. Webhook Query Params & Headers

Worker yang membaca query param dan custom header:

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Query Header Demo",
  "mode": "loop",
  "running": true,
  "steps": [
    { "id": "hook", "type": "webhook", "value": { "method": "POST", "path": "/api" } },
    { "type": "log", "value": {
        "message": "body.action={{hook.action}} | query.version={{hook._query.version}} | header.X-Client={{hook._headers.X-Client}}"
    }}
  ]
}'
```

Trigger dengan query param dan custom header:

```bash
curl -X POST "http://localhost:8080/WORKER_ID/api?version=2&lang=id" \
  -H "Content-Type: application/json" \
  -H "X-Client: myapp-v2" \
  -d '{"action": "search", "keyword": "rendang"}'
```

Expected log:
```
body.action=search | query.version=2 | header.X-Client=myapp-v2
```

---

## 7. Tips & Troubleshooting

### HTTP 429 saat trigger webhook

Worker masih memproses request sebelumnya. Tunggu sebentar lalu coba lagi. Untuk mode `loop`, ini normal jika workflow panjang dan request datang terlalu cepat.

```bash
# Cek apakah worker masih running
curl "http://localhost:8080/status?id=WORKER_ID"
```

### Worker berhenti mendadak

Cek `/status` untuk melihat last error:

```bash
curl "http://localhost:8080/status?id=WORKER_ID"
# last_run_ok: false → ada error
# last_error: "PANIC: ..." atau pesan error lain
```

Worker yang panic otomatis berhenti (`running: false`) dan error tersimpan. Perbaiki konfigurasi lalu jalankan lagi dengan `/run`.

### HTTP 503 saat create/run

Limit sistem tercapai:
- `max concurrent workers reached` → stop worker yang tidak diperlukan dulu
- `max stored workers reached` → hapus worker lama

### JSON body di call_api rusak / HTTP 400

Kemungkinan nilai template mengandung karakter spesial. Gunakan `{{json:step.field}}`:

```json
"body": { "message": "{{json:hook.user_text}}" }
```

### Update vars tanpa restart worker

```bash
curl -X PUT http://localhost:8080/update \
  -H "Content-Type: application/json" \
  -d '{
  "id": "WORKER_ID",
  "name": "Worker Name",
  "mode": "loop",
  "running": true,
  "vars": {
    "pvt": { "token": "NEW_TOKEN_HERE" }
  },
  "steps": [ ... ]
}'
```

Vars baru efektif di iterasi loop berikutnya tanpa restart goroutine.

### Cek semua worker sekaligus

```bash
curl http://localhost:8080/list | python3 -m json.tool
# atau jq jika tersedia:
curl http://localhost:8080/list | jq '.[] | {id, name, running, mode}'
```