# Worker Engine — Agentic AI: Resep Masakan (v2 — Fixed)

Worker ini menerima input masakan dalam 3 bentuk berbeda (**deskripsi**, **judul**, atau **bahan**),
routing ke agent AI yang sesuai, lalu AI validator memastikan output JSON lengkap 5 field.

---

## Alur

```
webhook (input)
    │
    ▼
branch (detect_type)
    ├── type == "deskripsi"  ──► agent_deskripsi  ──┐
    ├── type == "judul"      ──► agent_judul       ──┤  output map: agent_raw
    ├── type == "bahan"      ──► agent_bahan       ──┘
    └── else (undefined)     ──► log_undefined ──► end
                                                     │
                                                     ▼
                                               parse_result  (AI validator)
                                                     │
                                                     ▼
                                               log_result → end
```

---

## CREATE WORKER

```bash
curl -X POST http://localhost:8080/create \
  -H "Content-Type: application/json" \
  -d '{
  "name": "Agentic AI: Resep Masakan v2",
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
              "content": "Kamu adalah chef AI yang ahli masakan Indonesia dan internasional. Pengguna memberikan DESKRIPSI masakan yang diinginkan. Tugasmu: buat resep lengkap berdasarkan deskripsi tersebut. WAJIB jawab HANYA dengan JSON valid, tanpa teks tambahan, tanpa markdown. Format: {\"judul\":\"nama masakan\",\"deskripsi\":\"deskripsi singkat masakan\",\"bahan\":\"daftar bahan dan takaran\",\"cara_masak\":\"langkah memasak\",\"saran_penyajian\":\"saran penyajian\"}"
            },
            {
              "role": "user",
              "content": "Buat resep berdasarkan deskripsi ini: {{input.data}}"
            }
          ]
        }
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
              "content": "Kamu adalah chef AI yang ahli masakan Indonesia dan internasional. Pengguna memberikan NAMA/JUDUL masakan. Tugasmu: buat resep lengkap untuk masakan tersebut. WAJIB jawab HANYA dengan JSON valid, tanpa teks tambahan, tanpa markdown. Format: {\"judul\":\"nama masakan\",\"deskripsi\":\"deskripsi singkat masakan\",\"bahan\":\"daftar bahan dan takaran\",\"cara_masak\":\"langkah memasak\",\"saran_penyajian\":\"saran penyajian\"}"
            },
            {
              "role": "user",
              "content": "Buat resep lengkap untuk masakan: {{input.data}}"
            }
          ]
        }
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
              "content": "Kamu adalah chef AI yang ahli masakan Indonesia dan internasional. Pengguna memberikan daftar BAHAN yang tersedia. Tugasmu: rekomendasikan dan buat resep masakan terbaik dari bahan-bahan tersebut. WAJIB jawab HANYA dengan JSON valid, tanpa teks tambahan, tanpa markdown. Format: {\"judul\":\"nama masakan\",\"deskripsi\":\"deskripsi singkat masakan\",\"bahan\":\"daftar bahan dan takaran\",\"cara_masak\":\"langkah memasak\",\"saran_penyajian\":\"saran penyajian\"}"
            },
            {
              "role": "user",
              "content": "Bahan yang saya punya: {{input.data}}. Rekomendasikan dan buat resep masakan dari bahan ini."
            }
          ]
        }
      },
      "output": { "agent_raw": "{{agent_bahan.result.response}}" },
      "next": "parse_result"
    },
    {
      "id": "log_undefined",
      "name": "Tipe Input Tidak Dikenal",
      "type": "log",
      "value": {
        "message": "[UNDEFINED] Tipe tidak dikenal: \"{{input.type}}\" | data: \"{{input.data}}\" | Gunakan type: deskripsi / judul / bahan"
      },
      "next": "end"
    },
    {
      "id": "parse_result",
      "name": "Agent AI — Validator dan Formatter",
      "type": "call_api",
      "value": {
        "method": "POST",
        "url": "{{glb.api_url}}",
        "headers": { "Authorization": "Bearer {{pvt.cf_auth}}" },
        "body": {
          "messages": [
            {
              "role": "system",
              "content": "Kamu adalah JSON validator dan formatter. Input adalah teks yang seharusnya berisi JSON resep masakan. Tugasmu: pastikan output adalah JSON valid dengan tepat 5 field: judul, deskripsi, bahan, cara_masak, saran_penyajian. Jika sudah valid kembalikan apa adanya. Jika ada field hilang tambahkan. Jika ada teks di luar JSON buang. WAJIB jawab HANYA dengan JSON murni, TANPA markdown, TANPA backtick, TANPA teks apapun."
            },
            {
              "role": "user",
              "content": "Validasi dan format JSON ini: {{agent_deskripsi.agent_raw}}{{agent_judul.agent_raw}}{{agent_bahan.agent_raw}}"
            }
          ]
        }
      },
      "output": { "final": "{{parse_result.result.response}}" },
      "next": "log_result"
    },
    {
      "id": "log_result",
      "name": "Output Akhir Resep",
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

---

## TRIGGER WEBHOOK

Ganti `WORKER_ID` dengan ID dari response `/create`.

### Test 1 — Input tipe `deskripsi`

```bash
curl -X POST http://localhost:8080/WORKER_ID/masakan \
  -H "Content-Type: application/json" \
  -d '{
  "type": "deskripsi",
  "data": "Masakan pedas manis dengan protein tinggi, cocok untuk sarapan, menggunakan telur dan cabai"
}'
```

### Test 2 — Input tipe `judul`

```bash
curl -X POST http://localhost:8080/WORKER_ID/masakan \
  -H "Content-Type: application/json" \
  -d '{
  "type": "judul",
  "data": "Rendang Daging Sapi"
}'
```

### Test 3 — Input tipe `bahan`

```bash
curl -X POST http://localhost:8080/WORKER_ID/masakan \
  -H "Content-Type: application/json" \
  -d '{
  "type": "bahan",
  "data": "ayam kampung, santan, serai, daun salam, lengkuas, bawang merah, bawang putih, kemiri, kunyit"
}'
```

### Test 4 — Tipe `undefined` (edge case)

```bash
curl -X POST http://localhost:8080/WORKER_ID/masakan \
  -H "Content-Type: application/json" \
  -d '{
  "type": "foto",
  "data": "gambar nasi goreng"
}'
```

---

## Catatan Fix: Aturan `output` Mapping

### ❌ Bug sebelumnya — self-reference di parse_result

```json
"output": { "full_response": "{{parse_result.result.response}}" }
```
Step `parse_result` membaca dari dirinya sendiri setelah `pctx` di-override → selalu kosong.

### ✅ Fix — output mapping di masing-masing agent

```json
// agent_deskripsi: baca raw response SEBELUM pctx di-override
"output": { "agent_raw": "{{agent_deskripsi.result.response}}" }

// parse_result: baca dari agent yg sudah selesai (sudah ada di pctx)
"output": { "final": "{{parse_result.result.response}}" }
```

**Aturan:** field `output` pada step X boleh self-reference ke `{{X.field}}` karena
raw API response masuk ke pctx lebih dulu, baru output mapping meng-override.
Yang tidak boleh adalah membaca step **lain** yang belum dieksekusi.

Di step `parse_result`, input AI menggunakan:
```
{{agent_deskripsi.agent_raw}}{{agent_judul.agent_raw}}{{agent_bahan.agent_raw}}
```
Hanya **satu** yang berisi nilai (agent yang dieksekusi branch), dua lainnya kosong string — ini disengaja dan benar.

---

## Expected Log (saat berhasil)

```
[worker:ID][step:Deteksi Tipe Input] BRANCH case[1] match ("judul" == "judul") → goto "agent_judul"
[worker:ID][step:Agent AI — dari Judul] CALL_API POST https://api.cloudflare.com/... → HTTP 200 | {"result":{"response":"{\"judul\":\"Rendang..."}...}
[worker:ID][step:Agent AI — Validator dan Formatter] CALL_API POST ... → HTTP 200 | ...
[worker:ID][step:Output Akhir Resep] LOG → [RESEP] type=judul | input=Rendang Daging Sapi | result={"judul":"Rendang Daging Sapi","deskripsi":"...","bahan":"...","cara_masak":"...","saran_penyajian":"..."}
[worker:ID][step:Selesai] LOG → [END] Menunggu input berikutnya...
```