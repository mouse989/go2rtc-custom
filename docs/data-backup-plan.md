# Kế hoạch Backup Dữ liệu — go2rtc-custom

**Trạng thái: RÀ SOÁT + LÊN KẾ HOẠCH — chưa code/triển khai.** Tài liệu này tổng hợp toàn bộ nơi hệ thống lưu dữ liệu trên đĩa, cách tổ chức hiện tại, và đề xuất kế hoạch backup cụ thể.

---

## 0. Tóm tắt nhanh

- Toàn bộ dữ liệu (ngoài file cấu hình `go2rtc.yaml` gốc) là **file JSON/JSONL phẳng + ảnh JPEG/PNG** — **không dùng CSDL nào** (đã grep toàn repo: không có SQLite/Postgres/ORM).
- Mọi module tự tạo file/thư mục dữ liệu riêng theo đúng 1 quy ước chung: `filepath.Join(filepath.Dir(app.ConfigPath), "<tên>")` — nghĩa là **tất cả nằm cạnh file `go2rtc.yaml`, trong đúng 1 thư mục duy nhất**.
- ⇒ Về bản chất, backup toàn hệ thống = backup **một thư mục duy nhất** trên đĩa (thư mục chứa `go2rtc.yaml`).

---

## 1. Rà soát dữ liệu hiện có

### 1.1. File cấu hình gốc

| Mục | Vị trí | Định dạng |
|---|---|---|
| Config chính | `go2rtc.yaml` (đường dẫn qua `-c`, mặc định `./go2rtc.yaml`) | YAML — streams, api, log... của go2rtc gốc + danh sách module của bản fork này |

Dữ liệu người dùng/phân quyền/API key **không** nằm trong file này — nằm ở các file JSON cạnh nó (bên dưới).

### 1.2. Dữ liệu từng module (tất cả là file cạnh `go2rtc.yaml`)

| Module | File/thư mục | Dạng | Retention đã có | Ghi chú khối lượng |
|---|---|---|---|---|
| auth — users | `users.json` | 1 file | — | nhỏ |
| auth — JWT secret | `.jwt_secret` | 32 byte random, mode 0600 | — | ⚠️ xem §3 |
| auth — vị trí camera | `camera_locations.json` | 1 file | — | nhỏ |
| auth — nhóm camera | `camera_groups.json` | 1 file | — | nhỏ |
| auth — địa bàn (regions) | `regions.json` | 1 file | — | nhỏ |
| auth — settings chung | `settings.json` | 1 file | — | nhỏ (chứa VietMap API key — xem §3) |
| auth — loại camera | `camera_types.json` | 1 file | — | nhỏ |
| auth — thiết bị | `devices.json` | 1 file | — | nhỏ |
| auth — log sự kiện thiết bị | `device_events.jsonl` | append-only JSONL | **chưa có** | tăng vô hạn |
| auth — xác minh thiết bị (WebAuthn) | `device_bindings.json` | 1 file, mode 0600 | — | nhỏ |
| auth — preset camera | `cam_presets.json` | 1 file, mode 0600 | — | nhỏ |
| auth — lịch sử đăng nhập | `login_history/<YYYY-MM-DD>.jsonl` | 1 file/ngày | **chưa có** | tăng vô hạn |
| auth — cảnh báo an ninh | `security_alerts/<YYYY-MM-DD>.jsonl` + `known_ips.json` | 1 file/ngày + 1 file | **chưa có** | tăng vô hạn |
| auth — vị trí GPS user | `user_locations/<YYYY-MM-DD>.jsonl` | 1 file/ngày | **chưa có** | tăng vô hạn |
| auth — Integration API key | `integrations.json` | 1 file | — | nhỏ, nhưng ⚠️ xem §3 |
| access log | `access_log/<YYYY-MM-DD>.jsonl` | 1 file/ngày | **chưa có** | tăng vô hạn |
| counting — config | `counting.json` | 1 file | n/a | config |
| counting — trạm đo | `counting_stations.json` | 1 file | — | nhỏ |
| counting — dữ liệu đếm xe | `counting_data/<YYYY-MM-DD>.json` | 1 file/ngày | **có — mặc định 30 ngày** | cao: mỗi camera, bucket 5 phút, liên tục |
| incidents | `incidents_data/incidents_<year>.json` + `meta.json` | 1 file/năm + 1 meta | **không** (lưu vĩnh viễn, có chủ đích — nhập tay) | thấp |
| traffic — config | `traffic.json` | 1 file | n/a | config |
| traffic — lịch sử quét | `traffic_data/<YYYY-MM-DD>.json` | 1 file/ngày | **có — mặc định 30 ngày** | theo chu kỳ quét |
| **trafficflow (Đo đếm lưu lượng)** | ✅ **đã chuyển sang SQLite** — `trafficflow_data/trafficflow.db` (1 file duy nhất, bảng `sensors`/`observed_sensors`/`flow_records`) | 1 file CSDL | **có — 30 ngày** (`deleteRecordsOlderThan`, quét mỗi 6h — vẫn giữ nguyên, chưa tắt theo §3 mục 1, chờ quyết định riêng) | **cao nhất trong hệ thống**: ~500 camera × 288 chu kỳ/ngày ≈ 144.000 bản ghi/ngày — lỗi ghi-lại-toàn-bộ-chỉ-mục ở §8 **đã được vá** bằng migration này |
| aievent — sự kiện | `aievent_data/events_<ngày>.json` + `record_index.json` | 1 file/ngày | **không** (lưu vĩnh viễn, có chủ đích) | thấp (vài chục sự kiện/ngày) |
| aievent — ảnh đính kèm | `aievent_data/images/<RecordID>/...` | file ảnh JPEG/PNG | **có — mặc định 4 giờ** | tối đa 5 ảnh/sự kiện, 5MB/ảnh |
| traveltime — config | `traveltime.json` | 1 file | n/a | config |
| traveltime — tuyến | `travel_routes.json` | 1 file | — | nhỏ |
| traveltime — ngày lễ override | `holiday_overrides.json` | 1 file | — | nhỏ |
| traveltime — log | `travel_logs/<YYYY-MM-DD>.jsonl` | 1 file/ngày | **chưa có** | tăng vô hạn |
| workers (worker phân tích từ xa) | `workers.json` | 1 file | n/a | nhỏ, ⚠️ chứa username/password plaintext |

**Tóm lại cấu trúc thực tế trên đĩa** (ví dụ config đặt tại `/etc/go2rtc/go2rtc.yaml`):

```
/etc/go2rtc/
├── go2rtc.yaml
├── users.json  .jwt_secret  integrations.json
├── camera_locations.json  camera_groups.json  camera_types.json  regions.json
├── settings.json  devices.json  device_bindings.json  cam_presets.json
├── device_events.jsonl  known_ips.json
├── login_history/  security_alerts/  user_locations/  access_log/  travel_logs/
├── counting.json  counting_stations.json  counting_data/
├── incidents_data/
├── traffic.json  traffic_data/
├── trafficflow_data/ (nay chỉ còn trafficflow.db — SQLite, kèm -wal/-shm khi đang chạy ở WAL mode)
├── aievent_data/ (kèm images/)
├── traveltime.json  travel_routes.json  holiday_overrides.json
└── workers.json
```

---

## 2. Phân loại theo mức độ quan trọng

**Nhóm A — Không thể tái tạo, mất là mất vĩnh viễn** (ưu tiên backup cao nhất, RPO càng ngắn càng tốt):
`go2rtc.yaml`, `users.json`, `.jwt_secret`, `integrations.json`, `camera_locations/groups/types.json`, `regions.json`, `settings.json`, `devices.json`, `device_bindings.json`, `cam_presets.json`, `counting_stations.json`, `counting.json`, `traffic.json`, `traveltime.json`, `travel_routes.json`, `holiday_overrides.json`, `workers.json`, `incidents_data/*` (nhập tay, không có nguồn khác để tái tạo), mapping `Sensor` của trafficflow.

**Nhóm B — Time-series vận hành, có giá trị phân tích/báo cáo nhưng mất vài giờ/ngày gần nhất vẫn chấp nhận được**:
`counting_data/`, `traffic_data/`, `trafficflow_data/`, `aievent_data/events_*.json`, `access_log/`, `login_history/`, `security_alerts/`, `user_locations/`, `device_events.jsonl`, `travel_logs/`.

**Nhóm C — Tái tạo được hoặc giá trị thấp theo thời gian** (có thể loại khỏi backup để giảm dung lượng):
`aievent_data/images/` — bản thân hệ thống đã tự xoá sau 4 giờ, backup khó theo kịp và không cần thiết.

---

## 3. Rủi ro phát hiện qua rà soát (cần biết **trước khi** backup)

1. **6 loại log chưa có retention — ĐÃ CHỐT chính sách với bạn:**
   - `login_history/`, `security_alerts/`, `user_locations/` → **giới hạn 3 tháng (90 ngày)**, cần bổ sung sweep (hiện chưa có).
   - `device_events.jsonl`, `access_log/`, `travel_logs/`, và cả 3 dataset time-series khối lượng lớn (`counting_data/`, `traffic_data/`, `trafficflow_data/`) → **giữ lâu dài, không giới hạn theo thời gian** (dữ liệu bạn muốn thu thập/phân tích lâu dài). Lưu ý: `counting_data/` và `traffic_data/`/`trafficflow_data/` hiện đang có sweep xoá sau 30 ngày (mặc định) — cần **tắt/nâng retention này lên "không giới hạn"** để khớp quyết định này; xem đánh giá ảnh hưởng performance ở §8 trước khi tắt.

2. **Một số secret lưu dạng plaintext, chưa mã hoá tại rest**: `VietmapAPIKey`/`VietmapLiveTrafficAPIKey` (trong `settings.json`), Telegram bot token (trong `traffic.json`), `Worker.Username`/`Password` (trong `workers.json`). Bản backup chứa các file này **cần được mã hoá khi lưu ra ngoài máy** (đặc biệt nếu đẩy lên cloud/NAS chia sẻ).

3. **⚠️ Quan trọng nhất: `.jwt_secret` và `integrations.json` phải luôn được backup/khôi phục CÙNG MỘT thời điểm, không trộn lẫn phiên bản khác nhau.** Lý do: khóa dùng để mã hoá lại (`KeyEnc`, phần cho phép admin "xem lại" API key) được dẫn xuất trực tiếp từ `.jwt_secret` (`HMAC-SHA256(jwtSecretRef, "integration-key-enc-v1")`). Nếu phục hồi `integrations.json` của một bản backup cũ nhưng `.jwt_secret` đã bị thay bằng bản mới (hoặc ngược lại), các API key cũ vẫn **xác thực bình thường** (vì `KeyHash` là SHA-256 độc lập) nhưng chức năng "xem lại key" sẽ hỏng vĩnh viễn cho các integration đó. Ngoài ra, mất `.jwt_secret` còn làm mọi JWT token hiện tại bị vô hiệu (user phải đăng nhập lại) — không nguy hiểm nhưng cần biết trước.

4. **Không có transaction/khoá ghi**: mọi file JSON được ghi bằng `os.WriteFile` (ghi đè toàn file), không có cơ chế flush/lock để tạm dừng ghi trước khi backup. Rủi ro đọc trúng 1 file đang ghi nửa chừng là có thật nhưng thấp (ghi diễn ra rất nhanh, không có 2 tiến trình ghi đồng thời cùng file) — xem cách giảm rủi ro ở §4.

---

## 4. Đề xuất kế hoạch backup (nguyên tắc chung — hạ tầng-trung lập)

- **Phạm vi**: toàn bộ thư mục chứa `go2rtc.yaml` (một thư mục duy nhất, theo đúng cấu trúc ở §1.2).
- **Tần suất**: 1 lần/ngày, vào giờ ít tải (ví dụ 02:00 sáng).
- **Nguyên tắc 3-2-1**: ít nhất 1 bản sao cục bộ (đĩa khác), 1 bản off-site (NAS...); lưu dạng **versioned** (không ghi đè bản cũ ngay lập tức).
- **Retention bản backup**: giữ 7 bản ngày gần nhất + 4 bản cuối-tuần gần nhất + vài bản cuối-tháng gần nhất — giảm dần dung lượng theo thời gian mà vẫn có điểm khôi phục xa.
- **Giảm rủi ro đọc file đang ghi nửa chừng** (mục 4 ở §3): snapshot filesystem (nếu hạ tầng hỗ trợ) cho các file JSON; với `trafficflow.db` (SQLite) dùng lệnh backup online an toàn của chính SQLite (`.backup`) thay vì copy thô file đang mở.

**→ Triển khai thực tế (công cụ cụ thể, đã chốt với bạn): xem §9.** Mục này chỉ còn giữ lại làm nguyên tắc tham khảo khi đổi hạ tầng sau này.

## 5. Khôi phục (restore)

- Restore = giải mã + giải nén đúng 1 bản backup vào **đúng đường dẫn** (thư mục chứa `go2rtc.yaml`, trùng với `app.ConfigPath` lúc backup) → không cần sửa code gì, mọi module tự load lại dữ liệu khi service khởi động lại.
- **Bắt buộc**: `.jwt_secret` và `integrations.json` phải phục hồi từ **cùng một bản backup** (xem lý do ở §3 mục 3).
- Nên **test restore định kỳ** (ví dụ mỗi quý) trên 1 server/staging riêng để xác nhận bản backup dùng được thật — "tồn tại" không đồng nghĩa "khôi phục được".

## 6. Khuyến nghị sửa kèm theo (nên làm trước khi backup đi vào vận hành chính thức)

1. **Thêm retention cho 6 loại log hiện chưa có** (§3 mục 1) — nếu không, backup sẽ ngày càng nặng và tốn chi phí lưu trữ off-site vô ích. Có thể dùng đúng mẫu `deleteOlderThan`/sweeper đã có sẵn trong `internal/counting/storage.go`, `internal/trafficflow/records.go`.
2. **Xem xét mã hoá VietMap API key / Telegram token / worker password tại rest** (hiện đang plaintext) — quan trọng hơn nếu backup được đẩy lên cloud/nơi lưu chia sẻ.

## 7. Việc cần bạn quyết định trước khi triển khai

1. Server đang chạy trên hạ tầng gì (bare-metal/VM/container)? Có sẵn LVM/ZFS/btrfs snapshot không? → quyết định cách lấy bản chụp nhất quán.
2. ✅ Nơi lưu off-site: **NAS Synology** (đã có — xem §8.4 về cách gắn vào kế hoạch).
3. RPO/RTO mong muốn: chấp nhận mất tối đa bao nhiêu giờ dữ liệu? Cần khôi phục xong trong bao lâu?
4. Muốn tự viết script backup (ví dụ `scripts/backup.sh` + cron, dễ tuỳ biến) hay dùng công cụ backup có sẵn (Restic, BorgBackup — có mã hoá + dedup + versioning tích hợp sẵn, ít phải tự code)?

---

## 8. Đánh giá performance với cách lưu hiện tại + có cần đổi CSDL không

### 8.1. Vì sao hỏi câu này đúng lúc

Quyết định ở §3 mục 1 ("giữ lâu dài" cho `counting_data/`, `traffic_data/`, `trafficflow_data/`, tức **tắt retention 30 ngày hiện có**) làm lộ ra 2 vấn đề performance thật, không phải giả định — một đã từng xảy ra thật trong chính codebase này, một là lỗi kiến trúc cụ thể trong module Đo đếm lưu lượng (trafficflow) cần sửa **trước khi** tắt retention.

### 8.2. Vấn đề 1 (đã từng xảy ra thật) — đọc nhiều file ngày càng chậm khi không giới hạn thời gian

Module `traffic` (quét kẹt xe) từng gặp đúng việc này và đã phải vá: comment gốc trong `internal/traffic/storage.go:28-36` ghi rõ — khi giữ tới ~100 file lịch sử (ứng với retention mặc định), việc đọc+parse lại toàn bộ file mỗi lần gọi `GET /api/traffic/data` "mất vài giây và càng chậm dần khi dữ liệu tích lũy", phải thêm một cache tổng hợp (`summaryCache`) để né việc đọc lại từ đĩa mỗi lần.

→ Đây là **bằng chứng thật, không phải suy đoán**: lưu theo kiểu "1 file JSON/ngày" hoạt động tốt cho dữ liệu "hôm nay" (mỗi lần chỉ đọc 1 file), nhưng **bất kỳ tính năng nào cần tổng hợp/so sánh nhiều ngày** (báo cáo xu hướng, dashboard lịch sử, export theo khoảng ngày) sẽ chậm dần tuyến tính theo số ngày đã tích lũy — đúng là hậu quả của việc "giữ lâu dài" nếu không xử lý thêm. Hiện `counting`/`trafficflow` chưa có tính năng truy vấn nhiều-ngày nào (chỉ có dữ liệu "hôm nay"/"gần nhất"), nên **chưa bị chậm ngay bây giờ** — nhưng sẽ gặp đúng vấn đề này ngay khi bạn làm thêm 1 trong các tính năng: thống kê theo tuần/tháng, dashboard lịch sử, hoặc export CSV theo khoảng ngày.

### 8.3. Vấn đề 2 (nghiêm trọng hơn, cần sửa SỚM — không chỉ là rủi ro tương lai)

Module `trafficflow` (Đo đếm lưu lượng — dữ liệu khối lượng lớn nhất hệ thống, do chính tôi xây trong các phiên trước) có **1 file chỉ mục toàn cục** (`trafficflow_data/flow_index.json`) dùng để chống trùng (`internal/trafficflow/records.go:139-144`): mỗi lần có 1 bản tin Hawkwise mới (tại baseline ~100 req/phút với ~500 camera), hàm `Ingest()` sẽ **ghi lại TOÀN BỘ file chỉ mục này** (marshal lại hết + `os.WriteFile` đè file) — không phải chỉ thêm 1 dòng.

File chỉ mục này **không có cơ chế dọn** (không theo retention 30 ngày như file dữ liệu từng-sensor-từng-ngày) — nó cộng dồn **vĩnh viễn**, kể cả khi vẫn còn giữ 30-ngày retention cho dữ liệu chi tiết. Ước tính: ~144.000 bản ghi/ngày × 365 ngày ≈ **52 triệu dòng/năm** trong 1 file JSON — và file này bị ghi lại toàn bộ mỗi lần có request mới. Đây là lỗi kiến trúc **sẽ làm hệ thống chậm dần mỗi tháng**, độc lập với quyết định "giữ dữ liệu lâu dài" — và quyết định đó (đúng, hợp lý về mặt nghiệp vụ) sẽ làm lỗi này **lộ ra nhanh hơn**, vì trước đây sweep 30 ngày từng vô tình giới hạn một phần tốc độ phình to của chỉ mục theo thời gian không hề giới hạn tổng số record mà chỉ ảnh hưởng tới cách sweep xoá file — nói đúng hơn: **sweep hiện tại không hề dọn `flow_index.json`** (tôi vừa xác nhận lại code — sweep `deleteRecordFilesOlderThan` có dọn `index`/`days` trong RAM theo ngày đã xoá, nhưng vẫn phải đọc/viết lại index mỗi request trong khi nó tiếp tục phình to vô hạn).

**→ Khuyến nghị: sửa cái này sớm, không chờ đến lúc chuyển CSDL** (xem §8.5 mục ưu tiên).

### 8.4. Có cần đổi sang CSDL khác không?

**Không cần đổi toàn bộ hệ thống** — phần cấu hình/user (Nhóm A ở §2: `users.json`, `settings.json`, `camera_*.json`, `integrations.json`...) nhỏ, ít viết, và ở dạng JSON còn có lợi (admin/bạn có thể mở xem/sửa tay khi cần) — giữ nguyên JSON là hợp lý, không nên động vào.

**Nên đổi sang CSDL nhúng (embedded) cho đúng 3 dataset khối lượng lớn**: `counting_data/`, `traffic_data/`, `trafficflow_data/`. Đề xuất **SQLite** (qua `database/sql` + driver Go, ví dụ `modernc.org/sqlite` — bản pure-Go, không cần biên dịch C, dễ build/deploy hơn `mattn/go-sqlite3`):

- Vẫn là **1 file duy nhất trên đĩa**, không cần cài thêm server CSDL, không phá nguyên tắc "không hạ tầng phụ" hiện tại của hệ thống.
- Insert có **index** (vd trên `event_id`, `(sensor_id, received_at)`) → giải quyết triệt để Vấn đề 2 (không còn khái niệm "ghi lại toàn bộ file chỉ mục" — chỉ update 1 dòng + index tự cập nhật).
- Truy vấn theo khoảng ngày/theo camera **nhanh và không chậm dần theo số ngày tích lũy** (giải quyết Vấn đề 1) — khác hẳn việc phải mở/đọc/parse N file JSON.
- Ghi đồng thời từ nhiều goroutine được SQLite tự lo (WAL mode), không cần tự quản mutex + ghi đè file như hiện tại.
- Hạ tầng backup ở §4 **không đổi nhiều** — vẫn tar.gz + mã hoá + đẩy lên NAS; chỉ cần dùng `sqlite3 .backup` (hoặc snapshot khi DB đang idle) để lấy bản chụp nhất quán của file `.db` thay vì tar trực tiếp 1 file đang mở (SQLite hỗ trợ lệnh backup online an toàn, không cần dừng service).

**Không cần Postgres/MySQL** (cần 1 server riêng, thêm hạ tầng phải quản lý/backup riêng) — quy mô dữ liệu này (trăm nghìn dòng/ngày) vẫn nằm hoàn toàn trong khả năng xử lý tốt của SQLite.

### 8.5. Thứ tự ưu tiên đề xuất

1. ✅ **Đã làm (2026-10-14)**: `trafficflow` đã chuyển sang SQLite (`internal/trafficflow/db.go`), vá triệt để lỗi `flow_index.json` (dedup giờ là `UNIQUE`/`PRIMARY KEY` trên `event_id`, không còn ghi-lại-toàn-bộ-file mỗi push). Tự động nhập lại dữ liệu JSON cũ (nếu có) vào SQLite một lần khi khởi động (`migrate.go`), file JSON cũ được đổi tên `.migrated` sau khi nhập thành công, không xoá. Retention 30 ngày hiện vẫn giữ nguyên (chưa tắt theo quyết định "giữ lâu dài" ở §3 mục 1 — đây là 2 việc tách biệt, cần quyết định riêng).
2. **Còn lại**: `counting_data/`, `traffic_data/` chưa chuyển — vẫn JSON theo ngày như trước.
3. **Trước khi tắt retention 30 ngày** của `trafficflow`: đã xong (mục 1) — có thể tắt an toàn bất cứ lúc nào nếu muốn.
4. **Song song, độc lập**: bổ sung sweep 90 ngày cho `login_history/`, `security_alerts/`, `user_locations/` (đã chốt ở §3 mục 1) — việc này không liên quan CSDL, chưa làm, làm được ngay.

---

## 9. Triển khai thực tế trên Windows Server 2016 (đã chốt, có script)

Server thật chạy **Windows Server 2016**, go2rtc chạy trực tiếp bằng
`.exe` (không phải Windows Service), thư mục dữ liệu thật là
`D:\Working\Counting\`. Công cụ đã chốt với bạn: **Restic** (mã hoá + dedupe
+ retention có sẵn, không tự viết), kết nối NAS Synology qua **SMB/CIFS**.

**Kiến trúc**: 2 Restic repo độc lập — 1 trên ổ cứng local khác ổ chứa dữ
liệu, 1 trên NAS Synology qua UNC path (`\\NAS-IP\...`) — hỏng 1 bên không
ảnh hưởng bên kia (đúng nguyên tắc 3-2-1 ở §4). Tự động hoá bằng **Windows
Task Scheduler** (không phải cron) chạy 1 PowerShell script mỗi ngày
02:00.

**Vấn đề `trafficflow.db` đang live-write (WAL mode)**: giải quyết bằng
`sqlite3.exe <db> ".backup '<đích>'"` — lệnh backup online, nhất quán, an
toàn ngay cả khi go2rtc.exe đang ghi — chạy trước mỗi lần Restic backup,
ghi bản chụp vào `trafficflow_data\_sqlite_snapshot\trafficflow.db` (nằm
trong chính `$DataDir` nên tự được backup cùng, không cần thêm path/exclude
riêng cho Restic).

**Code/script cụ thể — xem [`scripts/backup/`](../scripts/backup/README.md)**:
- `Backup-Go2rtc.ps1` — script chạy hàng ngày (snapshot SQLite → backup vào
  repo local → backup vào repo NAS → dọn bản cũ theo retention cả 2 repo
  độc lập; 1 bên lỗi không chặn bên kia).
- `Restore-Go2rtc.ps1` — khôi phục từ 1 snapshot Restic vào thư mục mới
  (không bao giờ đè trực tiếp lên thư mục đang chạy).
- `README.md` — hướng dẫn cài đặt lần đầu: cài `restic.exe`/`sqlite3.exe`,
  tạo + giới hạn quyền file mật khẩu Restic, lưu thông tin đăng nhập NAS qua
  `cmdkey` (không map ổ đĩa, không lưu mật khẩu trong script), đăng ký Task
  Scheduler.

Đây đã là trả lời cụ thể cho §7 mục 1 và mục 4 (Restic, không tự viết tool
mới) — chỉ còn §7 mục 3 (RPO/RTO chính xác bạn muốn) là chưa chốt; mặc định
đang dùng RPO = 24h (backup 1 lần/ngày) theo §4, có thể rút ngắn nếu cần.

---

## Phụ lục: Lịch sử phiên bản

| Phiên bản | Ngày | Nội dung |
|---|---|---|
| 1.0 | 2026-10-14 | Rà soát toàn bộ dữ liệu + cách tổ chức hiện có, phân loại theo mức độ quan trọng, đề xuất kế hoạch backup. Trạng thái: planning, chưa triển khai. |
| 1.1 | 2026-10-14 | Chốt chính sách retention theo quyết định của bạn (login_history/security_alerts/user_locations: 3 tháng; phần còn lại kể cả 3 dataset khối lượng lớn: giữ lâu dài). Thêm §8: đánh giá performance khi giữ lâu dài (dẫn chứng thật từ comment có sẵn trong `internal/traffic/storage.go`), phát hiện lỗi ghi-lại-toàn-bộ-chỉ-mục trong trafficflow, và khuyến nghị chuyển 3 dataset khối lượng lớn sang SQLite (giữ nguyên JSON cho phần cấu hình/user). Trạng thái: planning, chưa triển khai. |
| 1.2 | 2026-10-14 | **Đã triển khai**: chuyển `trafficflow` (Đo đếm lưu lượng) sang SQLite (`internal/trafficflow/db.go` — bảng `sensors`/`observed_sensors`/`flow_records`, dùng `modernc.org/sqlite` pure-Go), vá lỗi `flow_index.json` ghi-lại-toàn-bộ-mỗi-push (dedup giờ qua `PRIMARY KEY`/`UNIQUE` của SQLite). Tự nhập lại dữ liệu JSON cũ (nếu có) một lần khi khởi động, không mất dữ liệu cũ. `counting_data/`, `traffic_data/` vẫn chưa chuyển, để sau. |
| 1.3 | 2026-10-14 | **Đã triển khai** phần backup thật cho server Windows Server 2016 (`D:\Working\Counting\`, go2rtc chạy `.exe` trực tiếp): thêm §9 + script `scripts/backup/` (Restic, 2 repo độc lập local-HDD + NAS Synology qua SMB/CIFS, snapshot nhất quán `trafficflow.db` qua `sqlite3 .backup`, tự động qua Windows Task Scheduler). §4 rút lại thành nguyên tắc chung, trỏ sang §9 cho phần triển khai cụ thể. |
