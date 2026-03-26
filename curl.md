# curl Guide — Worker Engine

Base URL: `http://localhost:8080`

> Semua body JSON harus satu baris untuk CLI compatibility (gunakan format di bawah).

---

## Create Worker

```bash
curl -X POST http://localhost:8080/create -H "Content-Type: application/json" -d '{
  "name": "AI Food Worker",
  "mode": "loop",
  "running": true,
  "vars": {
    "glb": {
      "ai_model": "@cf/meta/llama-3.1-8b-instruct-fast",
      "api_url": "https://api.cloudflare.com/client/v4/accounts/{{pvt.cf_id}}/ai/run/{{glb.ai_model}}"
    },
    "pvt": {
      "cf_id": "56667d302adadb8e04093b1aac32017c",
      "cf_auth": "cfut_KpdkVf3ZkIArmKDoBLQySa2TA4Pb5MXY4ZkXA7Qbe21abbbf"
    }
  },
  "steps": [
    {
      "id": "hook",
      "name": "terima request",
      "type": "webhook",
      "value": { "method": "POST", "path": "/food" }
    },
    {
      "name": "log input",
      "type": "log",
      "value": { "message": "sys={{hook.sys}} usr={{hook.usr}}" }
    },
    {
      "id": "ai",
      "name": "tanya AI",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url": "{{glb.api_url}}",
        "headers": { "Authorization": "Bearer {{pvt.cf_auth}}" },
        "body": {
          "messages": [
            { "role": "system", "content": "{{hook.sys}}" },
            { "role": "user",   "content": "{{hook.usr}}" }
          ]
        }
      }
    },
    {
      "name": "log result",
      "type": "log",
      "value": { "message": "AI jawab: {{ai.result.response}}" }
    }
  ]
}'
```

Simpan `id` dari response, contoh: `a3f2c1d4-e5b6-4a7f-8c9d-0e1f2a3b4c5d`

---

## Trigger Webhook

Format path: `/<workerID><path_dari_json>`

```bash
# Kirim data — bisa dirujuk di step lain via {{hook.sys}}, {{hook.usr}}, dst
curl -X POST http://localhost:8080/<workerID>/food \
  -H "Content-Type: application/json" \
  -d '{"sys":"jawab max 5 kata","usr":"makanan terenak di dunia?"}'
```

---

## List Semua Worker

```bash
curl http://localhost:8080/list
```

---

## Get Worker by ID

```bash
curl "http://localhost:8080/get?id=<id>"
```

---

## Update Worker

```bash
curl -X PUT http://localhost:8080/update \
  -H "Content-Type: application/json" \
  -d '{"id":"<id>","name":"Updated","mode":"loop","running":true,"vars":{...},"steps":[...]}'
```

> Jika `steps` berubah dan worker sedang running, worker di-restart otomatis.

---

## Run Worker

```bash
curl -X POST "http://localhost:8080/run?id=<id>"
```

---

## Stop Worker

```bash
curl -X POST "http://localhost:8080/stop?id=<id>"
```

---

## Delete Worker

```bash
curl -X DELETE "http://localhost:8080/delete?id=<id>"
```

---

## Template Reference

| Syntax | Sumber | Contoh |
|--------|--------|--------|
| `{{wadah.key}}` | `vars["wadah"]["key"]` | `{{glb.api_url}}` |
| `{{stepId.field}}` | output step by ID | `{{hook.usr}}` |
| `{{stepId.nested.field}}` | nested JSON response | `{{ai.result.response}}` |

### Contoh referensi output `call_api`

Jika response AI Cloudflare adalah:
```json
{ "result": { "response": "Pizza!" }, "success": true }
```

Maka step berikutnya bisa akses:
```
{{ai.result.response}}  →  "Pizza!"
{{ai.success}}          →  "true"
```

Jika response bukan JSON, tersedia sebagai:
```
{{stepId.raw}}     → raw string response
{{stepId.status}}  → HTTP status code
```

---

## Vars Cross-Reference

Vars antar wadah bisa saling merujuk:

```json
"vars": {
  "pvt": { "cf_id": "abc123" },
  "glb": {
    "base": "https://api.cloudflare.com/client/v4/accounts/{{pvt.cf_id}}",
    "ai_url": "{{glb.base}}/ai/run/@cf/meta/llama-3.1-8b-instruct-fast"
  }
}
```

`{{glb.ai_url}}` akan resolved menjadi URL lengkap dengan `cf_id` dari `pvt`.