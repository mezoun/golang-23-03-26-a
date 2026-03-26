# curl-agent-recipe-generator.md

Kumpulan curl untuk membuat, menguji, dan mengelola **Recipe Generator Agent** —
implementasi `rancangan_membuat_agent.md` di atas engine `main.go`.

---

## 📐 Pemetaan Rancangan → main.go

| Fase Rancangan | Step Type main.go | Catatan |
|---|---|---|
| Input-Handling Layer | `webhook` + `call_api` (8b) + `branch` | Deteksi tipe input via LLM |
| Enrichment Step 2-A/B/C/D | `call_api` (70b) + `call_api` (8b) | Sekuensial |
| Validator Step 3 | `call_api` (8b) | JSON re-check |
| Context Enricher Step 4 | `call_api` (70b) | |
| Parallel Domain Agents Step 5-8 | `call_api` (70b) × 1 *(digabung)* | main.go tidak parallel → digabung 1 call |
| First Fusion Step 9 | `call_api` (8b) | |
| Core Builders Step 10-13 | `call_api` (8b) | |
| Sensory & Presentation Step 14-15 | `call_api` (70b) | |
| Narrative Step 16 | `call_api` (70b) | |
| Critics Step 17-24 | `call_api` (8b) + `branch` | Quality gate |
| Nutrition/Plating/Variations Step 25-27 | `call_api` (70b) | |
| Final Polish Step 28 | `call_api` (8b) | |
| **Save to File Step 28b** | **`call_api` → `/files/save`** | **Auto-simpan ke `output/`, return `view_url`** |

> **Catatan loop feedback:** rancangan max 3 iterasi → diimplementasikan sebagai 1× retry via `branch` (quality gate). Jump limit engine = 1000, cukup aman.

---

## ⚙️ Prerequisites

```
Server main.go berjalan di: http://localhost:8080
```

---

## 1. 🚀 CREATE WORKER

Buat agent, langsung `running: true` (mode `loop` — menunggu webhook tiap iterasi).

```bash
# Simpan definisi worker ke file JSON terlebih dahulu
cat > recipe-agent.json << 'ENDJSON'
{
  "id": "recipe-agent-v1",
  "name": "Recipe Generator Agent",
  "mode": "loop",
  "loop_interval_ms": 1000,
  "running": true,
  "vars": {
    "cf": {
      "token":   "Bearer cfut_KpdkVf3ZkIArmKDoBLQySa2TA4Pb5MXY4ZkXA7Qbe21abbbf",
      "url_8b":  "https://api.cloudflare.com/client/v4/accounts/56667d302adadb8e04093b1aac32017c/ai/run/@cf/meta/llama-3.1-8b-instruct-fast",
      "url_70b": "https://api.cloudflare.com/client/v4/accounts/56667d302adadb8e04093b1aac32017c/ai/run/@cf/meta/llama-3.1-70b-instruct"
    }
  },
  "steps": [

    {
      "id":   "s01_webhook",
      "name": "01 – Receive Input",
      "type": "webhook",
      "value": { "method": "POST", "path": "/recipe" }
    },

    {
      "id":   "s02_detect",
      "name": "02 – Input Classification (8b-fast)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_8b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "You are a culinary input classifier. Analyze the user input and respond ONLY with a compact JSON object — no markdown, no extra text: {\"type\":\"title|ingredients|description|combination|unknown\"}. Rules: title=single dish name only. ingredients=comma or newline separated ingredient list. description=narrative paragraph longer than 30 words. combination=two or more of the above. unknown=non-food gibberish that cannot be interpreted as any recipe element."
            },
            {
              "role": "user",
              "content": "{{json:s01_webhook.input}}"
            }
          ]
        },
        "retry": { "max": 1, "delay_ms": 500, "backoff": false }
      }
    },

    {
      "id":   "s03_branch_type",
      "name": "03 – Route: unknown → skip pipeline",
      "type": "branch",
      "value": {
        "cases": [
          {
            "when": {
              "left":  "{{s02_detect.result.response}}",
              "op":    "contains",
              "right": "unknown"
            },
            "goto": "s18_run_complete"
          }
        ]
      }
    },

    {
      "id":   "s04_enrich",
      "name": "04 – Enrichment: fill missing fields (70b)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_70b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "You are a culinary expert specializing in Indonesian and Asian cuisine. From partial food input, infer ALL missing parts and output ONLY valid JSON — no markdown, no explanation: {\"title\":\"dish name\",\"description\":\"2–3 sentence description in Indonesian\",\"ingredients\":[\"ingredient with quantity\",\"...\"]}"
            },
            {
              "role": "user",
              "content": "Detected input type: {{json:s02_detect.result.response}}\nRaw input: {{json:s01_webhook.input}}"
            }
          ]
        },
        "retry": { "max": 2, "delay_ms": 1000, "backoff": true }
      }
    },

    {
      "id":   "s05_validate",
      "name": "05 – Validator: check & consolidate JSON (8b-fast)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_8b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "You are a strict JSON validator. Verify the input JSON contains all required keys: title (non-empty string), description (non-empty string), ingredients (non-empty array of strings). If valid, output the EXACT same JSON unchanged. If any key is missing or empty, add it with a sensible culinary default. Output ONLY valid JSON — no markdown."
            },
            {
              "role": "user",
              "content": "{{json:s04_enrich.result.response}}"
            }
          ]
        },
        "retry": { "max": 1, "delay_ms": 500, "backoff": false }
      }
    },

    {
      "id":   "s06_context",
      "name": "06 – Context Enricher: intent, difficulty, equipment (70b)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_70b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "You are a culinary context analyzer. Enrich the recipe base data with cooking context. Output ONLY valid JSON — no markdown: {\"title\":\"...\",\"description\":\"...\",\"ingredients\":[...],\"difficulty\":\"easy|medium|hard\",\"equipment\":[\"...\"],\"serving_size\":\"N porsi\",\"prep_time_min\":0,\"cook_time_min\":0,\"cuisine_type\":\"...\",\"meal_type\":\"breakfast|lunch|dinner|snack|dessert\"}"
            },
            {
              "role": "user",
              "content": "{{json:s05_validate.result.response}}"
            }
          ]
        },
        "retry": { "max": 2, "delay_ms": 1000, "backoff": true }
      }
    },

    {
      "id":   "s07_domain",
      "name": "07 – Domain Agents: ingredient + cultural + health + budget (70b)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_70b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "You are a multi-domain culinary analyst covering 4 specializations simultaneously: (1) Ingredient Realism — are all ingredients real and available in Indonesia? (2) Cultural Authenticity — cultural context and notes. (3) Health Analysis — health benefits and cautions. (4) Budget Estimation — cost tier. Output ONLY valid JSON — no markdown: {\"ingredient_check\":{\"all_realistic\":true,\"substitutions\":{}},\"cultural_notes\":\"...\",\"health_notes\":\"...\",\"allergens\":[\"...\"],\"budget\":\"low|medium|high\",\"estimated_cost_idr\":0}"
            },
            {
              "role": "user",
              "content": "{{json:s06_context.result.response}}"
            }
          ]
        },
        "retry": { "max": 2, "delay_ms": 1000, "backoff": true }
      }
    },

    {
      "id":   "s08_fusion",
      "name": "08 – First Fusion + Hallucination Guard (8b-fast)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_8b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "You are a data fusion agent. Merge two JSON inputs (context enricher + domain analysis) into one coherent, hallucination-free recipe object. Remove any ingredient or step that does not make culinary sense. Output ONLY valid JSON — no markdown: {\"title\":\"...\",\"description\":\"...\",\"ingredients\":[...],\"difficulty\":\"...\",\"equipment\":[...],\"prep_time_min\":0,\"cook_time_min\":0,\"serving_size\":\"...\",\"cuisine_type\":\"...\",\"meal_type\":\"...\",\"cultural_notes\":\"...\",\"health_notes\":\"...\",\"allergens\":[...],\"budget\":\"...\",\"estimated_cost_idr\":0}"
            },
            {
              "role": "user",
              "content": "Context JSON: {{json:s06_context.result.response}}\nDomain JSON: {{json:s07_domain.result.response}}"
            }
          ]
        },
        "retry": { "max": 1, "delay_ms": 500, "backoff": false }
      }
    },

    {
      "id":   "s09_core",
      "name": "09 – Core Builders: quantity + method + heat + blueprint (8b-fast)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_8b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "You are a precise recipe step generator. Create detailed, actionable cooking steps with exact quantities and temperatures. Output ONLY valid JSON — no markdown: {\"steps\":[\"1. ...\",\"2. ...\"],\"primary_method\":\"...\",\"heat_levels\":[\"medium\",\"high\"],\"key_techniques\":[\"...\"],\"timing_notes\":\"...\"}"
            },
            {
              "role": "user",
              "content": "{{json:s08_fusion.result.response}}"
            }
          ]
        },
        "retry": { "max": 1, "delay_ms": 500, "backoff": false }
      }
    },

    {
      "id":   "s10_sensory",
      "name": "10 – Sensory & Presentation Agent (70b)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_70b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "You are a sensory experience and food styling specialist. Describe the dish's sensory qualities in vivid, professional detail. Output ONLY valid JSON — no markdown: {\"aroma\":\"...\",\"texture\":\"...\",\"taste_profile\":\"...\",\"color_appearance\":\"...\",\"plating_suggestion\":\"...\",\"garnish\":[\"...\"],\"serving_temperature\":\"...\"}"
            },
            {
              "role": "user",
              "content": "Recipe: {{json:s08_fusion.result.response}}\nCooking steps: {{json:s09_core.result.response}}"
            }
          ]
        },
        "retry": { "max": 2, "delay_ms": 1000, "backoff": true }
      }
    },

    {
      "id":   "s11_narrative",
      "name": "11 – Narrative & Engagement Agent (70b)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_70b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "Kamu adalah penulis kuliner yang hangat dan berempati. Tulis cerita latar budaya yang emosional untuk hidangan ini. Output HANYA JSON valid — tanpa markdown: {\"story\":\"cerita maks 200 kata, emosional, dalam Bahasa Indonesia\",\"origin\":\"...\",\"cultural_significance\":\"...\",\"best_occasion\":\"...\",\"fun_fact\":\"...\"}"
            },
            {
              "role": "user",
              "content": "{{json:s08_fusion.result.response}}"
            }
          ]
        },
        "retry": { "max": 2, "delay_ms": 1000, "backoff": true }
      }
    },

    {
      "id":   "s12_critics",
      "name": "12 – Multi-Pass Critics: 7 dimensi (8b-fast)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_8b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "You are a strict culinary quality evaluator. Score the recipe across exactly 7 dimensions from 0.0 to 10.0. Compute average. Set quality_ok=false if average < 9.5. Output ONLY valid JSON — no markdown: {\"fact\":0.0,\"realism\":0.0,\"health\":0.0,\"culture\":0.0,\"sensory\":0.0,\"budget\":0.0,\"meta\":0.0,\"average\":0.0,\"feedback\":\"specific points to improve\",\"quality_ok\":true}"
            },
            {
              "role": "user",
              "content": "Base recipe: {{json:s08_fusion.result.response}}\nSteps: {{json:s09_core.result.response}}\nSensory: {{json:s10_sensory.result.response}}\nNarrative: {{json:s11_narrative.result.response}}"
            }
          ]
        },
        "retry": { "max": 1, "delay_ms": 500, "backoff": false }
      }
    },

    {
      "id":   "s13_quality_gate",
      "name": "13 – Quality Gate: score < 9.5 → retry",
      "type": "branch",
      "value": {
        "cases": [
          {
            "when": {
              "left":  "{{s12_critics.result.response}}",
              "op":    "contains",
              "right": "quality_ok\":false"
            },
            "goto": "s14_retry_enrich"
          },
          {
            "goto": "s15_nutrition_vars"
          }
        ]
      }
    },

    {
      "id":   "s14_retry_enrich",
      "name": "14 – Retry: improve with critic feedback (70b)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_70b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "You are a culinary improvement specialist. Improve the recipe by addressing ALL points in the critic feedback. Output ONLY valid JSON with the same structure as the original recipe — no markdown: {\"title\":\"...\",\"description\":\"...\",\"ingredients\":[...],\"difficulty\":\"...\",\"equipment\":[...],\"prep_time_min\":0,\"cook_time_min\":0,\"serving_size\":\"...\",\"cuisine_type\":\"...\",\"meal_type\":\"...\",\"cultural_notes\":\"...\",\"health_notes\":\"...\",\"allergens\":[...],\"budget\":\"...\",\"estimated_cost_idr\":0}"
            },
            {
              "role": "user",
              "content": "Original recipe: {{json:s08_fusion.result.response}}\nCritic feedback: {{json:s12_critics.result.response}}"
            }
          ]
        },
        "retry": { "max": 2, "delay_ms": 1500, "backoff": true }
      },
      "next": "s15_nutrition_vars"
    },

    {
      "id":   "s15_nutrition_vars",
      "name": "15 – Nutrition, Plating & Variations (70b)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_70b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "You are a nutrition and recipe variation specialist. Compute per-serving nutrition and generate 4 creative variations. Output ONLY valid JSON — no markdown: {\"nutrition\":{\"calories\":0,\"protein_g\":0,\"fat_g\":0,\"carbs_g\":0,\"fiber_g\":0,\"sodium_mg\":0},\"variations\":[\"Versi vegan: ...\",\"Versi hemat: ...\",\"Versi ekstra pedas: ...\",\"Versi festive: ...\"],\"plating_tips\":[\"...\",\"...\"],\"storage_tips\":\"...\",\"shelf_life_hours\":0}"
            },
            {
              "role": "user",
              "content": "Improved recipe (if available, else empty): {{json:s14_retry_enrich.result.response}}\nOriginal recipe (fallback): {{json:s08_fusion.result.response}}\nUse whichever contains data."
            }
          ]
        },
        "retry": { "max": 2, "delay_ms": 1000, "backoff": true }
      }
    },

    {
      "id":   "s16_final_polish",
      "name": "16 – Final Polish & JSON Assembly (8b-fast)",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url":     "{{cf.url_8b}}",
        "headers": { "Authorization": "{{cf.token}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "You are a final JSON assembler. Merge ALL provided inputs into one complete, clean recipe object. Generate a UUID v4 for recipe_id. Output ONLY valid JSON — absolutely no markdown, no preamble, no trailing text: {\"recipe_id\":\"uuid-v4-here\",\"title\":\"...\",\"description\":\"...\",\"ingredients\":[...],\"steps\":[...],\"difficulty\":\"...\",\"equipment\":[...],\"prep_time_min\":0,\"cook_time_min\":0,\"serving_size\":\"...\",\"cuisine_type\":\"...\",\"meal_type\":\"...\",\"nutrition\":{\"calories\":0,\"protein_g\":0,\"fat_g\":0,\"carbs_g\":0,\"fiber_g\":0},\"variations\":[...],\"sensory\":{\"aroma\":\"...\",\"texture\":\"...\",\"taste_profile\":\"...\",\"color_appearance\":\"...\",\"plating_suggestion\":\"...\"},\"story\":\"...\",\"cultural_notes\":\"...\",\"health_notes\":\"...\",\"budget\":\"...\",\"allergens\":[...],\"confidence\":0.0,\"critics\":{\"fact\":0.0,\"realism\":0.0,\"health\":0.0,\"culture\":0.0,\"sensory\":0.0,\"budget\":0.0,\"meta\":0.0,\"average\":0.0}}"
            },
            {
              "role": "user",
              "content": "Base recipe: {{json:s08_fusion.result.response}}\nCooking steps: {{json:s09_core.result.response}}\nSensory: {{json:s10_sensory.result.response}}\nNarrative: {{json:s11_narrative.result.response}}\nNutrition+Variations: {{json:s15_nutrition_vars.result.response}}\nCritics scores: {{json:s12_critics.result.response}}"
            }
          ]
        },
        "retry": { "max": 1, "delay_ms": 500, "backoff": false }
      }
    },

    {
      "id":   "s16b_save_file",
      "name": "16b – Save Result to File (output folder)",
      "type": "call_api",
      "value": {
        "method":  "POST",
        "url":     "http://localhost:8080/files/save",
        "headers": { "Content-Type": "application/json" },
        "body": {
          "tag":     "recipe",
          "content": "{{json:s16_final_polish.result.response}}"
        },
        "retry": { "max": 2, "delay_ms": 500, "backoff": false }
      }
    },

    {
      "id":   "s17_log_result",
      "name": "17 – Log Final Recipe",
      "type": "log",
      "value": {
        "message": "RECIPE_DONE | input={{s01_webhook.input}} | critics_avg={{s12_critics.result.response}} | file={{s16b_save_file.result.response}}"
      }
    },

    {
      "id":   "s18_run_complete",
      "name": "18 – Run Complete (juga menangani unknown input)",
      "type": "log",
      "value": {
        "message": "RUN_COMPLETE | worker=recipe-agent-v1 | input={{s01_webhook.input}} | detected_type={{s02_detect.result.response}} | saved_file={{s16b_save_file.result.response}}"
      }
    }

  ]
}
ENDJSON

# Kirim ke worker engine
curl -s -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d @recipe-agent.json | jq .
```

**Expected response:**
```json
{
  "id": "recipe-agent-v1",
  "name": "Recipe Generator Agent",
  "mode": "loop",
  "running": true,
  ...
}
```

---

## 2. 🧪 WEBHOOK TEST — Input Judul Saja

Setelah worker running, kirim request ke `/recipe-agent-v1/recipe`  
(format path: `/{worker-id}{webhook-path}`).

```bash
curl -s -X POST http://localhost:8080/recipe-agent-v1/recipe \
  -H "Content-Type: application/json" \
  -d '{"input": "Sate Padang"}' | jq .
```

**Expected response (HTTP 200):**
```json
{ "status": "received" }
```

---

## 3. 🧪 WEBHOOK TEST — Input Kombinasi (judul + bahan)

```bash
curl -s -X POST http://localhost:8080/recipe-agent-v1/recipe \
  -H "Content-Type: application/json" \
  -d '{"input": "Rendang – daging sapi 500g, santan kental, lengkuas, serai, cabai merah, bawang merah, bawang putih, kunyit"}' | jq .
```

---

## 4. 🧪 WEBHOOK TEST — Input Deskripsi Narasi

```bash
curl -s -X POST http://localhost:8080/recipe-agent-v1/recipe \
  -H "Content-Type: application/json" \
  -d '{"input": "Hidangan berkuah kuning khas Jawa Timur yang terbuat dari daging sapi empuk dengan bumbu rempah yang kaya. Biasanya disajikan saat lebaran dengan lontong atau ketupat."}' | jq .
```

---

## 5. 🧪 WEBHOOK TEST — Input Tidak Dikenal (harus skip pipeline)

Harus langsung loncat ke `s18_run_complete` tanpa memanggil LLM pipeline.

```bash
curl -s -X POST http://localhost:8080/recipe-agent-v1/recipe \
  -H "Content-Type: application/json" \
  -d '{"input": "asdf123xyz!!"}' | jq .
```

---

## 6. 🧪 WEBHOOK TEST — Input Bahan Saja

```bash
curl -s -X POST http://localhost:8080/recipe-agent-v1/recipe \
  -H "Content-Type: application/json" \
  -d '{"input": "nasi putih, telur, kecap manis, bawang merah, bawang putih, cabai, minyak goreng"}' | jq .
```

---

## 7. 💾 FILE HASIL AGENT

### 7a. Lihat semua file hasil agent

```bash
# List semua file di folder output (termasuk hasil agent)
curl -s http://localhost:8080/files/list | jq '[.[] | select(.name | startswith("agent-"))]'
```

**Contoh output:**
```json
[
  {
    "name": "agent-recipe_20250326-143022_a1b2c3d4.json",
    "size_bytes": 3821,
    "modified": "2025-03-26T14:30:22Z"
  }
]
```

### 7b. Download / lihat isi file hasil agent

```bash
# Download langsung ke file lokal (nama otomatis sesuai server)
curl -O -J "http://localhost:8080/files/view?name=agent-recipe_20250326-143022_a1b2c3d4.json"

# Atau tampilkan ke stdout dan format dengan jq
curl -s "http://localhost:8080/files/view?name=agent-recipe_20250326-143022_a1b2c3d4.json" | jq .
```

### 7c. Simpan manual ke output folder (untuk testing / embed sistem lain)

Endpoint `POST /files/save` bisa dipanggil dari luar agent (curl biasa):

```bash
curl -s -X POST http://localhost:8080/files/save \
  -H "Content-Type: application/json" \
  -d '{
    "tag": "recipe",
    "content": "{\"title\":\"Rendang\",\"description\":\"Masakan khas Minang\"}"
  }' | jq .
```

**Response (HTTP 201):**
```json
{
  "name": "agent-recipe_20250326-143055_ff1a2b3c.json",
  "size_bytes": 61,
  "view_url": "/files/view?name=agent-recipe_20250326-143055_ff1a2b3c.json"
}
```

> **Konvensi nama file:**  
> `agent-{tag}_{YYYYMMDD-HHMMSS}_{rand8hex}.json`  
> Awalan `agent-` memudahkan filter di `/files/list`. `tag` boleh kosong → `agent_{ts}_{rand}.json`.

### 7d. Hapus file hasil agent

```bash
curl -s -X DELETE "http://localhost:8080/files/delete?name=agent-recipe_20250326-143022_a1b2c3d4.json" | jq .
```

---

## 8. 📊 CEK STATUS WORKER

```bash
curl -s "http://localhost:8080/status?id=recipe-agent-v1" | jq .
```

**Expected response:**
```json
{
  "id": "recipe-agent-v1",
  "running": true,
  "last_run_at": "2025-...",
  "last_run_ok": true,
  "last_error": "",
  "run_count": 3
}
```

---

## 9. 📋 GET DEFINISI WORKER

```bash
curl -s "http://localhost:8080/get?id=recipe-agent-v1" | jq .
```

---

## 10. 📋 LIST SEMUA WORKERS

```bash
curl -s http://localhost:8080/list | jq '[.[] | {id, name, mode, running}]'
```

---

## 11. ⏹️ STOP WORKER

```bash
curl -s -X POST "http://localhost:8080/stop?id=recipe-agent-v1" | jq .
```

**Expected:**
```json
{ "status": "stopped" }
```

---

## 12. ▶️ RESUME WORKER (tanpa update definisi)

```bash
curl -s -X POST "http://localhost:8080/run?id=recipe-agent-v1" | jq .
```

**Expected:**
```json
{ "status": "started", "mode": "loop" }
```

---

## 13. ✏️ UPDATE VARS (ganti token tanpa restart steps)

Update vars saja — engine otomatis me-refresh vars tiap iterasi loop.

```bash
curl -s -X PUT http://localhost:8080/update \
  -H "Content-Type: application/json" \
  -d '{
    "id": "recipe-agent-v1",
    "name": "Recipe Generator Agent",
    "mode": "loop",
    "loop_interval_ms": 1000,
    "running": true,
    "vars": {
      "cf": {
        "token":   "Bearer NEW_TOKEN_HERE",
        "url_8b":  "https://api.cloudflare.com/client/v4/accounts/56667d302adadb8e04093b1aac32017c/ai/run/@cf/meta/llama-3.1-8b-instruct-fast",
        "url_70b": "https://api.cloudflare.com/client/v4/accounts/56667d302adadb8e04093b1aac32017c/ai/run/@cf/meta/llama-3.1-70b-instruct"
      }
    }
  }' | jq '{id, name, running}'
```

---

## 14. 🗑️ DELETE WORKER

```bash
curl -s -X DELETE "http://localhost:8080/delete?id=recipe-agent-v1" | jq .
```

**Expected:**
```json
{ "deleted": "recipe-agent-v1" }
```

---

## 🔍 Trace Alur Pipeline (untuk debug via log)

Setelah mengirim webhook, pantau log server. Urutan yang benar:

```
[worker:recipe-agent-v1][step:01 – Receive Input(s01_webhook)]         WEBHOOK triggered
[worker:recipe-agent-v1][step:02 – Input Classification(s02_detect)]   CALL_API POST ...8b... → HTTP 200
[worker:recipe-agent-v1][step:03 – Route(s03_branch_type)]             BRANCH → no case matched (valid input) ATAU BRANCH → goto s18_run_complete (unknown)
[worker:recipe-agent-v1][step:04 – Enrichment(s04_enrich)]             CALL_API POST ...70b... → HTTP 200
[worker:recipe-agent-v1][step:05 – Validator(s05_validate)]            CALL_API POST ...8b... → HTTP 200
[worker:recipe-agent-v1][step:06 – Context Enricher(s06_context)]      CALL_API POST ...70b... → HTTP 200
[worker:recipe-agent-v1][step:07 – Domain Agents(s07_domain)]          CALL_API POST ...70b... → HTTP 200
[worker:recipe-agent-v1][step:08 – Fusion(s08_fusion)]                 CALL_API POST ...8b... → HTTP 200
[worker:recipe-agent-v1][step:09 – Core Builders(s09_core)]            CALL_API POST ...8b... → HTTP 200
[worker:recipe-agent-v1][step:10 – Sensory(s10_sensory)]               CALL_API POST ...70b... → HTTP 200
[worker:recipe-agent-v1][step:11 – Narrative(s11_narrative)]           CALL_API POST ...70b... → HTTP 200
[worker:recipe-agent-v1][step:12 – Critics(s12_critics)]               CALL_API POST ...8b... → HTTP 200
[worker:recipe-agent-v1][step:13 – Quality Gate(s13_quality_gate)]     BRANCH → goto s14 (retry) ATAU goto s15 (ok)
[worker:recipe-agent-v1][step:15 – Nutrition+Vars(s15_nutrition_vars)] CALL_API POST ...70b... → HTTP 200
[worker:recipe-agent-v1][step:16 – Final Polish(s16_final_polish)]     CALL_API POST ...8b... → HTTP 200
[worker:recipe-agent-v1][step:16b – Save Result(s16b_save_file)]       CALL_API POST /files/save → HTTP 201 {"name":"agent-recipe_20250326-143022_a1b2c3d4.json","view_url":"/files/view?name=agent-recipe_..."}
[worker:recipe-agent-v1][step:17 – Log Final(s17_log_result)]          LOG → RECIPE_DONE | ... | file={"name":"agent-recipe_...","view_url":"..."}
[worker:recipe-agent-v1][step:18 – Run Complete(s18_run_complete)]     LOG → RUN_COMPLETE | ... | saved_file={"name":"agent-recipe_...","view_url":"..."}
[worker:recipe-agent-v1] workflow completed
```

---

## 📊 Ringkasan Model per Step

| Step | Model | Alasan |
|------|-------|--------|
| s02_detect | 8b-fast | Parsing ringan, konteks 128k |
| s04_enrich | 70b | Pengetahuan kuliner luas |
| s05_validate | 8b-fast | Struktur check, tidak perlu kreativitas |
| s06_context | 70b | Inferensi konteks kompleks |
| s07_domain | 70b | 4 domain analisis sekaligus |
| s08_fusion | 8b-fast | Merge JSON, cukup struktural |
| s09_core | 8b-fast | Step generation terstruktur |
| s10_sensory | 70b | Kreativitas tinggi (temp 0.7) |
| s11_narrative | 70b | Kreativitas maksimal (temp 0.85) |
| s12_critics | 8b-fast | Evaluasi konsisten, konteks 128k |
| s14_retry_enrich | 70b | Improvement butuh pengetahuan dalam |
| s15_nutrition_vars | 70b | Kalkulasi nutrisi + kreativitas variasi |
| s16_final_polish | 8b-fast | Assembly struktural, cepat |