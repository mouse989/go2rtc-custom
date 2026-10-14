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
| **trafficflow (Đo đếm lưu lượng)** | `trafficflow_data/flow_<source_uuid>_<ngày>.json` + `flow_index.json` + sensor mapping | 1 file/(sensor,ngày) | **có — 30 ngày**, quét mỗi 6h | **cao nhất trong hệ thống**: ~500 camera × 288 chu kỳ/ngày ≈ 144.000 bản ghi/ngày |
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
├── trafficflow_data/
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

1. **6 loại log chưa có retention, tăng vô hạn theo thời gian**: `device_events.jsonl`, `login_history/`, `security_alerts/` (phần log sự kiện), `user_locations/`, `access_log/`, `travel_logs/`. Nếu không dọn, dung lượng cần backup sẽ phình to dần vô thời hạn — xem khuyến nghị §6.

2. **Một số secret lưu dạng plaintext, chưa mã hoá tại rest**: `VietmapAPIKey`/`VietmapLiveTrafficAPIKey` (trong `settings.json`), Telegram bot token (trong `traffic.json`), `Worker.Username`/`Password` (trong `workers.json`). Bản backup chứa các file này **cần được mã hoá khi lưu ra ngoài máy** (đặc biệt nếu đẩy lên cloud/NAS chia sẻ).

3. **⚠️ Quan trọng nhất: `.jwt_secret` và `integrations.json` phải luôn được backup/khôi phục CÙNG MỘT thời điểm, không trộn lẫn phiên bản khác nhau.** Lý do: khóa dùng để mã hoá lại (`KeyEnc`, phần cho phép admin "xem lại" API key) được dẫn xuất trực tiếp từ `.jwt_secret` (`HMAC-SHA256(jwtSecretRef, "integration-key-enc-v1")`). Nếu phục hồi `integrations.json` của một bản backup cũ nhưng `.jwt_secret` đã bị thay bằng bản mới (hoặc ngược lại), các API key cũ vẫn **xác thực bình thường** (vì `KeyHash` là SHA-256 độc lập) nhưng chức năng "xem lại key" sẽ hỏng vĩnh viễn cho các integration đó. Ngoài ra, mất `.jwt_secret` còn làm mọi JWT token hiện tại bị vô hiệu (user phải đăng nhập lại) — không nguy hiểm nhưng cần biết trước.

4. **Không có transaction/khoá ghi**: mọi file JSON được ghi bằng `os.WriteFile` (ghi đè toàn file), không có cơ chế flush/lock để tạm dừng ghi trước khi backup. Rủi ro đọc trúng 1 file đang ghi nửa chừng là có thật nhưng thấp (ghi diễn ra rất nhanh, không có 2 tiến trình ghi đồng thời cùng file) — xem cách giảm rủi ro ở §4.

---

## 4. Đề xuất kế hoạch backup

- **Phạm vi**: toàn bộ thư mục chứa `go2rtc.yaml` (một thư mục duy nhất, theo đúng cấu trúc ở §1.2).
- **Loại trừ** (giảm dung lượng, giá trị thấp): `aievent_data/images/` (Nhóm C).
- **Tần suất**:
  - Backup đầy đủ: **1 lần/ngày**, vào giờ ít tải (ví dụ 02:00 sáng).
  - Nếu cần RPO ngắn hơn cho dữ liệu Nhóm A (cấu hình/user ít thay đổi nhưng quan trọng): có thể thêm 1 bản backup nhẹ (chỉ các file Nhóm A, không kèm `*_data/` time-series) mỗi vài giờ.
- **Phương pháp**: đóng gói `tar.gz` → **mã hoá** (gpg hoặc `age`) → đẩy ra ngoài máy bằng `rsync`/`rclone`. Để giảm rủi ro đọc file đang ghi nửa chừng (mục 4 ở §3): dùng snapshot filesystem (LVM/ZFS/btrfs snapshot) nếu hạ tầng có hỗ trợ, trước khi tar; nếu không có, chấp nhận rủi ro thấp còn lại (đã phân tích ở trên) và dùng `tar --ignore-failed-read` để một file lỗi hiếm gặp không làm hỏng cả bản backup.
- **Nơi lưu**: nguyên tắc 3-2-1 — ít nhất 1 bản sao cục bộ (đĩa khác), 1 bản off-site (NAS/S3/SFTP...); lưu dạng **versioned** (không ghi đè bản cũ ngay lập tức).
- **Retention bản backup** (đề xuất, có thể chỉnh theo nhu cầu thực tế): giữ 7 bản ngày gần nhất + 4 bản cuối-tuần gần nhất + 3 bản cuối-tháng gần nhất — giảm dần dung lượng theo thời gian mà vẫn có điểm khôi phục xa.

## 5. Khôi phục (restore)

- Restore = giải mã + giải nén đúng 1 bản backup vào **đúng đường dẫn** (thư mục chứa `go2rtc.yaml`, trùng với `app.ConfigPath` lúc backup) → không cần sửa code gì, mọi module tự load lại dữ liệu khi service khởi động lại.
- **Bắt buộc**: `.jwt_secret` và `integrations.json` phải phục hồi từ **cùng một bản backup** (xem lý do ở §3 mục 3).
- Nên **test restore định kỳ** (ví dụ mỗi quý) trên 1 server/staging riêng để xác nhận bản backup dùng được thật — "tồn tại" không đồng nghĩa "khôi phục được".

## 6. Khuyến nghị sửa kèm theo (nên làm trước khi backup đi vào vận hành chính thức)

1. **Thêm retention cho 6 loại log hiện chưa có** (§3 mục 1) — nếu không, backup sẽ ngày càng nặng và tốn chi phí lưu trữ off-site vô ích. Có thể dùng đúng mẫu `deleteOlderThan`/sweeper đã có sẵn trong `internal/counting/storage.go`, `internal/trafficflow/records.go`.
2. **Xem xét mã hoá VietMap API key / Telegram token / worker password tại rest** (hiện đang plaintext) — quan trọng hơn nếu backup được đẩy lên cloud/nơi lưu chia sẻ.

## 7. Việc cần bạn quyết định trước khi triển khai

1. Server đang chạy trên hạ tầng gì (bare-metal/VM/container)? Có sẵn LVM/ZFS/btrfs snapshot không? → quyết định cách lấy bản chụp nhất quán.
2. Đã có nơi lưu off-site chưa (S3, NAS khác, SFTP server khác...)?
3. RPO/RTO mong muốn: chấp nhận mất tối đa bao nhiêu giờ dữ liệu? Cần khôi phục xong trong bao lâu?
4. Muốn tự viết script backup (ví dụ `scripts/backup.sh` + cron, dễ tuỳ biến) hay dùng công cụ backup có sẵn (Restic, BorgBackup — có mã hoá + dedup + versioning tích hợp sẵn, ít phải tự code)?

---

## Phụ lục: Lịch sử phiên bản

| Phiên bản | Ngày | Nội dung |
|---|---|---|
| 1.0 | 2026-10-14 | Rà soát toàn bộ dữ liệu + cách tổ chức hiện có, phân loại theo mức độ quan trọng, đề xuất kế hoạch backup. Trạng thái: planning, chưa triển khai. |
