# Worker Engine — curl Demo: Branch / Percabangan

Demonstrasi semua pola percabangan menggunakan step type `"branch"`.

---

## Konsep Branch

```
"type": "branch"
"value": {
  "cases": [
    { "when": { "left": "{{step.field}}", "op": "==", "right": "nilai" }, "goto": "step_id" },
    { "when": { "left": "{{step.score}}", "op": ">=", "right": "90"    }, "goto": "step_id" },
    { }   ← else: tanpa "when", selalu cocok, goto opsional
  ]
}
```

**Op yang tersedia:** `==` `!=` `>` `<` `>=` `<=` `contains` `starts_with` `ends_with`

**Goto:** step ID tujuan. Kosong / tidak ada = lanjut sequential ke step berikutnya.

**Urutan evaluasi:** case pertama yang cocok menang (if → elseif → elseif → else).

---

## Pola 1 — if / elseif / else (string)

Webhook menerima `{"lang": "id"}` → branch ke log berbeda per bahasa.

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Branch: if-elseif-else",
  "mode": "loop",
  "running": true,
  "steps": [
    {
      "id": "hook",
      "type": "webhook",
      "value": { "method": "POST", "path": "/lang" }
    },
    {
      "id": "check_lang",
      "name": "cek bahasa",
      "type": "branch",
      "value": {
        "cases": [
          { "when": { "left": "{{hook.lang}}", "op": "==", "right": "id" }, "goto": "log_id" },
          { "when": { "left": "{{hook.lang}}", "op": "==", "right": "en" }, "goto": "log_en" },
          { "when": { "left": "{{hook.lang}}", "op": "==", "right": "jp" }, "goto": "log_jp" },
          { "goto": "log_unknown" }
        ]
      }
    },
    { "id": "log_id",      "type": "log", "value": { "message": "Halo! Bahasa: {{hook.lang}}" },     "next": "log_done" },
    { "id": "log_en",      "type": "log", "value": { "message": "Hello! Language: {{hook.lang}}" },   "next": "log_done" },
    { "id": "log_jp",      "type": "log", "value": { "message": "こんにちは！言語: {{hook.lang}}" },  "next": "log_done" },
    { "id": "log_unknown", "type": "log", "value": { "message": "Bahasa tidak dikenal: {{hook.lang}}" } },
    { "id": "log_done",    "type": "log", "value": { "message": "Selesai proses bahasa {{hook.lang}}" } }
  ]
}'
```

Trigger (ganti `WORKER_ID`):

```bash
curl -X POST http://localhost:8080/WORKER_ID/lang \
  -H "Content-Type: application/json" \
  -d '{"lang": "id"}'
```

```bash
curl -X POST http://localhost:8080/WORKER_ID/lang \
  -H "Content-Type: application/json" \
  -d '{"lang": "fr"}'
```

---

## Pola 2 — switch (numeric / skor)

Webhook menerima `{"score": 85}` → cabang berbeda per rentang nilai.

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Branch: numeric switch",
  "mode": "loop",
  "running": true,
  "steps": [
    {
      "id": "hook",
      "type": "webhook",
      "value": { "method": "POST", "path": "/score" }
    },
    {
      "id": "grade",
      "name": "tentukan grade",
      "type": "branch",
      "value": {
        "cases": [
          { "when": { "left": "{{hook.score}}", "op": ">=", "right": "90" }, "goto": "grade_a" },
          { "when": { "left": "{{hook.score}}", "op": ">=", "right": "75" }, "goto": "grade_b" },
          { "when": { "left": "{{hook.score}}", "op": ">=", "right": "60" }, "goto": "grade_c" },
          { "goto": "grade_d" }
        ]
      }
    },
    { "id": "grade_a", "type": "log", "value": { "message": "Score {{hook.score}} → Grade A (Sangat Baik)" }, "next": "selesai" },
    { "id": "grade_b", "type": "log", "value": { "message": "Score {{hook.score}} → Grade B (Baik)" },       "next": "selesai" },
    { "id": "grade_c", "type": "log", "value": { "message": "Score {{hook.score}} → Grade C (Cukup)" },      "next": "selesai" },
    { "id": "grade_d", "type": "log", "value": { "message": "Score {{hook.score}} → Grade D (Kurang)" } },
    { "id": "selesai", "type": "log", "value": { "message": "Evaluasi skor selesai." } }
  ]
}'
```

Trigger:

```bash
curl -X POST http://localhost:8080/WORKER_ID/score \
  -H "Content-Type: application/json" \
  -d '{"score": 92}'
```

```bash
curl -X POST http://localhost:8080/WORKER_ID/score \
  -H "Content-Type: application/json" \
  -d '{"score": 55}'
```

---

## Pola 3 — branch berdasarkan response API (contains)

Branch berdasarkan konten text dari respons AI / API eksternal.

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Branch: API response contains",
  "mode": "loop",
  "running": true,
  "vars": {
    "pvt": { "cf_id": "YOUR_CF_ACCOUNT_ID", "cf_auth": "YOUR_CF_TOKEN" },
    "glb": {
      "model":   "@cf/meta/llama-3.1-8b-instruct-fast",
      "api_url": "https://api.cloudflare.com/client/v4/accounts/{{pvt.cf_id}}/ai/run/{{glb.model}}"
    }
  },
  "steps": [
    {
      "id": "hook",
      "type": "webhook",
      "value": { "method": "POST", "path": "/classify" }
    },
    {
      "id": "ai",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url": "{{glb.api_url}}",
        "headers": { "Authorization": "Bearer {{pvt.cf_auth}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "Klasifikasikan pertanyaan berikut. Jawab HANYA dengan satu kata: FOOD, TECH, atau OTHER."
            },
            { "role": "user", "content": "{{hook.question}}" }
          ]
        }
      },
      "output": { "category": "{{ai.result.response}}" }
    },
    {
      "id": "route",
      "name": "routing kategori",
      "type": "branch",
      "value": {
        "cases": [
          { "when": { "left": "{{ai.category}}", "op": "contains", "right": "FOOD" }, "goto": "ans_food" },
          { "when": { "left": "{{ai.category}}", "op": "contains", "right": "TECH" }, "goto": "ans_tech" },
          { "goto": "ans_other" }
        ]
      }
    },
    { "id": "ans_food",  "type": "log", "value": { "message": "[FOOD] Q: {{hook.question}} | cat: {{ai.category}}" },  "next": "done" },
    { "id": "ans_tech",  "type": "log", "value": { "message": "[TECH] Q: {{hook.question}} | cat: {{ai.category}}" },  "next": "done" },
    { "id": "ans_other", "type": "log", "value": { "message": "[OTHER] Q: {{hook.question}} | cat: {{ai.category}}" } },
    { "id": "done",      "type": "log", "value": { "message": "Routing selesai untuk: {{hook.question}}" } }
  ]
}'
```

Trigger:

```bash
curl -X POST http://localhost:8080/WORKER_ID/classify \
  -H "Content-Type: application/json" \
  -d '{"question": "Resep nasi goreng terenak?"}'
```

```bash
curl -X POST http://localhost:8080/WORKER_ID/classify \
  -H "Content-Type: application/json" \
  -d '{"question": "Apa itu Kubernetes?"}'
```

---

## Pola 4 — branch berdasarkan success/gagal API

Cek `{{ai.success}}` true/false → handling error eksplisit.

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Branch: API success check",
  "mode": "loop",
  "running": true,
  "vars": {
    "pvt": { "cf_id": "YOUR_CF_ACCOUNT_ID", "cf_auth": "YOUR_CF_TOKEN" },
    "glb": {
      "model":   "@cf/meta/llama-3.1-8b-instruct-fast",
      "api_url": "https://api.cloudflare.com/client/v4/accounts/{{pvt.cf_id}}/ai/run/{{glb.model}}"
    }
  },
  "steps": [
    {
      "id": "hook",
      "type": "webhook",
      "value": { "method": "POST", "path": "/ask" }
    },
    {
      "id": "ai",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url": "{{glb.api_url}}",
        "headers": { "Authorization": "Bearer {{pvt.cf_auth}}" },
        "body": {
          "messages": [{ "role": "user", "content": "{{hook.question}}" }]
        }
      },
      "output": {
        "answer":  "{{ai.result.response}}",
        "success": "{{ai.success}}"
      }
    },
    {
      "id": "check_ok",
      "name": "cek hasil AI",
      "type": "branch",
      "value": {
        "cases": [
          { "when": { "left": "{{ai.success}}", "op": "==", "right": "true" }, "goto": "log_ok" },
          { "goto": "log_err" }
        ]
      }
    },
    { "id": "log_ok",  "type": "log", "value": { "message": "[OK]  Q: {{hook.question}} | A: {{ai.answer}}" }, "next": "done" },
    { "id": "log_err", "type": "log", "value": { "message": "[ERR] API gagal untuk: {{hook.question}}" } },
    { "id": "done",    "type": "log", "value": { "message": "Request selesai." } }
  ]
}'
```

Trigger:

```bash
curl -X POST http://localhost:8080/WORKER_ID/ask \
  -H "Content-Type: application/json" \
  -d '{"question": "Ibu kota Indonesia?"}'
```

---

## Ringkasan: Semua Operator

| Operator | Tipe | Contoh |
|---|---|---|
| `==` | string / numeric | `"left": "{{hook.lang}}", "op": "==", "right": "id"` |
| `!=` | string / numeric | `"left": "{{hook.status}}", "op": "!=", "right": "error"` |
| `>` | numeric | `"left": "{{hook.score}}", "op": ">", "right": "90"` |
| `<` | numeric | `"left": "{{hook.age}}", "op": "<", "right": "18"` |
| `>=` | numeric | `"left": "{{hook.score}}", "op": ">=", "right": "75"` |
| `<=` | numeric | `"left": "{{hook.score}}", "op": "<=", "right": "100"` |
| `contains` | string | `"left": "{{ai.result}}", "op": "contains", "right": "error"` |
| `starts_with` | string | `"left": "{{hook.url}}", "op": "starts_with", "right": "https"` |
| `ends_with` | string | `"left": "{{hook.file}}", "op": "ends_with", "right": ".pdf"` |

> **Note:** `>` `<` `>=` `<=` otomatis numeric jika kedua sisi bisa di-parse sebagai angka.
> Jika tidak bisa (misal membandingkan string), fallback ke string comparison (lexicographic).