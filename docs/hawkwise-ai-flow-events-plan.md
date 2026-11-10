# Plan: Bản tin AI-flow rời rạc của Hawkwise (vd. "AbnormalStop") — hiện đang bị mất dữ liệu

**Trạng thái: PLANNING — chưa code.** Phát hiện từ bản tin thật người dùng gửi (sự kiện `AbnormalStop`, `event_id 482559`, camera "Ngã tư Hàng Xanh"): Hawkwise gửi **ít nhất 2 loại bản tin khác nhau** tới cùng 1 endpoint vật lý `/api/trafficdata/hawkwise/v1/flow-push`, nhưng package `internal/trafficflow` hiện tại **chỉ parse đúng 1 loại** (`AdvancedTrafficDensity`, xem `docs/hawkwise-flow-integration-plan.md`). Loại còn lại đang bị **mất dữ liệu âm thầm**.

---

## 1. Phát hiện: bản tin AI-flow không khớp bất kỳ schema nào đang có

So bản tin mẫu thật (`trigger_code: "AbnormalStop"`) với 2 struct Go hiện có:

| | `hawkwisePush` (`internal/trafficflow/api.go`) | `pushRequest` (`internal/aievent/api.go`, OMNIA) | Bản tin AbnormalStop thật |
|---|---|---|---|
| Vỏ ngoài | `general/evidence/details`, snake_case | `type/recordId/category/event`, camelCase ngoài + PascalCase trong `event` | `general/evidence/details`, snake_case — **khớp cột 1 ở phần vỏ** |
| `general.trigger_code` | kỳ vọng `"AdvancedTrafficDensity"` | không có field này | `"AbnormalStop"` |
| `details.*` | `density_detail.{avg_occupancy, avg_speed, total, data[], partial_data[]}` | không áp dụng | `{owner_id, owner_name, profile_object_id, type, brand_code, color_code, name (biển số), has_lp_detected, helmet_wearing_result, is_weapon_detection, weapon_detection_classes, passenger_limit_exceeded_result, ...}` — **hoàn toàn khác** `density_detail` |
| `category.typeId/subtypeId` | không áp dụng | **bắt buộc**, khác 0 | không có field tương đương — có `general.ai_flow_code` (21) + `general.ai_flow_name` ("Abnormal Stop") |
| `recordId` | không áp dụng | **bắt buộc**, không rỗng | không có field tên `recordId` — có `general.event_id` (số) + `general.event_id_string` (UUID) |

**Hệ quả khi bản tin AbnormalStop đi vào `/api/trafficdata/hawkwise/v1/flow-push` hiện tại:**

`encoding/json` của Go **không báo lỗi** khi JSON có field lạ không khớp struct — nó chỉ bỏ qua. Vậy nên:
- `validatePush` vẫn pass (vì `event_id`, `camera.source_uuid`, `start_time`, `end_time` đều có mặt — 4 field bắt buộc duy nhất).
- Request được lưu thành **1 `flow_record` với toàn bộ số liệu đếm xe = 0** (`avg_occupancy=0, avg_speed=0, total=0, data=[], partial_data=[]`) vì `details.density_detail` không tồn tại trong bản tin này.
- Toàn bộ dữ liệu thật sự có giá trị — biển số xe (`details.name`), chủ xe, hãng/màu xe, điểm tin cậy AI (`general.confident_score`), ảnh hiện trường (`evidence.frame_url`, `body_crop_url`), tên luồng AI (`general.ai_flow_name`) — **không có field nào trên struct Go để nhận**, nên bị **loại bỏ vĩnh viễn ngay tại bước decode**, không log, không lưu ở đâu cả.

→ **Đây không phải vấn đề hiển thị sai chỗ — đây là mất dữ liệu.** Ưu tiên xử lý cao hơn việc chỉ "định tuyến lại cho đúng trang".

---

## 2. Vì sao không thể chỉ đổi URL đích sang `/api/aievent/omnia/v1/push`

Ý định của bạn ("những sự cố này tôi đang bố trí nhận bên AI Event") đúng về mặt **khái niệm** — một sự kiện rời rạc do AI phát hiện, có `start_time`/`end_time`, có ảnh minh chứng, đúng là bản chất giống OMNIA AI Event hơn là số liệu đếm xe định kỳ. Nhưng **không thể chỉ đổi đường dẫn nhận** vì:

- `pushRequest` (OMNIA) đòi `recordId` không rỗng và `type` phải là `"event.created"`/`"event.terminated"` — bản tin Hawkwise không có 2 field này đúng tên/giá trị đó → **sẽ bị từ chối ngay (400)** nếu Hawkwise gửi thẳng JSON hiện tại tới endpoint OMNIA.
- Hawkwise là hệ thống cố định phía đối tác — **không đổi format theo yêu cầu bên nhận** (kết luận đã chốt ở `docs/hawkwise-flow-integration-plan.md` mục 0).

→ Muốn 2 hệ thống gặp nhau, **phía ta phải viết bộ chuyển đổi (adapter)** đọc đúng format Hawkwise rồi "dịch" sang mô hình `AIEvent` nội bộ — không có cách nào bỏ qua bước này.

---

## 3. Bức tranh tổng thể: đang có (và sẽ có) mấy luồng dữ liệu?

| Luồng | Vendor | Endpoint hiện tại | Bản chất dữ liệu | Package xử lý | Lưu trữ | Hiển thị |
|---|---|---|---|---|---|---|
| 1 | OMNIA / FPT VDS | `/api/aievent/omnia/v1/push` | Sự kiện AI rời rạc, có mở/đóng (`event.created`/`event.terminated`), category `typeId/subtypeId`, ảnh base64 đính kèm | `internal/aievent` | JSON theo ngày | Map marker "🤖 AI Event", popup category+ảnh |
| 2 | Hawkwise | `/api/trafficdata/hawkwise/v1/flow-push` | Số liệu đếm xe **định kỳ** (`trigger_code = AdvancedTrafficDensity`), time-series liên tục theo camera | `internal/trafficflow` | SQLite | Map pill "📊 Đo đếm lưu lượng", tô màu theo `traffic_status` |
| 3 | Hawkwise | **cùng URL với luồng 2, nhưng lệch hẳn bản chất** | Sự kiện AI rời rạc (`trigger_code = AbnormalStop`, và khả năng cao còn nhiều `ai_flow_code` khác — xem mục 5) — có nhận diện phương tiện (biển số/hãng/màu/chủ xe), ảnh hiện trường qua URL (không phải base64) | **chưa có** — đang rơi vào (2) và mất dữ liệu | — | — |

**Luồng 3 về bản chất giống luồng 1 hơn luồng 2** (sự kiện rời rạc do AI phát hiện, không phải số liệu đếm định kỳ) — nên đích đến hợp lý là mô hình `AIEvent`, không phải `trafficflow`. Nhưng **vỏ bọc JSON của luồng 3 giống luồng 2** (snake_case, `general/evidence/details`, `event_id` số nguyên) chứ không giống luồng 1 (camelCase, `recordId`, `category.typeId`). Nói cách khác: **phân loại theo vendor (ai gửi) và phân loại theo bản chất dữ liệu (nó là gì) không trùng nhau** — đây là lý do cần thiết kế rõ ràng thay vì chỉ "đổi URL".

---

## 4. Hai câu hỏi định tuyến cần quyết định

### 4.1. Một URL hay hai URL cho Hawkwise?

**Phương án A — tách URL riêng theo loại bản tin** (vd. thêm `/api/aievent/hawkwise/v1/push` cho các `trigger_code` dạng AI-flow rời rạc, giữ `/api/trafficdata/hawkwise/v1/flow-push` chỉ nhận `AdvancedTrafficDensity`):
- ✅ Sạch nhất về kiến trúc — đúng tinh thần `AllowedPath`-prefix của hệ Integration Key hiện có (1 URL = 1 hợp đồng dữ liệu rõ ràng), dễ audit/log riêng từng luồng.
- ⚠️ **Phụ thuộc Hawkwise/Elcom có hỗ trợ cấu hình gửi các `trigger_code` khác nhau tới URL khác nhau hay không** — chưa xác nhận được, cần hỏi đội Hawkwise lúc cấu hình tích hợp (giống câu hỏi còn treo #3 trong `docs/hawkwise-flow-integration-plan.md`, giờ đã có ví dụ thật để hỏi cụ thể hơn).

**Phương án B — giữ nguyên 1 URL, phân nhánh theo `general.trigger_code` ngay trong handler:**
- `trigger_code == "AdvancedTrafficDensity"` → xử lý như hiện tại (không đổi gì).
- `trigger_code` khác (bắt đầu từ allow-list: `"AbnormalStop"`, mở rộng dần khi phát hiện loại mới — theo đúng mô hình `ObservedCategory`/`observed_sensors` đã dùng: tự ghi nhận loại lạ, không chặn cứng) → parse theo schema AI-flow (mục 5) rồi gọi sang pipeline `aievent`.
- ✅ **Không phụ thuộc gì phía Hawkwise** — họ không cần đổi cấu hình, vẫn gửi đúng 1 URL như hiện tại. An toàn hơn để bắt đầu ngay.
- ⚠️ 1 handler phải tự phân biệt 2 schema con hoàn toàn khác nhau trong cùng field `details` — cần decode "thăm dò" (`trigger_code` trước, rồi mới quyết định decode `details` theo schema nào) thay vì decode thẳng vào 1 struct cố định như hiện tại.

**Khuyến nghị:** làm **Phương án B trước** (không chờ xác nhận từ Hawkwise, chặn được mất dữ liệu ngay), đồng thời hỏi đội Hawkwise/Elcom xem Phương án A có khả thi không — nếu có, chuyển sang sau cũng không tốn công viết lại phần lõi (bộ parse/adapter ở mục 5 dùng được cho cả 2 phương án, chỉ khác chỗ nó được gọi từ route nào).

### 4.2. Dữ liệu AI-flow của Hawkwise lưu/hiển thị ở đâu?

Khuyến nghị: **tái dùng `internal/aievent`** (không tạo phân hệ song song thứ 3), vì:
- Model "sự kiện rời rạc, có thời điểm bắt đầu/kết thúc, có category, có ảnh, hiển thị marker trên bản đồ" đã có sẵn và đang chạy ổn định cho OMNIA.
- Tái dùng được: store theo ngày, dedup theo RecordID, hệ category-rule (admin tự đặt icon/nhãn cho từng loại — ở đây là theo `ai_flow_code`/`ai_flow_name` thay vì `typeId/subtypeId`), layer "🤖 AI Event" trên `map.html`, cơ chế `ObservedCategory` để tự ghi nhận loại `ai_flow_code` mới chưa cấu hình.
- Người vận hành chỉ cần nhìn **1 chỗ duy nhất** ("AI Event") cho mọi sự kiện AI rời rạc, bất kể từ OMNIA hay Hawkwise — đúng tinh thần "nguồn khác nhau, cùng 1 loại dữ liệu → cùng 1 nơi quản lý" mà hệ Integration Key cũng đang theo (nhiều tích hợp, 1 bảng quản lý).

Vấn đề cần xử lý thêm khi gộp: `AIEvent` hiện tại **không có chỗ chứa** các field nhận diện phương tiện (biển số, hãng, màu, chủ xe, mũ bảo hiểm, vũ khí, quá tải khách) — đây là dữ liệu OMNIA không có nhưng Hawkwise AI-flow có. Đề xuất: thêm 1 trường **tùy chọn** trên `AIEvent`, vd `VehicleDetail *VehicleInfo` (chỉ có giá trị khi nguồn là Hawkwise, OMNIA để `nil` như cũ) — không phá cấu trúc hiện có, không ảnh hưởng OMNIA, chỉ hiển thị thêm trong popup map khi có dữ liệu. Ảnh: Hawkwise gửi **URL** (`evidence.frame_url`, không phải base64 như OMNIA) — đơn giản hơn nhiều (không cần decode/validate/lưu file), nhưng cần xác nhận URL đó (`http://10.12.7.21:9490/...`) **trình duyệt người dùng có truy cập được không** (có thể là địa chỉ nội bộ NVR, chỉ server go2rtc truy cập được chứ không phải máy người xem) — nếu đúng vậy, cần server **proxy tải ảnh hộ** thay vì nhúng thẳng URL vào `<img>`.

---

## 5. Những điều chưa biết — cần xác nhận trước khi chốt thiết kế field-by-field

Bản tin mẫu chỉ có 1 sự kiện (`AbnormalStop`) — không đủ để suy ra toàn bộ enum. Trước khi code phần adapter cần biết thêm (hỏi đội Hawkwise/Elcom, hoặc chờ gom thêm vài bản tin mẫu thật):

1. **`ai_flow_code`/`trigger_code` còn những giá trị nào khác** ngoài `AdvancedTrafficDensity` (21=Abnormal Stop)? Hawkwise có tài liệu liệt kê đủ các "luồng AI" (illegal parking, đi ngược chiều, vượt tốc độ, v.v.) mà camera có thể bật không? → quyết định allow-list ban đầu cho Phương án B, và thiết kế category-rule mapping tương tự OMNIA.
2. **`event_type` (2), `object_action` (0), `event_state` (3)** trong bản tin mẫu — ý nghĩa từng mã số là gì? (`event_state` ở luồng density đã biết null/3/5 = đợt ùn tắc đang diễn ra/kết thúc; ở đây có cùng ý nghĩa không hay là 1 enum khác hoàn toàn cho nhóm AI-flow?)
3. **`profile_object_groups`** (vd "VEHICLE - NOT REGISTERED") — đây có phải danh sách cố định hay tùy biến theo cấu hình hệ thống Hawkwise? Có cần hiển thị/lọc theo nhóm này không?
4. **Ảnh (`frame_url`/`body_crop_url`/`identified_crop_url`/`video_url`)** — các URL này server go2rtc có pull được trực tiếp không (cùng mạng nội bộ), hay cần Hawkwise cấp thêm access/token riêng để tải?
5. **Có cần dedup theo `event_id` giống luồng density không**, hay mỗi bản tin AI-flow luôn là 1 sự kiện mới độc lập (không có khái niệm "cập nhật lại 1 sự kiện đang mở" như OMNIA's `event.created`→`event.terminated`)? Bản tin mẫu có cả `start_time` và `end_time` đã điền sẵn (chênh 5 giây) — gợi ý đây là bản tin **đã hoàn chỉnh ngay từ lần gửi đầu**, không có bước "mở rồi đóng sau" như OMNIA.

---

## 6. Câu hỏi sản phẩm — cần bạn quyết định (không phải câu hỏi kỹ thuật)

1. Các sự kiện AI-flow của Hawkwise (AbnormalStop và tương tự) hiển thị **chung 1 layer "🤖 AI Event" với OMNIA** trên bản đồ, hay **tách layer riêng** (vd "🚨 Hawkwise AI Flow") để phân biệt nguồn?
2. Có cần đẩy các sự kiện này vào sổ **"Sự cố giao thông"** (`internal/incidents`, nơi CSGT xem/xác nhận/xử lý thủ công) để có luồng con người duyệt lại, hay chỉ cần **hiển thị tự động** trên bản đồ như AI Event hiện tại (không ai phải bấm xác nhận gì)?
3. Dữ liệu biển số/chủ xe có cần **tra cứu/lọc được sau này** (vd "tìm tất cả sự kiện liên quan biển số 98A-277.06") hay hiển thị trong popup khi xem sự kiện là đủ cho nhu cầu hiện tại?

---

## 7. Đề xuất các bước triển khai (khi đã chốt phương án)

Thứ tự ưu tiên theo mức độ cấp bách (mục 1 chặn mất dữ liệu đang diễn ra):

1. **Chặn mất dữ liệu ngay** (Phương án B, mục 4.1): sửa handler `/api/trafficdata/hawkwise/v1/flow-push` để nhận diện `trigger_code != "AdvancedTrafficDensity"` → **lưu tạm nguyên văn** (raw JSON, giống cơ chế `push_log` đã có sẵn trong `internal/trafficflow/pushlog.go`) thay vì để Go âm thầm bỏ field lạ. Việc này **không cần chờ trả lời các câu hỏi ở mục 5-6** — chỉ cần sửa 1 chỗ, đảm bảo từ giờ không còn sự kiện AI-flow nào bị mất, dữ liệu thô vẫn nằm đó chờ xử lý tiếp.
2. **Mở rộng `AIEvent`** với trường `VehicleDetail` tùy chọn (mục 4.2) + viết adapter parse đúng schema Hawkwise AI-flow (dựa câu trả lời mục 5).
3. **Nối adapter vào pipeline `aievent.Ingest()`** từ bước phân nhánh ở bước 1 — không tạo endpoint mới trừ khi quyết định đổi sang Phương án A (mục 4.1).
4. **Admin UI**: mở rộng bảng category-rule hiện có của "AI Event" để nhận cả mapping theo `ai_flow_code`, hoặc thêm cột nguồn (OMNIA/Hawkwise) nếu 2 bên dùng khóa khác nhau (`typeId/subtypeId` vs `ai_flow_code`).
5. **map.html**: hiển thị thêm block "thông tin phương tiện" trong popup khi `VehicleDetail` có dữ liệu; xử lý ảnh theo kết luận mục 5.4 (proxy tải hộ nếu URL không truy cập được từ trình duyệt người dùng).

---

## Phụ lục: Lịch sử phiên bản

| Phiên bản | Ngày | Nội dung |
|---|---|---|
| 1.0 | 2026-11-10 | Phát hiện ban đầu từ bản tin thật `AbnormalStop`: dữ liệu đang bị mất âm thầm tại endpoint flow-push. Đối chiếu 3 schema, đề xuất 2 phương án định tuyến, liệt kê câu hỏi còn treo. Trạng thái: planning, chưa code. |
