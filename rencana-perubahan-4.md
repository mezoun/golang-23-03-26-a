| # | Item | Status di `main.go` | Keterangan |
|---|------|---------------------|------------|
| X1 | `handleUpdate`: stop jika `running: false` | ❌ **Belum** | Hanya stop jika `stepsHash` berbeda. Kasus `running:false` tidak di-handle |
| X2 | `execSleep`: hapus dead code `cfg.Message` | ❌ **Belum** | Baris `if ms == 0 && strings.Contains(cfg.Message, "{{")` masih ada di line 1130 |
| X3 | `handleDelete`: bersihkan `rt.statuses[id]` | ❌ **Belum** | Tidak ada pemanggilan `deleteStatus()` atau sejenisnya |
| X4 | `Store.shutdown()`: ganti `time.Sleep(500ms)` dengan `sync.WaitGroup` | ❌ **Belum** | Struct `Store` tidak punya `wg sync.WaitGroup`, `main()` masih pakai `time.Sleep(500ms)` |
| X5 | `writeJSON`: log error encode | ❌ **Belum** | Masih `_ = json.NewEncoder(w).Encode(v)` di line 1376 |
| X6 | `sleep`/`set_var`: aplikasikan `parsedOutput` | ❌ **Belum** | Kedua case di `runWorker` tidak punya blok `if step.parsedOutput != nil` |
| O1 | `ReadHeaderTimeout: 5s` pada HTTP server | ❌ **Belum** | `http.Server` di `main()` tidak punya field ini |
| O2 | `writeJSON` pakai `json.Marshal` + `w.Write` | ❌ **Tidak perlu** | Minor, bisa dikerjakan bersama X5 |

---

## Rencana perbaikan `main.go` + workflow agent

Ini yang akan saya kerjakan dalam satu pass:

**Patch 1 — X5 + O2:** `writeJSON` → `json.Marshal` + log error

**Patch 2 — X2:** `execSleep` → hapus dead code `cfg.Message`

**Patch 3 — X6:** `runWorker` → tambah `parsedOutput` untuk `sleep` dan `set_var`

**Patch 4 — X1:** `handleUpdate` → stop worker jika `updated.Running == false` dan sedang berjalan

**Patch 5 — X3:** tambah `rt.deleteStatus(id)` + panggil di `handleDelete`

**Patch 6 — X4:** `Store` tambah `wg sync.WaitGroup`, `shutdown()` blocking, hapus `time.Sleep(500ms)` di `main()`

**Patch 7 — O1:** `http.Server` tambah `ReadHeaderTimeout: 5 * time.Second`

Setelah semua patch, saya akan buatkan file JSON workflow agent resep sesuai `rancangan_membuat_agent.md`.