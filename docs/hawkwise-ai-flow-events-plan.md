# Plan: Bản tin AI-flow rời rạc của Hawkwise (vd. "AbnormalStop") — hiện đang bị mất dữ liệu

**Trạng thái: ĐÃ CODE XONG cả 7 bước (2026-11-10)** — xem mục "Triển khai thực tế" ở cuối file. Giữ lại toàn bộ phần phân tích/kế hoạch bên dưới làm tài liệu tham chiếu cho quyết định kiến trúc.

Phát hiện từ bản tin thật người dùng gửi (sự kiện `AbnormalStop`, `event_id 482559`, camera "Ngã tư Hàng Xanh"): Hawkwise gửi **ít nhất 2 loại bản tin khác nhau** tới cùng 1 endpoint vật lý `/api/trafficdata/hawkwise/v1/flow-push`, nhưng package `internal/trafficflow` hiện tại **chỉ parse đúng 1 loại** (`AdvancedTrafficDensity`, xem `docs/hawkwise-flow-integration-plan.md`). Loại còn lại đang bị **mất dữ liệu âm thầm**.

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

Vấn đề cần xử lý thêm khi gộp: `AIEvent` hiện tại **không có chỗ chứa** các field nhận diện phương tiện (biển số, hãng, màu, chủ xe, mũ bảo hiểm, vũ khí, quá tải khách) — đây là dữ liệu OMNIA không có nhưng Hawkwise AI-flow có. Đề xuất: thêm 1 trường **tùy chọn** trên `AIEvent`, vd `VehicleDetail *VehicleInfo` (chỉ có giá trị khi nguồn là Hawkwise, OMNIA để `nil` như cũ) — không phá cấu trúc hiện có, không ảnh hưởng OMNIA, chỉ hiển thị thêm trong popup map khi có dữ liệu.

**Ảnh minh chứng — ĐÃ XÁC NHẬN (2026-11-10):** các URL (`frame_url`, `body_crop_url`, `identified_crop_url`, `video_url`) **chỉ gọi được từ máy chủ (cùng LAN với Hawkwise/NVR)**, người dùng từ Internet **không** gọi trực tiếp được → bắt buộc server phải **tải hộ**. Cách làm: tái dùng nguyên vẹn hạ tầng ảnh đã có cho OMNIA (`internal/aievent/images.go` — lưu theo `<dataDir>/images/<RecordID>/`, `GET /api/aievent/image` phục vụ lại có gate xác minh thiết bị, `CleanupOldImages` dọn theo thời gian). Khác biệt duy nhất: OMNIA gửi sẵn bytes (base64) trong bản tin, còn Hawkwise AI-flow chỉ gửi URL — nên tại thời điểm ingest, adapter cần **tự `GET` các URL này từ phía server** (server nằm trong LAN nên gọi được) rồi lưu bytes tải về qua đúng `ReplaceImages()` sẵn có, thay vì decode base64. Từ đó trở đi, toàn bộ phần hiển thị/serve ảnh trên `map.html` dùng chung 1 cơ chế cho cả 2 vendor, không cần code thêm đường nào khác. Rủi ro cần lường trước: Hawkwise không đảm bảo giữ ảnh gốc lâu dài trên NVR của họ — nên tải **ngay khi nhận được push**, không trì hoãn, và chấp nhận ảnh trống nếu lần tải đó thất bại (log lại để biết tỉ lệ lỗi, không chặn việc lưu sự kiện).

---

## 5. Những điều chưa biết — ĐÃ CHỐT CÁCH XỬ LÝ (2026-11-10): tự thu thập qua GUI, không hỏi vendor trước

Bản tin mẫu chỉ có 1 sự kiện (`AbnormalStop`) — không đủ để suy ra toàn bộ enum (`ai_flow_code` còn gì khác, ý nghĩa `event_type`/`object_action`/`event_state`, `profile_object_groups` có cố định không...). Quyết định: **không chặn lại để hỏi đội Hawkwise/Elcom trước** — thay vào đó xây **cơ chế tự quan sát** (đúng mẫu `ObservedCategory` của `internal/aievent` và `ObservedSensor` của `internal/trafficflow` đã có sẵn và đang chạy tốt), để bạn tự thấy dữ liệu thật trên GUI và quyết định tiếp (tự gán cấu hình, hoặc nhờ agent code thêm khi đã hiểu rõ 1 field nào đó).

**Thiết kế "Observed Hawkwise AI-flow"** (bảng mới, đi kèm Bước 1 ở mục 7, không phải việc làm sau):

- Mỗi khi nhận 1 bản tin có `trigger_code != "AdvancedTrafficDensity"`, ngoài việc lưu raw JSON (mục 7 bước 1), **upsert 1 dòng** vào bảng quan sát, khoá theo `(trigger_code, ai_flow_code)`:
  - `TriggerCode`, `AIFlowCode`, `AIFlowName` — để biết tên hiển thị Hawkwise tự đặt.
  - `FirstSeenAt`, `LastSeenAt`, `Count` — tần suất xuất hiện, giống hệt cách `ObservedCategory` đang theo dõi `(typeId, subtypeId)` của OMNIA.
  - `SampleRawJSON` — giữ nguyên văn **1 bản tin mẫu gần nhất** của tổ hợp đó (không phải mọi bản tin — tránh phình dữ liệu), để admin bấm xem trực tiếp `event_type`/`object_action`/`event_state`/`profile_object_groups`/... thực tế là gì mà không cần hỏi ai.
- **Admin UI**: 1 bảng mới trong tab "AI Event" (cạnh bảng "Observed Categories" hiện có), liệt kê các tổ hợp đã thấy + nút "xem bản tin mẫu" (hiện raw JSON formatted) + trạng thái đã cấu hình category-rule hay chưa (giống cột trạng thái "đã gán icon" bên OMNIA observed categories).
- Khi đã nhìn thấy đủ dữ liệu thật, bạn tự quyết định: tạo category-rule (icon/nhãn hiển thị) ngay trên GUI hiện có — không cần code; hoặc nếu phát hiện 1 field cần xử lý đặc biệt (vd `profile_object_groups` cần hiển thị thành badge riêng, hoặc `event_state` hoá ra có ý nghĩa cần xử lý logic) thì nhờ code thêm đúng phần đó, có dữ liệu mẫu thật trong tay thay vì đoán.

→ Việc này **giải quyết toàn bộ 5 câu hỏi cũ** (ai_flow_code còn gì khác, ý nghĩa event_type/object_action/event_state, profile_object_groups, dedup theo event_id — dedup vẫn áp dụng y hệt OMNIA/density vì `event_id` luôn có mặt, không cần chờ xác nhận gì thêm) mà không cần chặn tiến độ chờ trả lời từ bên thứ ba.

---

## 6. Câu hỏi sản phẩm — ĐÃ CHỐT với bạn (2026-11-10)

1. **Hiển thị**: gộp chung layer **"🤖 AI Event"** với OMNIA (không tách layer riêng cho Hawkwise).
2. **Luồng xử lý**: chỉ hiển thị tự động trên bản đồ, **không** đẩy vào sổ "Sự cố giao thông" — giống hệt cách OMNIA đang chạy, không ai phải xác nhận/thao tác gì.
3. **Tra cứu dữ liệu phương tiện**: chỉ cần hiển thị trong popup khi xem sự kiện — **không** xây chức năng tìm kiếm/lọc theo biển số/hãng xe riêng. → `VehicleDetail` (mục 4.2) có thể lưu đơn giản (vd 1 blob JSON đính kèm `AIEvent`, không cần field riêng từng cột/chỉ mục tìm kiếm).

---

## 7. Đề xuất các bước triển khai (khi đã chốt phương án)

Thứ tự ưu tiên theo mức độ cấp bách (mục 1 chặn mất dữ liệu đang diễn ra):

1. **Chặn mất dữ liệu + bắt đầu thu thập** (Phương án B, mục 4.1 + cơ chế quan sát mục 5): sửa handler `/api/trafficdata/hawkwise/v1/flow-push` để nhận diện `trigger_code != "AdvancedTrafficDensity"` → (a) **lưu tạm nguyên văn** (raw JSON, giống cơ chế `push_log` đã có sẵn trong `internal/trafficflow/pushlog.go`) thay vì để Go âm thầm bỏ field lạ, và (b) **upsert vào bảng "Observed Hawkwise AI-flow"** mới (mục 5) để GUI có dữ liệu thật ngay từ bản tin đầu tiên. Việc này **không cần chờ trả lời câu hỏi kỹ thuật nào** — chỉ cần sửa 1 chỗ, đảm bảo từ giờ không còn sự kiện AI-flow nào bị mất, và bạn có GUI để theo dõi dữ liệu thật ngay.
2. **Admin UI cho bảng quan sát** (mục 5): tab mới cạnh "Observed Categories" hiện có, liệt kê tổ hợp `(trigger_code, ai_flow_code)` đã thấy + xem bản tin mẫu.
3. **Mở rộng `AIEvent`** với trường `VehicleDetail` tùy chọn (mục 4.2) + viết adapter parse đúng schema Hawkwise AI-flow (dựa dữ liệu thật đã thu thập được ở bước 1-2, không cần đoán).
4. **Tải ảnh hộ tại thời điểm ingest** (mục 4.2, đã xác nhận LAN-only): gọi `GET` các URL ảnh ngay khi nhận push, lưu qua `internal/aievent/images.go`'s `ReplaceImages()` sẵn có — dùng chung toàn bộ hạ tầng serve/dọn ảnh đã có cho OMNIA, không cần endpoint hay cơ chế hiển thị mới trên `map.html`.
5. **Nối adapter vào pipeline `aievent.Ingest()`** từ bước phân nhánh ở bước 1 — không tạo endpoint mới trừ khi quyết định đổi sang Phương án A (mục 4.1).
6. **Category-rule**: mở rộng bảng category-rule hiện có của "AI Event" để nhận cả mapping theo `ai_flow_code`, hoặc thêm cột nguồn (OMNIA/Hawkwise) nếu 2 bên dùng khóa khác nhau (`typeId/subtypeId` vs `ai_flow_code`).
7. **map.html**: hiển thị thêm block "thông tin phương tiện" trong popup khi `VehicleDetail` có dữ liệu.

---

## Triển khai thực tế (2026-11-10) — cả 7 bước ở mục 7

- **`internal/aievent/categories.go`**: thêm `Source` vào `CategoryRule`/`ObservedCategory` + hằng số `CategorySourceOMNIA`/`CategorySourceHawkwise`; `ObservedCategory` thêm `SampleRawJSON`. `observedKey`/`ObserveCategory`/`ResolveIcon`/`CreateCategoryRule`/`UpdateCategoryRule` đều nhận thêm `source` để OMNIA và Hawkwise không bao giờ đụng số với nhau dù trùng typeId/ai_flow_code.
- **`internal/aievent/aievent.go`**: `AIEvent` thêm `CategorySource` và `VehicleDetail` (`json.RawMessage`, giữ nguyên văn object `details` của Hawkwise — không parse thành field riêng, đúng quyết định mục 6.3). Các trường khác tái dùng nguyên vẹn: `RecordType`←`trigger_code`, `CategoryDescription`←`ai_flow_name`, `Description`←tóm tắt phương tiện (biển số/hãng/màu), `SourceProcessor`←tên camera.
- **`internal/aievent/hawkwise.go`** (mới): `IngestHawkwiseAIFlow()` — điểm vào duy nhất `internal/trafficflow` gọi sang; `describeVehicle()` dựng dòng tóm tắt từ vài field đã rõ nghĩa (`name`/`brand_code`/`color_code`/`type`); `fetchHawkwiseImage()` tải ảnh từ URL (LAN-only, đã xác nhận với bạn) rồi tái dùng `ReplaceImages()` sẵn có — không có đường code ảnh riêng cho Hawkwise.
- **`internal/trafficflow/hawkwise_ai_flow.go`** (mới): `hawkwiseAIFlowPush` struct (giữ `details` dạng `json.RawMessage`, không ép theo schema density); `buildHawkwiseAIFlowInput()` thuần decode+map field (tách riêng để test được, không đụng tới store); ưu tiên tọa độ Sensor đã admin xác nhận hơn tọa độ Hawkwise tự báo, giống tinh thần mục đếm lưu lượng.
- **`internal/trafficflow/api.go`**: `handlePush` phân nhánh theo `general.trigger_code` ngay sau bước validate chung (không đổi gì ở nhánh `AdvancedTrafficDensity`); nhánh khác gọi `handleHawkwiseAIFlowPush` thay vì `Ingest(rec)` — chặn đứt việc tạo `flow_record` rỗng như trước.
- **`www/admin.html`**: bảng Category Rules + Observed Categories thêm cột "Nguồn"; dialog tạo/sửa quy tắc thêm chọn Nguồn (nhãn ô TypeID tự đổi thành "ai_flow_code" khi chọn Hawkwise); nút "👁 Bản tin mẫu" ở Observed Categories tái dùng đúng dialog `pushLogBodyDlg` đã có cho Push Log; thêm mẫu Test Push "AbnormalStop (AI-flow)" cạnh mẫu "AdvancedTrafficDensity" sẵn có.
- **`www/map.html`: không cần sửa gì** — layer "🤖 AI Event" đã hiển thị `description`/`category_label`/`source_processor` trong toast/preview từ trước; vì bước trên tái dùng đúng các field này nên tóm tắt phương tiện tự động hiện ra, không cần code thêm.
- **Test**: `internal/aievent/hawkwise_test.go`, `internal/trafficflow/hawkwise_ai_flow_test.go`, mở rộng `internal/aievent/categories_test.go` cho chiều Source. `go build`/`go vet`/`go test` sạch cho cả 2 package.
- **Kiểm thử thực tế**: dựng server test, tạo Integration cho đường Hawkwise, gửi đúng bản tin `AbnormalStop` mẫu người dùng cung cấp — xác nhận: không sinh `flow_record` rỗng (kể cả sau khi map Sensor), sinh đúng 1 `AIEvent` với `vehicle_detail` đầy đủ + `description` tóm tắt biển số/hãng/màu, `ObservedCategory` ghi nhận đúng `(hawkwise, 21)` kèm bản tin mẫu; tạo quy tắc gán icon qua API thì sự kiện đang "active" đổi icon/nhãn ngay. Gửi lại 1 bản tin `AdvancedTrafficDensity` bình thường để xác nhận nhánh đếm lưu lượng cũ không bị ảnh hưởng — vẫn ra đúng `flow_record`. Playwright xác nhận UI admin hiển thị đúng cột Nguồn và dialog bản tin mẫu.

## Phụ lục: Lịch sử phiên bản

| Phiên bản | Ngày | Nội dung |
|---|---|---|
| 1.0 | 2026-11-10 | Phát hiện ban đầu từ bản tin thật `AbnormalStop`: dữ liệu đang bị mất âm thầm tại endpoint flow-push. Đối chiếu 3 schema, đề xuất 2 phương án định tuyến, liệt kê câu hỏi còn treo. Trạng thái: planning, chưa code. |
| 1.1 | 2026-11-10 | Chốt 3 câu hỏi sản phẩm (mục 6): gộp layer AI Event, không qua sổ sự cố, không cần tra cứu biển số riêng. Câu hỏi kỹ thuật ở mục 5 (các `trigger_code`/`ai_flow_code` khác, khả năng tải ảnh từ URL Hawkwise...) vẫn còn treo — không chặn việc bắt đầu code Bước 1 (mục 7) vì bước đó chỉ cần lưu nguyên văn, chưa cần parse chi tiết. Trạng thái: planning, chưa code. |
| 1.2 | 2026-11-10 | Xác nhận ảnh minh chứng chỉ gọi được từ máy chủ (LAN) → bắt buộc server tải hộ, tái dùng hạ tầng ảnh OMNIA. Chốt cách xử lý các câu hỏi kỹ thuật còn treo ở mục 5: không hỏi vendor trước, xây cơ chế "Observed Hawkwise AI-flow" tự thu thập (giống ObservedCategory/ObservedSensor) để admin tự xem GUI và quyết định tiếp. Cập nhật thứ tự 7 bước triển khai. Trạng thái: planning, chưa code. |
| 2.0 | 2026-11-10 | Code xong cả 7 bước theo đúng kế hoạch 1.2 — xem mục "Triển khai thực tế" ở trên cho danh sách file/thiết kế thực tế. Đã build/test/kiểm thử trực tiếp bằng đúng bản tin mẫu người dùng gửi, xác nhận hết mất dữ liệu và không ảnh hưởng luồng đếm lưu lượng cũ. Trạng thái: **đã triển khai**. |
