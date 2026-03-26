## Revised Agentic AI Worker – Recipe Generator  
**Goal:** menerima satu string bebas (judul, bahan, deskripsi, atau kombinasi) → mengekstrak informasi yang diperlukan → menghasilkan resep lengkap dengan kualitas tertinggi.  

---  

### 1️⃣ Input‑Handling Layer  

| Deteksi | Cara | Output | Tindakan selanjutnya |
|--------|------|--------|----------------------|
| **Hanya judul** | Regex + keyword‑density (cek keberadaan kata kerja “masak”, “resep”, dsb.) | `{title}` | Lanjut ke **Step 2‑A** (Prompt *“Berikan deskripsi singkat dan bahan‑bahan yang biasanya dipakai untuk *{title}*.”) |
| **Hanya bahan** | Deteksi list (pemisah koma, baris baru, atau bullet) | `{ingredients}` | Lanjut ke **Step 2‑B** (Prompt *“Berikan judul yang cocok dan deskripsi singkat untuk kumpulan bahan berikut.”) |
| **Hanya deskripsi/narasi** | Panjang > 30 kata, tidak ada koma‑list | `{description}` | Lanjut ke **Step 2‑C** (Prompt *“Dari deskripsi ini, apa judul yang tepat dan bahan‑bahan apa yang diperlukan?”) |
| **Kombinasi ≥ 2 elemen** | Kombinasi regex + token‑type detection | `{title?}`, `{ingredients?}`, `{description?}` | Langsung ke **Step 3** (Enrichment). |
| **Lainnya** | Tidak cocok dengan pola di atas | – | **LLM‑driven re‑prompt**: “Mohon masukkan **judul**, **bahan**, atau **deskripsi** yang jelas. Contoh: *‘Sate Padang – daging sapi, bumbu kacang, daun jeruk.’*” (output berupa satu string singkat). |

*Semua deteksi dilakukan dengan model **llama‑3.1‑8b‑instruct‑fast** (kecepatan tinggi, konteks 128 k) karena hanya memerlukan parsing ringan.*  

---  

### 2️⃣ Enrichment & Extraction (Step 2) – Pilih Model Berdasarkan Beban  

| Sub‑step | Tujuan | Model yang dipakai | Alasan |
|----------|--------|-------------------|--------|
| **2‑A / 2‑B / 2‑C** (generate missing fields) | Menambahkan judul, bahan, atau deskripsi yang belum ada | **llama‑3.1‑70b‑instruct** (24 k ctx, high quality) | Memerlukan pengetahuan kuliner luas & kreativitas untuk melengkapi data yang hilang. |
| **2‑D** (final consolidation) | Menggabungkan semua elemen menjadi satu objek `{title, description, ingredients}` | **llama‑3.1‑8b‑instruct‑fast** (128 k ctx) | Cukup untuk meng‑merge teks tanpa hallucination, lebih cepat. |

**Prompt contoh (model 70 b):**  

```
You are a culinary expert. From the following partial information, infer the missing parts and output a JSON object with three keys: "title", "description", "ingredients". Keep the ingredient list as an array of strings, each item a single ingredient with optional quantity.

Partial input: <user‑string>
```

**Output contoh:**  

```json
{
  "title": "Sate Padang",
  "description": "Masakan khas Sumatera Barat, kuah kental berwarna kuning, rasa pedas‑gurih.",
  "ingredients": [
    "daging sapi 500 g",
    "bumbu kacang 150 g",
    "daun jeruk 2 lembar",
    "air 500 ml",
    "garam secukupnya"
  ]
}
```

---  

### 3️⃣ Core Recipe Generation Pipeline (unchanged core, but model assignment clarified)

| Step | Agent / Function | Model | Temperature | Catatan |
|------|------------------|-------|-------------|---------|
| **3** | Mega‑Strict Validator (re‑check JSON) | llama‑3.1‑8b‑instruct‑fast | 0.1 | Pastikan semua field ada & tidak kosong. |
| **4** | Context Enricher (intent, difficulty, equipment) | llama‑3.1‑70b‑instruct | 0.1 | High‑quality inference. |
| **5‑8** | Parallel Domain Agents (Ingredient Realism, Cultural, Health, Budget) | llama‑3.1‑70b‑instruct | 0.2‑0.3 | Set‑based, masing‑masing menghasilkan vector JSON. |
| **9** | First Fusion & Hallucination Guard | llama‑3.1‑8b‑instruct‑fast | 0.1 | Menggabungkan output, memfilter inkonsistensi. |
| **10‑13** | Core Builders (Quantity, Method, Heat, Blueprint) | llama‑3.1‑8b‑instruct‑fast | 0.2‑0.4 | Efisien, konteks cukup. |
| **14‑15** | Sensory & Presentation Agents | llama‑3.1‑70b‑instruct | 0.6‑0.8 | Kreativitas tinggi untuk rasa & visual. |
| **16** | Narrative & Engagement | llama‑3.1‑70b‑instruct | 0.85 | Cerita budaya & emotif. |
| **17‑24** | Multi‑Pass Critics (Fact, Realism, Health, Culture, Sensory, Budget, Meta) | llama‑3.1‑8b‑instruct‑fast | 0.1‑0.3 | Konsistensi & skor. |
| **25‑27** | Nutrition, Plating, Variations | llama‑3.1‑70b‑instruct | 0.4‑0.6 | Nilai tambah. |
| **28** | Final Polish & JSON Output | llama‑3.1‑8b‑instruct‑fast | 0.2 | Produksi string final. |

*Model **gemma‑7b‑it** dan **meta‑llama‑3‑8b‑instuct** tidak dipakai karena:*
- **gemma‑7b‑it** terlalu lambat (80‑150 t/s) untuk tahap kreatif tinggi.  
- **meta‑llama‑3‑8b** memiliki konteks terbatas (8 k) sehingga tidak cocok untuk pipeline dengan banyak langkah paralel.  

---  

### 4️⃣ Loop & Feedback Mechanism  

1. **Critic score < 9.5** → feedback JSON dikirim ke **Step 5** (Fusion) dengan catatan spesifik (mis. “Takaran terlalu banyak, kurangi 20 %”).  
2. **Maximum loop** = 3 iterasi; setelah itu, worker mengembalikan resep dengan **warning** “Beberapa aspek masih berada di ambang batas, harap verifikasi manual.”  

---  

### 5️⃣ Output akhir (single JSON string)  

```json
{
  "recipe_id": "sha256(…)",
  "title": "Sate Padang",
  "description": "Masakan khas Sumatera Barat, kuah kental berwarna kuning, rasa pedas‑gurih.",
  "ingredients": [
    "daging sapi 500 g",
    "bumbu kacang 150 g",
    "daun jeruk 2 lembar",
    "air 500 ml",
    "garam secukupnya"
  ],
  "steps": [
    "Panaskan grill …",
    "Campur bumbu kacang …",
    "Tusuk daging, bakar …",
    "Tuang kuah, sajikan dengan daun jeruk."
  ],
  "nutrition": {
    "calories": 720,
    "protein_g": 45,
    "fat_g": 30,
    "carbs_g": 45
  },
  "variations": [
    "Versi vegan (ganti daging sapi dengan tempe)",
    "Versi hemat (kurangi bumbu kacang 50 %)",
    "Versi ekstra pedas (tambah cabai rawit)",
    "Versi festive (tambahkan kelapa parut)"
  ],
  "story": "Sate Padang berasal dari Padang, Sumatera Barat, ...",
  "confidence": 9.78,
  "critics": {
    "fact": 9.9,
    "realism": 9.8,
    "health": 9.7,
    "culture": 9.9,
    "sensory": 9.8,
    "budget": 9.6,
    "meta": 9.8
  }
}
```

Jika input tidak dikenali (mis. “asdf123”), worker **tidak** menjalankan pipeline; ia langsung mengembalikan satu string singkat:  

```
Mohon masukkan judul, bahan, atau deskripsi masakan yang jelas. Contoh: “Nasi Goreng – nasi, telur, kecap, bawang merah.”
```

---  

### 6️⃣ Ringkasan Pemilihan Model  

| Model | Digunakan untuk | Alasan |
|------|----------------|--------|
| **llama‑3.1‑70b‑instruct** | Enrichment, Domain Agents, Sensory, Narrative, Variations | Kualitas tinggi, konteks 24 k, cocok untuk kreativitas & pengetahuan kuliner. |
| **llama‑3.1‑8b‑instruct‑fast** | Input parsing, validator, fusion, critics, final polish | Cepat, konteks 128 k, cukup untuk operasi struktural & evaluasi. |
| **gemma‑7b‑it** & **meta‑llama‑3‑8b‑instuct** | **Tidak dipakai** | Kecepatan atau konteks terbatas membuatnya tidak efisien untuk pipeline ini. |

---  

Dengan alur ini, worker dapat menerima **apapun** berupa string tunggal, mengekstrak informasi yang diperlukan, dan menghasilkan resep yang **akurat, natural, dan kaya cerita** sambil menjaga penggunaan model yang optimal.