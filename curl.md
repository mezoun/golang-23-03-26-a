# Worker Engine — curl Demo

Ganti `YOUR_CF_ACCOUNT_ID` dan `YOUR_CF_TOKEN` sebelum dijalankan.

---

## 1. Create Worker

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "AI Food Worker",
  "mode": "loop",
  "running": true,
  "vars": {
    "pvt": {
      "cf_id":   "YOUR_CF_ACCOUNT_ID",
      "cf_auth": "YOUR_CF_TOKEN"
    },
    "glb": {
      "ai_model": "@cf/meta/llama-3.1-8b-instruct-fast",
      "api_url":  "https://api.cloudflare.com/client/v4/accounts/{{pvt.cf_id}}/ai/run/{{glb.ai_model}}"
    }
  },
  "steps": [
    {
      "id":   "hook",
      "name": "terima request",
      "type": "webhook",
      "value": {
        "method": "POST",
        "path":   "/food"
      },
      "output": {
        "sys":  "{{hook.sys}}",
        "usr":  "{{hook.usr}}",
        "meta": {
          "model":  "{{glb.ai_model}}",
          "source": "webhook"
        }
      }
    },
    {
      "id":   "log_input",
      "name": "log input",
      "type": "log",
      "value": {
        "message": "[INPUT] sys={{hook.sys}} | usr={{hook.usr}} | model={{glb.ai_model}}"
      },
      "output": {
        "summary": "{{hook.sys}} / {{hook.usr}}",
        "model":   "{{glb.ai_model}}"
      }
    },
    {
      "id":   "ai",
      "name": "tanya AI",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":    "{{glb.api_url}}",
        "headers": {
          "Authorization": "Bearer {{pvt.cf_auth}}"
        },
        "body": {
          "messages": [
            { "role": "system", "content": "{{hook.sys}}" },
            { "role": "user",   "content": "{{hook.usr}}" }
          ]
        }
      },
      "output": {
        "answer": "{{ai.result.response}}",
        "ok":     "{{ai.success}}",
        "meta": {
          "model":    "{{glb.ai_model}}",
          "question": "{{hook.usr}}",
          "sys":      "{{hook.sys}}"
        }
      }
    },
    {
      "id":   "log_result",
      "name": "log result",
      "type": "log",
      "value": {
        "message": "[RESULT] model={{glb.ai_model}} | Q={{ai.meta.question}} | A={{ai.answer}} | ok={{ai.ok}}"
      },
      "output": {
        "final":        "{{ai.answer}}",
        "full_log":     "Q: {{ai.meta.question}} | A: {{ai.answer}}",
        "unknown_test": "{{tidak.ada}}",
        "debug": {
          "model":           "{{glb.ai_model}}",
          "ok":              "{{ai.ok}}",
          "input_summary":   "{{log_input.summary}}",
          "log_input_model": "{{log_input.model}}"
        }
      }
    }
  ]
}'
```

Response berisi `id` — catat sebagai `WORKER_ID` untuk perintah berikutnya.

---

## 2. Trigger Webhook

Ganti `WORKER_ID` dengan `id` dari response create.

```bash
curl -X POST http://localhost:8080/WORKER_ID/food \
  -H "Content-Type: application/json" \
  -d '{
  "sys": "Jawab singkat dalam 1 kalimat.",
  "usr": "Makanan terenak di dunia itu apa?"
}'
```

---

## 3. Get Worker

```bash
curl http://localhost:8080/get?id=WORKER_ID
```

---

## 4. List Semua Worker

```bash
curl http://localhost:8080/list
```

---

## 5. Stop Worker

```bash
curl -X POST http://localhost:8080/stop?id=WORKER_ID
```

---

## 6. Run Worker

```bash
curl -X POST http://localhost:8080/run?id=WORKER_ID
```

---

## 7. Update Worker

Steps berubah → worker di-restart otomatis.

```bash
curl -X PUT http://localhost:8080/update \
  -H "Content-Type: application/json" \
  -d '{
  "id":      "WORKER_ID",
  "name":    "AI Food Worker v2",
  "mode":    "loop",
  "running": true,
  "vars": {
    "pvt": {
      "cf_id":   "YOUR_CF_ACCOUNT_ID",
      "cf_auth": "YOUR_CF_TOKEN"
    },
    "glb": {
      "ai_model": "@cf/meta/llama-3.1-8b-instruct-fast",
      "api_url":  "https://api.cloudflare.com/client/v4/accounts/{{pvt.cf_id}}/ai/run/{{glb.ai_model}}"
    }
  },
  "steps": [
    {
      "id":   "hook",
      "name": "terima request",
      "type": "webhook",
      "value": { "method": "POST", "path": "/food" },
      "output": {
        "sys": "{{hook.sys}}",
        "usr": "{{hook.usr}}"
      }
    },
    {
      "id":   "ai",
      "name": "tanya AI",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":    "{{glb.api_url}}",
        "headers": { "Authorization": "Bearer {{pvt.cf_auth}}" },
        "body": {
          "messages": [
            { "role": "user", "content": "{{hook.usr}}" }
          ]
        }
      },
      "output": {
        "answer": "{{ai.result.response}}",
        "ok":     "{{ai.success}}"
      }
    },
    {
      "type": "log",
      "value": {
        "message": "[v2] Q={{hook.usr}} | A={{ai.answer}} | ok={{ai.ok}}"
      }
    }
  ]
}'
```

---

## 8. Trigger Webhook (setelah update)

```bash
curl -X POST http://localhost:8080/WORKER_ID/food \
  -H "Content-Type: application/json" \
  -d '{
  "usr": "Minuman terenak itu apa?"
}'
```

---

## 9. Delete Worker

```bash
curl -X DELETE http://localhost:8080/delete?id=WORKER_ID
```