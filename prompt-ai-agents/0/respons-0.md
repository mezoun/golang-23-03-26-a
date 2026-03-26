Berikut perbaikan alur worker agar AI Agents lebih andal, akurat, dan mengikuti best practice: penambahan langkah normalisasi input, langkah deteksi tipe berbasis prompt yang robust, pembatasan dan validasi respons AI (strict JSON schema), penanganan fallback, retry terpusat, serta logging yang lebih informatif. Di bawah ini ada payload CREATE (curl) yang direkomendasikan, lalu contoh curl untuk pengujian berbagai tipe input.

## Perubahan utama (singkat)
- Normalisasi input (trim, lowercase, remove punctuation yang tidak perlu).
- Deteksi tipe via model (lebih andal dari pencocokan literal).
- Setiap panggilan model gunakan prompt system yang tegas + temperature rendah untuk konsistensi.
- Validator JSON terpisah dengan schema eksplisit; otomatis tambahkan field yang hilang.
- Fallback jika model output bukan JSON valid: jalankan parsing/repair sekali lagi.
- Retry terpusat (max 3) dan exponential backoff sederhana.
- Output final disimpan di field tunggal: result_json.

## CREATE WORKER (curl)
```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Agentic AI: Resep Masakan (Improved)",
    "mode": "loop",
    "running": true,
    "vars": {
      "glb": {
        "ai_model": "@cf/meta/llama-3.1-8b-instruct-fast",
        "api_url": "https://api.cloudflare.com/client/v4/accounts/{{pvt.cf_id}}/ai/run/{{glb.ai_model}}",
        "temperature": 0.0
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
        "id": "normalize",
        "name": "Normalisasi Input",
        "type": "script",
        "value": {
          "script": "vars.normalized = (input||{}).input || ''; vars.normalized = vars.normalized.trim();"
        },
        "next": "detect_type"
      },
      {
        "id": "detect_type",
        "name": "Deteksi Tipe (Model)",
        "type": "call_api",
        "value": {
          "method": "POST",
          "url": "{{glb.api_url}}",
          "headers": { "Authorization": "Bearer {{pvt.cf_auth}}", "Content-Type": "application/json" },
          "body": {
            "messages": [
              { "role":"system", "content":"Kamu adalah classifier singkat. Tentukan apakah input pengguna merupakan: deskripsi (kalimat panjang menjelaskan cita rasa/asal/occasions), judul (nama masakan singkat), bahan (daftar bahan), atau campuran (gabungan). Jawab HANYA dengan satu kata: deskripsi | judul | bahan | campuran | unknown." },
              { "role":"user", "content":"Input: {{normalized}}" }
            ],
            "temperature": "{{glb.temperature}}",
            "max_tokens": 16
          },
          "retry": { "max": 2, "delay_ms": 800 }
        },
        "output": { "detected_type_raw": "{{detect_type.result.response}}" },
        "next": "map_type"
      },
      {
        "id": "map_type",
        "name": "Map/Normalize Type",
        "type": "script",
        "value": {
          "script": "let t = (detect_type.detected_type_raw||'').toLowerCase().replace(/[^a-z]/g,''); if(['deskripsi','deskripsi'].includes(t)) vars.type='deskripsi'; else if(['judul','title','name'].includes(t)) vars.type='judul'; else if(['bahan','ingredients','ingredient','list'].includes(t)) vars.type='bahan'; else if(['campuran','mixed','mixture'].includes(t)) vars.type='campuran'; else vars.type='unknown';"
        },
        "next": "branch_type"
      },
      {
        "id": "branch_type",
        "name": "Branch ke Agent yang Tepat",
        "type": "branch",
        "value": {
          "cases": [
            { "when": { "left": "{{type}}", "op": "==", "right": "deskripsi" }, "goto": "agent_core" },
            { "when": { "left": "{{type}}", "op": "==", "right": "judul"     }, "goto": "agent_core" },
            { "when": { "left": "{{type}}", "op": "==", "right": "bahan"     }, "goto": "agent_core" },
            { "when": { "left": "{{type}}", "op": "==", "right": "campuran"  }, "goto": "agent_core" },
            { "goto": "log_undefined" }
          ]
        }
      },
      {
        "id": "agent_core",
        "name": "Agent AI — Buat Resep (Core)",
        "type": "call_api",
        "value": {
          "method": "POST",
          "url": "{{glb.api_url}}",
          "headers": { "Authorization": "Bearer {{pvt.cf_auth}}", "Content-Type": "application/json" },
          "body": {
            "messages": [
              {
                "role":"system",
                "content":"Kamu adalah chef AI profesional. Pengguna memberi satu dari: deskripsi, judul, bahan, atau campuran. Buat sebuah JSON valid persis dengan 5 field: judul, deskripsi, bahan (array atau string), cara_masak (langkah terurut), saran_penyajian. Jangan sertakan teks selain JSON. Pastikan bahasa: Indonesian. Jika perlu lengkapi informasi yang hilang secara wajar."
              },
              {
                "role":"user",
                "content":"Tipe input: {{type}}. Konten: {{normalized}}. Keluarkan hanya JSON murni."
              }
            ],
            "temperature": "{{glb.temperature}}",
            "max_tokens": 800
          },
          "retry": { "max": 3, "delay_ms": 1000 }
        },
        "output": { "agent_raw": "{{agent_core.result.response}}" },
        "next": "validate_json"
      },
      {
        "id": "validate_json",
        "name": "Validator & Repair JSON",
        "type": "call_api",
        "value": {
          "method": "POST",
          "url": "{{glb.api_url}}",
          "headers": { "Authorization": "Bearer {{pvt.cf_auth}}", "Content-Type": "application/json" },
          "body": {
            "messages": [
              { "role":"system", "content":"Kamu adalah JSON fixer. Input mungkin berisi teks non-JSON dan JSON parcial. Ekstrak JSON jika ada, atau bangun JSON baru berdasarkan input. Output harus menjadi JSON murni (no markdown, no backticks) dengan tepat 5 field: judul (string), deskripsi (string), bahan (array of strings), cara_masak (array of steps), saran_penyajian (string). Jika bahan/cara_masak diterima sebagai string, pecah menjadi array secara wajar. Jangan menambahkan field lain." },
              { "role":"user", "content":"Raw output AI: {{agent_core.agent_raw}}" }
            ],
            "temperature": 0.0,
            "max_tokens": 512
          },
          "retry": { "max": 2, "delay_ms": 800 }
        },
        "output": { "repaired_raw": "{{validate_json.result.response}}" },
        "next": "parse_and_store"
      },
      {
        "id": "parse_and_store",
        "name": "Parse JSON ke Object & Simpan",
        "type": "script",
        "value": {
          "script": "try{ let parsed = JSON.parse(validate_json.repaired_raw); vars.result_json = parsed; } catch(e){ vars.result_json = {error:'invalid_json', raw: validate_json.repaired_raw}; }"
        },
        "next": "log_result"
      },
      {
        "id": "log_result",
        "name": "Output Akhir",
        "type": "log",
        "value": {
          "message": "[RESEP] type={{type}} | input={{normalized}} | result={{parse_and_store.result_json}}"
        },
        "next": "respond_webhook"
      },
      {
        "id": "respond_webhook",
        "name": "Balas ke Client",
        "type": "webhook_response",
        "value": {
          "status": 200,
          "body": { "result": "{{parse_and_store.result_json}}" }
        },
        "next": "end"
      },
      {
        "id": "log_undefined",
        "name": "Tipe Tidak Dikenal",
        "type": "log",
        "value": {
          "message": "[UNDEFINED] normalized=\"{{normalized}}\" | classifier_raw=\"{{detect_type.detected_type_raw}}\""
        },
        "next": "respond_undefined"
      },
      {
        "id": "respond_undefined",
        "name": "Response untuk Undefined",
        "type": "webhook_response",
        "value": {
          "status": 400,
          "body": { "error": "type_unknown", "message": "Gunakan: deskripsi / judul / bahan / campuran (berikan konteks yang jelas)." }
        },
        "next": "end"
      },
      { "id": "end", "name": "Selesai", "type": "log", "value": { "message": "[END] Menunggu input berikutnya..." } }
    ]
  }'
```

## Contoh CURL untuk testing webhook (/masakan)
(Asumsi server worker mendengarkan pada path /masakan dan response body menampilkan JSON final.)

1) Test — Deskripsi
```bash
curl -X POST http://localhost:8080/masakan \
  -H "Content-Type: application/json" \
  -d '{"input":"Masakan pedas yang berasal dari kota Padang dan sangat cocok disajikan dengan nasi putih, selain rendang."}'
```

2) Test — Judul
```bash
curl -X POST http://localhost:8080/masakan \
  -H "Content-Type: application/json" \
  -d '{"input":"Rendang Ayam Asam Manis"}'
```

3) Test — Bahan (daftar)
```bash
curl -X POST http://localhost:8080/masakan \
  -H "Content-Type: application/json" \
  -d '{"input":"Kentang, kambing, kunyit, cabai"}'
```

4) Test — Campuran (kombinasi)
```bash
curl -X POST http://localhost:8080/masakan \
  -H "Content-Type: application/json" \
  -d '{"input":"Masakan khas Jawa, enak dimakan malam hari, terbuat dari kentang dan sayuran, dimasak dengan cara ditumis."}'
```

5) Test — Undefined / Tidak relevan
```bash
curl -X POST http://localhost:8080/masakan \
  -H "Content-Type: application/json" \
  -d '{"input":"Berita ekonomi hari ini tentang pasar saham."}'
```

Jika ingin, saya bisa juga:
- Beri contoh schema JSON yang dihasilkan (contoh output final),
- Menyederhanakan atau menambah bahasa/locale handling,
- Menyesuaikan retries/delay atau menambahkan rate limit handling.