# Plan: Tích hợp Hawkwise Advanced Traffic Density → `/api/trafficdata/hawkwise/v1/flow-push`

**Trạng thái: PLANNING — chưa code.** Tài liệu này đối chiếu 2 bản đặc tả đã nhận, chỉ ra các điểm lệch cần quyết định trước khi code, và đề xuất kiến trúc cụ thể (tái dùng tối đa hạ tầng đã có cho tích hợp OMNIA/AI Event).

---

## 0. Hai tài liệu nguồn — vai trò khác nhau

| | `HAWKWISE-3rdParty-AdvancedTrafficDensity-API.md` (v1.0, Hawkwise) | `API_luu_luong_HAWKWISE_1.0.md` (v1.1, ta tự viết) |
|---|---|---|
| Vai trò | Mô tả bản tin Hawkwise **đã đang gửi thật** (hệ thống cố định, không đổi theo yêu cầu bên nhận) | Đặc tả **ta tự đề xuất** lúc chưa biết rõ format thật của Hawkwise |
| Hệ quả | Đây là **nguồn sự thật** cho hình dạng JSON thực tế sẽ nhận được | Nhiều chỗ không khớp với thực tế — cần coi là "mong muốn ban đầu", không phải hợp đồng cuối cùng |

**Kết luận quan trọng nhất:** endpoint phải **parse đúng theo format tài liệu Hawkwise (doc 1)** — snake_case, cấu trúc lồng `general/evidence/details` — chứ không phải camelCase phẳng của doc 2. Hawkwise sẽ không đổi code gửi theo ý ta; ta phải nhận đúng cái họ đang gửi.

---

## 1. Bảng đối chiếu chi tiết (field-by-field)

### 1.1. Vỏ bọc & endpoint

| Hạng mục | Hawkwise thật (doc 1) | Đặc tả ta (doc 2) | Quyết định |
|---|---|---|---|
| URL | Do bên nhận cung cấp (không cố định) | `/api/trafficdata/hawkwise/v1/flow-push` | ✅ Dùng URL của doc 2 — đã thống nhất với bạn, không mâu thuẫn |
| Method | `POST` (cấu hình được PUT/PATCH) | `POST` | ✅ Khớp |
| Content-Type | `application/json; charset=utf-8` | `application/json; charset=utf-8` | ✅ Khớp |
| Nén | Không đề cập | Khuyến nghị `Content-Encoding: gzip` | ⚠️ Go's `net/http` server KHÔNG tự giải nén gzip request body — cần code thêm nếu muốn hỗ trợ. Hawkwise có thể không bật tính năng này (doc 1 không hề nhắc) → làm **tùy chọn, không bắt buộc**, map.html/go2rtc không cần Hawkwise bật gzip mới chạy được |
| Timeout chờ phản hồi | ~100 giây | Yêu cầu ta phản hồi ≤ 30ms | Mục tiêu 30ms là **tự đặt ra cho chính mình** (best practice), không phải Hawkwise yêu cầu cứng — nhưng nên theo vì giảm rủi ro timeout khi 1 camera cùng lúc với nhiều camera khác chốt chu kỳ |

### 1.2. Xác thực

| Hạng mục | Hawkwise thật | Đặc tả ta | Quyết định |
|---|---|---|---|
| Phương thức | Bên nhận **tự chọn**: header tùy ý (vd `X-API-Key`) HOẶC `Authorization: Bearer` | Bắt buộc `X-API-Key: <key>` | ✅ Chọn `X-API-Key` — khớp đặc tả ta, và **đúng hệt** header hệ thống Integration API Key sẵn có đang dùng cho OMNIA (`internal/auth/integrations.go`) → tái dùng 100%, không code gì mới cho phần auth |

### 1.3. Cấu trúc bản tin — khác biệt lớn nhất

| Khái niệm | Hawkwise thật | Đặc tả ta | Ghi chú |
|---|---|---|---|
| Vỏ ngoài | `{general:{...}, evidence:{...}, details:{...}}` (3 object lồng) | `{type, publishedAt, batchId, sensorId, ..., lanes:[...], summary:{...}}` (phẳng) | **Phải code theo cấu trúc lồng của Hawkwise**, không phải cấu trúc phẳng ta vẽ ra |
| Khóa chống trùng | `general.event_id` (số nguyên) + `general.event_id_string` (UUID) | `batchId` (chuỗi tự đặt quy ước) | Dùng `event_id` làm khóa dedup chính — field này Hawkwise nói rõ "dùng làm khóa chống trùng" |
| Mã điểm đo (sensor) | `general.camera.source_uuid` | `sensorId` | Map `source_uuid` → khái niệm "sensorId" nội bộ của ta |
| Mã nút giao | **Không có** trường tương đương | `intersectionId` | Hawkwise không gửi — nếu cần, ta tự quản lý bảng ánh xạ `source_uuid → intersectionId` phía mình (xem mục 4) |
| Thời gian | Chuỗi local `"yyyy-MM-dd HH:mm:ss"`, **giờ VN (GMT+7), không có hậu tố múi giờ** | RFC 3339 UTC (`"2026-10-12T15:30:00Z"`) | ⚠️ **Khác định dạng hoàn toàn.** Phải parse bằng `time.ParseInLocation("2006-01-02 15:04:05", s, vnLocation)` — đúng pattern `vnLocation` đã dùng ở `internal/incidents`/`internal/aievent`, **không phải** `time.Parse(time.RFC3339, s)` |
| Tọa độ | `general.camera.longitude` / `latitude` (có thể `null`) | `location.x` / `location.y` | Map trực tiếp, chấp nhận `null` (giữ nguyên quy ước AI Event: tọa độ `0,0`/`null` = không hiển thị trên bản đồ) |
| Theo làn | `details.density_detail.partial_data[]` — field `id` là **số thứ tự vùng vẽ khi cấu hình camera, KHÔNG theo quy ước đánh số từ dải phân cách** | `lanes[].laneId` + `movement` (THROUGH/LEFT/RIGHT/U_TURN) | Hawkwise **không gửi hướng rẽ** (`movement`) — chỉ có `id` số, ý nghĩa tùy từng camera. Nếu cần hiển thị hướng rẽ, phải có bảng cấu hình thủ công phía ta (Hawkwise không cung cấp) |
| Theo loại xe | `details.density_detail.data[]` — mỗi phần tử `{label, volume, avg_speed, class_id}` | `lanes[].counts.{motorcycle,car,bus,truck,other}` (object cố định field) | Hawkwise trả **mảng động** (label là string tự do, danh sách loại xe phụ thuộc cấu hình từng camera) — **không được hard-code 4 field cố định** như đặc tả ta; phải lưu dạng mảng/map linh hoạt và **bỏ qua không báo lỗi** khi gặp `label` lạ (đúng yêu cầu rõ ràng ở mục 5.4 tài liệu Hawkwise) |
| PCU (xe con quy đổi) | **Không gửi sẵn** — chỉ gợi ý công thức tự tính (mục 7: xe máy×0,3 + ô tô×1,0 + (tải+buýt)×2,5) | `totalPcu` gửi sẵn trong mỗi lane + summary | Phải **tự tính PCU phía ta** khi lưu/hiển thị, Hawkwise không cung cấp |
| `queueLength` (chiều dài hàng chờ) | **Không có trong dữ liệu** | Có trong mỗi lane | Field này **không tồn tại trong thực tế** — bỏ khỏi schema lưu trữ hoặc để `null` vĩnh viễn |
| `occupancy` (tỷ lệ chiếm dụng) | `avg_occupancy` (toàn mặt cắt) + `partial_data[].occupancy` (theo làn) — **thang giá trị chưa xác nhận** (0–1 hay 0–100%, tài liệu Hawkwise tự ghi "CẦN XÁC NHẬN") | `occupancyRate`, 0.00–1.00 | ⚠️ **Cần hỏi Hawkwise xác nhận đơn vị thật** trước khi hiển thị số liệu này ra UI — tài liệu nguồn của chính Hawkwise còn đang bỏ ngỏ |
| `avg_speed` đơn vị | Tài liệu Hawkwise cũng tự ghi "CẦN XÁC NHẬN: km/h" | km/h (khẳng định chắc) | ⚠️ Tương tự — cần Hawkwise xác nhận bằng văn bản trước khi hiển thị đơn vị cụ thể trên dashboard |
| Trạng thái giao thông | `details.traffic_status`: `"FreeFlow"` / `"HeavyTraffic"` / `"CongestedFlow"` / `null` | **Không có field tương đương** | Dữ liệu quý — đặc tả ta bị thiếu hẳn, phải bổ sung vào schema lưu trữ |
| Theo dõi đợt ùn tắc | `general.severity_level` (0/1) + `general.event_state` (null/3/5) — Hawkwise tự theo dõi 1 "đợt ùn tắc" liên tục qua nhiều chu kỳ | **Không có field tương đương** | Dữ liệu quý thứ 2 bị thiếu ở đặc tả ta — nên giữ lại để sau này có thể cảnh báo/đếm số đợt ùn tắc theo camera |
| Ảnh minh chứng | `evidence.frame_url` (link ảnh, có thể `null`/rỗng) | **Không có field tương đương** | Nên giữ lại — tận dụng được hạ tầng hiển thị ảnh đã có sẵn cho AI Event (`internal/aievent/images.go`), dù ở đây chỉ là link chứ không phải ảnh base64 cần tải về |

### 1.4. Chống trùng & gửi bù — khác biệt vận hành quan trọng

| | Hawkwise thật | Đặc tả ta |
|---|---|---|
| Chống trùng | Dedup theo `event_id`; nhận trùng → **không cộng dồn**, vẫn trả `2xx` | Dedup theo `batchId`; y hệt về nguyên tắc — chỉ khác tên khóa |
| **Đệm cục bộ khi mất kết nối kéo dài** | ❌ **"Hawkwise không lưu đệm dài hạn để gửi bù khi mất kết nối kéo dài"** (trích nguyên văn mục 1) | ✅ Giả định có: hàng đợi FIFO cục bộ tối thiểu **24 giờ**, backoff `3s→10s→30s→60s` |
| Số lần gửi lại | "Tối đa N lần, cách nhau T giây/phút" — **N, T không cố định, thống nhất lúc cấu hình tích hợp** (không đọc được `Retry-After`) | Backoff cụ thể 4 bước, có đọc `Retry-After` cho `429` |

⚠️ **Đây là lệch quan trọng nhất về mặt vận hành**: đặc tả ta kỳ vọng Hawkwise tự bù dữ liệu 24h khi mất mạng — nhưng tài liệu thật của Hawkwise nói **họ không làm việc này**. Nếu endpoint của ta down/lỗi quá số lần retry ngắn hạn (N lần, T giây — con số cụ thể cần hỏi lại đội Hawkwise lúc cấu hình), **dữ liệu của chu kỳ đó mất vĩnh viễn, không có cách lấy lại**. → Endpoint của ta cần ưu tiên **độ sẵn sàng cao** hơn là dựa vào cơ chế gửi bù phía đối tác.

### 1.5. Phản hồi & mã lỗi

| | Hawkwise thật | Đặc tả ta |
|---|---|---|
| Hawkwise đọc response body? | **Không** — chỉ quan tâm mã HTTP (`2xx` = OK, khác = thất bại) | Định nghĩa body JSON chi tiết `{status, batchId/eventId, receivedAt}` |
| Ý nghĩa | → Response body của ta **chỉ phục vụ log/debug nội bộ**, không ảnh hưởng gì tới hành vi retry thật của Hawkwise | Các mã lỗi (`invalid_json`, `missing_required_field`...) vẫn hữu ích để ta tự log và vận hành, giữ lại dùng nội bộ |

---

## 2. Kiến trúc đề xuất — tái dùng tối đa hạ tầng OMNIA/AI Event đã có

Đội dò (Explore agent) đã rà soát kỹ `internal/aievent` (tích hợp push tương tự, đã chạy ổn định) và `internal/counting` (module đếm xe hiện tại). Kết luận: **tái dùng gần như toàn bộ phần xác thực**, thiết kế schema lưu trữ riêng cho phù hợp dữ liệu dạng chuỗi thời gian (time-series).

### 2.1. Xác thực — không cần code gì mới

Hệ thống Integration API Key (`internal/auth/integrations.go`) đã hỗ trợ sẵn **nhiều tích hợp độc lập, mỗi cái gán 1 `AllowedPath` riêng** — đúng mô hình cần: chỉ cần

1. Tạo 1 bản ghi Integration mới qua admin UI, `AllowedPath = "/api/trafficdata/hawkwise/v1/flow-push"`.
2. Thêm path này vào `isPublicPath` (`internal/auth/middleware.go`).
3. Handler gọi đúng pattern đã có:
   ```go
   ip := auth.ClientIP(r)
   if locked, _ := auth.IntegrationAuthLocked(ip); locked { /* 429 */ }
   key := r.Header.Get("X-API-Key")
   integration, ok := auth.ValidateIntegrationKey(r.URL.Path, key, ip)
   if !ok { auth.RecordIntegrationAuthFailure(ip); /* 401 */ }
   auth.RecordIntegrationAuthSuccess(ip)
   ```
   (y hệt `internal/aievent/api.go` dòng ~158-178)

### 2.2. Package mới: `internal/trafficflow` (tên tạm, có thể đổi)

Không gắn vào `internal/aievent` (model "sự kiện mở/đóng" không khớp — đây là time-series liên tục, không phải sự kiện rời rạc) và không gắn thẳng vào `internal/counting` (model `Slot5` hiện tại không có occupancy/traffic_status/severity/theo làn chi tiết, và `counting` chưa từng nhận push từ hệ thống ngoài). → Package riêng, nhưng **vay mượn quy ước** từ cả hai:

- **Quy ước `vnLocation`** (copy y hệt `internal/aievent`/`internal/incidents`): parse `start_time`/`end_time` bằng `time.ParseInLocation("2006-01-02 15:04:05", s, vnLocation)`.
- **Quy ước lưu trữ** (copy y hệt `internal/aievent`): JSON theo ngày VN-local (`flow_<YYYY-MM-DD>.json`) + `record_index.json` (map `event_id → ngày`) để dedup xuyên suốt restart server, không cần tải hết dữ liệu vào RAM.

### 2.3. Struct nhận request (khớp đúng field Hawkwise thật — không phải đặc tả ta)

```go
type hawkwisePush struct {
    General struct {
        TriggerCode   string   `json:"trigger_code"`
        EventID       int64    `json:"event_id"`        // khóa dedup
        EventIDString string   `json:"event_id_string"`
        SeverityLevel int      `json:"severity_level"`
        EventState    *int     `json:"event_state"`      // null | 3 | 5
        StartTime     string   `json:"start_time"`        // "2006-01-02 15:04:05" VN local
        EndTime       string   `json:"end_time"`
        AIFlowCode    int      `json:"ai_flow_code"`
        Camera struct {
            SourceUUID   string   `json:"source_uuid"`    // = mã điểm đo (sensor)
            HardwareUUID string   `json:"hardware_uuid"`
            Address      *string  `json:"address"`
            Longitude    *float64 `json:"longitude"`
            Latitude     *float64 `json:"latitude"`
        } `json:"camera"`
    } `json:"general"`
    Evidence struct {
        FrameURL *string `json:"frame_url"`
    } `json:"evidence"`
    Details struct {
        TrafficStatus *string `json:"traffic_status"` // FreeFlow | HeavyTraffic | CongestedFlow | null
        DensityDetail struct {
            AvgOccupancy float64 `json:"avg_occupancy"`
            AvgSpeed     float64 `json:"avg_speed"`
            Total        int     `json:"total"`
            Data []struct {
                Label    string  `json:"label"`     // KHÔNG hard-code enum — chấp nhận mọi chuỗi
                Volume   int     `json:"volume"`
                AvgSpeed float64 `json:"avg_speed"`
            } `json:"data"`
            PartialData []struct {
                ID        int      `json:"id"`        // số thứ tự vùng vẽ, không phải thứ tự làn chuẩn
                Occupancy float64  `json:"occupancy"`
                AvgSpeed  *float64 `json:"avg_speed"`
                Volume    int      `json:"volume"`
                Data []struct {
                    Label    string  `json:"label"`
                    Volume   int     `json:"volume"`
                    AvgSpeed float64 `json:"avg_speed"`
                } `json:"data"`
            } `json:"partial_data"`
        } `json:"density_detail"`
    } `json:"details"`
}
```

### 2.4. Validate theo đúng mục 6 tài liệu Hawkwise

- Bắt buộc có: `general.event_id`, `general.camera.source_uuid`, `general.start_time`, `general.end_time`.
- `general.trigger_code != "AdvancedTrafficDensity"` → log cảnh báo, vẫn chấp nhận (Hawkwise có thể mở rộng thêm luồng AI khác sau này dùng chung endpoint) hoặc từ chối rõ ràng — **cần bạn quyết định** (xem câu hỏi mục 5).
- `details.density_detail.data[].label` lạ (không trong bảng 5.4) → **chấp nhận, không báo lỗi** (đúng yêu cầu rõ ràng của Hawkwise).
- Không kiểm tra cứng field ghi "Không" ở cột "Luôn có giá trị" — None là hợp lệ.

### 2.5. Idempotency

Dedup theo `general.event_id` — y hệt mẫu `aievent.Ingest()`: nhận trùng `event_id` → **không ghi đè/cộng dồn số liệu**, trả `2xx` luôn (per mục 6.1 tài liệu Hawkwise).

### 2.6. Response (phục vụ log nội bộ, Hawkwise không đọc)

```json
{"status":"ok","event_id":482558,"received_at":"2026-10-13T08:00:00+07:00"}
```
Lỗi dùng lại khuôn `writeError` sẵn có của `aievent/api.go` (`{"status":"error","error":{"code","message","field"}}`).

### 2.7. Admin UI

Nhân bản đúng section `aieventSection` (`admin.html` dòng 1196–1313): 1 tab menu mới + bảng quản lý Integration Key (lọc theo `AllowedPath` mới) + công cụ "🧪 Test API Push" (POST thử với mẫu JSON từ chính tài liệu Hawkwise, không phải mẫu camelCase cũ).

---

## 3. Những gì đặc tả cũ (doc 2) của bạn KHÔNG dùng được nguyên trạng

Tóm lại để bạn dễ quyết: toàn bộ phần **giao thức/kết nối/auth** của doc 2 (POST, JSON, `X-API-Key`, idempotency nguyên tắc) **giữ nguyên tinh thần, đổi tên field**. Toàn bộ phần **hình dạng payload cụ thể** (`lanes[].counts.{...}`, `totalPcu`, `queueLength`, `movement`, `summary.reliability`...) **không khớp thực tế**, phải bỏ hoặc thiết kế lại theo đúng mục 1 ở trên.

---

## 4. Câu hỏi còn treo — cần bạn quyết định trước khi code

1. **Hiển thị lên đâu?** Chỉ lưu trữ dữ liệu (phục vụ xuất báo cáo/API nội bộ sau này), hay cần hiện ngay thành 1 layer mới trên `map.html` (kiểu như 📊 Trạm đo hiện có) / 1 trang dashboard riêng?
2. **Số lượng camera Hawkwise thực tế** sẽ kết nối ở giai đoạn đầu là bao nhiêu? (Quyết định cỡ rate-limit, cách lưu trữ theo ngày có cần tối ưu thêm không — tài liệu doc 2 giả định 1.000 camera nhưng chưa rõ đây có phải số thật của triển khai này không.)
3. **Thời gian lưu giữ dữ liệu** (retention) bao nhiêu ngày? (`internal/counting` đang mặc định 30 ngày cho dữ liệu đếm xe tự thân — có thể dùng chung mốc này hay cần khác.)
4. **`avg_speed` (km/h?) và `avg_occupancy` (0–1 hay %?)** — chính tài liệu Hawkwise cũng ghi "CẦN XÁC NHẬN". Cần hỏi lại đội Hawkwise bằng văn bản trước khi đưa số liệu này lên UI, tránh hiển thị sai đơn vị.
5. **`trigger_code` lạ** (không phải `"AdvancedTrafficDensity"`) → từ chối hẳn hay log cảnh báo rồi vẫn nhận? (Ảnh hưởng tới việc endpoint này có dùng chung cho các luồng AI khác của Hawkwise sau này không.)
6. **Số lần/khoảng cách gửi lại (N, T)** phía Hawkwise khi endpoint lỗi — tài liệu nói "thống nhất khi cấu hình tích hợp" — cần xin con số cụ thể từ đội Hawkwise để biết endpoint của ta có bao lâu để tự phục hồi trước khi mất vĩnh viễn 1 chu kỳ dữ liệu (nhắc lại: Hawkwise **không** đệm bù dài hạn).

---

## Phụ lục: Lịch sử phiên bản

| Phiên bản | Ngày | Nội dung |
|---|---|---|
| 1.0 | 2026-10-13 | Đối chiếu 2 tài liệu đặc tả, đề xuất kiến trúc tích hợp. Trạng thái: planning, chưa code. |
