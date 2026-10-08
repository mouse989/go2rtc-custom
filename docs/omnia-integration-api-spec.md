# Đặc tả API — Tích hợp đẩy sự kiện AI từ OMNIA/FPT VDS

| | |
|---|---|
| Phiên bản tài liệu | 1.0 |
| Ngày | 2026-10-08 |
| Trạng thái | Dự thảo — chờ xác nhận từ đội OMNIA/FPT VDS trước khi triển khai |
| Đối tượng đọc | Đội phát triển hệ thống OMNIA/FPT VDS |

> Tài liệu này mô tả API mà hệ thống **OMNIA/FPT VDS** sẽ gọi để đẩy (push) sự kiện/sự cố giao thông phát hiện được sang hệ thống giám sát giao thông của chúng tôi. Đây là tích hợp **một chiều**: OMNIA là bên gửi (client), hệ thống chúng tôi là bên nhận (server). Không có API nào theo chiều ngược lại trong phạm vi tài liệu này.

## Mục lục

1. [Tổng quan](#1-tổng-quan)
2. [Giao thức & kết nối](#2-giao-thức--kết-nối)
3. [Xác thực (Authentication)](#3-xác-thực-authentication)
4. [Endpoint](#4-endpoint)
5. [Định dạng request](#5-định-dạng-request)
6. [Mô tả chi tiết các trường dữ liệu](#6-mô-tả-chi-tiết-các-trường-dữ-liệu)
7. [Ảnh đính kèm](#7-ảnh-đính-kèm)
8. [Cơ chế chống trùng lặp (Idempotency)](#8-cơ-chế-chống-trùng-lặp-idempotency)
9. [Giới hạn (Limits)](#9-giới-hạn-limits)
10. [Định dạng response](#10-định-dạng-response)
11. [Chính sách gửi lại (Retry)](#11-chính-sách-gửi-lại-retry)
12. [Ví dụ đầy đủ](#12-ví-dụ-đầy-đủ)
13. [Môi trường kiểm thử (Sandbox)](#13-môi-trường-kiểm-thử-sandbox)
14. [Quy trình đăng ký tích hợp](#14-quy-trình-đăng-ký-tích-hợp)
15. [Versioning & thay đổi trong tương lai](#15-versioning--thay-đổi-trong-tương-lai)
16. [Checklist trước khi go-live](#16-checklist-trước-khi-go-live)
17. [Liên hệ hỗ trợ](#17-liên-hệ-hỗ-trợ)

---

## 1. Tổng quan

Mỗi khi OMNIA phát hiện một sự kiện/sự cố giao thông (xe hỏng, va chạm, ùn tắc, ngập nước...), hệ thống OMNIA **chủ động gửi HTTP POST** một bản tin JSON mô tả sự kiện đó tới endpoint do chúng tôi cung cấp. Chúng tôi lưu lại, hiển thị dưới dạng biểu tượng (bong bóng) trên bản đồ giám sát giao thông kèm ảnh chụp hiện trường (nếu có), trong một khoảng thời gian nhất định.

Luồng tổng quát:

```
OMNIA phát hiện sự kiện
        │
        ▼
POST JSON (+ ảnh base64) ──► Endpoint của chúng tôi (xác thực bằng API Key)
        │
        ▼
Trả về HTTP response xác nhận thành công/lỗi (đồng bộ, ngay trong request đó)
```

Không có cơ chế kéo (polling) — toàn bộ là đẩy (push) một chiều từ OMNIA.

---

## 2. Giao thức & kết nối

| Mục | Giá trị |
|---|---|
| Giao thức | HTTPS **bắt buộc** — không hỗ trợ HTTP thuần |
| TLS | TLS 1.2 trở lên |
| Phương thức HTTP | `POST` |
| `Content-Type` yêu cầu | `application/json; charset=utf-8` |
| Mã hoá ký tự | UTF-8 |

Một request không phải `POST`, không phải HTTPS, hoặc sai `Content-Type` sẽ bị từ chối trước khi hệ thống đọc nội dung body (xem [mục 10](#10-định-dạng-response)).

---

## 3. Xác thực (Authentication)

### 3.1. Cơ chế

Xác thực bằng **API Key tĩnh**, gửi kèm trong header của mọi request:

```
X-API-Key: <api-key-được-cấp>
```

- Khoá này **không phải** JWT, không có hạn phiên, không gắn với tài khoản người dùng cụ thể nào trong hệ thống chúng tôi — nó định danh cho **hệ thống OMNIA** như một đối tác tích hợp.
- Khoá là một chuỗi ngẫu nhiên dài (≥ 32 ký tự), được chúng tôi **cấp một lần duy nhất** khi khởi tạo tích hợp (xem [mục 14](#14-quy-trình-đăng-ký-tích-hợp)). Chúng tôi **không lưu lại khoá dưới dạng có thể đọc được** — nếu làm mất, chúng tôi sẽ cấp khoá mới (rotate), khoá cũ sẽ ngừng hoạt động ngay lập tức.
- Mỗi khoá chỉ được phép gọi **đúng endpoint đã đăng ký** cho tích hợp đó (xem [mục 4](#4-endpoint)) — gọi sai endpoint bằng khoá này sẽ bị từ chối dù khoá hợp lệ.

### 3.2. Bảo mật khoá (bắt buộc tuân thủ phía OMNIA)

- **Không** nhúng khoá trong mã nguồn commit vào hệ thống quản lý phiên bản dùng chung.
- **Không** gửi khoá qua kênh không mã hoá (email thường, chat không mã hoá...) — trao đổi khoá qua kênh bảo mật đã thống nhất khi đăng ký tích hợp.
- Nếu nghi ngờ khoá bị lộ, **báo ngay** cho chúng tôi để thu hồi/cấp lại — xem [mục 17](#17-liên-hệ-hỗ-trợ).

### 3.3. Lỗi xác thực

| Tình huống | HTTP Status | `error.code` |
|---|---|---|
| Thiếu header `X-API-Key` | 401 | `missing_api_key` |
| Khoá sai/không tồn tại | 401 | `invalid_api_key` |
| Khoá hợp lệ nhưng đã bị tạm khoá/thu hồi | 403 | `integration_disabled` |
| Khoá hợp lệ nhưng gọi sai endpoint được phép | 403 | `endpoint_not_allowed` |

> **Lưu ý vận hành:** nếu gửi sai khoá liên tục nhiều lần trong thời gian ngắn từ cùng một địa chỉ IP, địa chỉ đó có thể bị tạm khoá ở tầng hạ tầng của chúng tôi (cơ chế chống dò khoá). Nếu gặp lỗi 401 kéo dài bất thường dù khoá đúng, liên hệ chúng tôi để kiểm tra.

---

## 4. Endpoint

```
POST https://<domain-hệ-thống-của-chúng-tôi>/api/aievent/omnia/v1/push
```

- `<domain-hệ-thống-của-chúng-tôi>` sẽ được cung cấp cụ thể khi hoàn tất đăng ký tích hợp (mục 14) — có thể khác nhau giữa môi trường sandbox và production.
- Đường dẫn chứa `v1` — xem [mục 15](#15-versioning--thay-đổi-trong-tương-lai) về chính sách versioning.

---

## 5. Định dạng request

### 5.1. Header bắt buộc

| Header | Giá trị | Bắt buộc |
|---|---|---|
| `Content-Type` | `application/json; charset=utf-8` | Có |
| `X-API-Key` | Khoá được cấp | Có |

### 5.2. Body

Body là **một object JSON duy nhất** mô tả một sự kiện, theo đúng cấu trúc đã thống nhất từ mẫu dữ liệu OMNIA cung cấp. **Lưu ý quan trọng về quy ước đặt tên field**: phần ngoài cùng dùng `camelCase` (`type`, `publishedAt`, `recordId`...), nhưng bên trong object `event` dùng `PascalCase` (`Description`, `StartTime`...) — đây **không phải lỗi**, đây là đúng theo định dạng dữ liệu gốc của OMNIA đã cung cấp cho chúng tôi. **Vui lòng giữ nguyên quy ước này**, không chuẩn hoá lại thành một kiểu đặt tên duy nhất, để tránh sai lệch với bộ phân tích dữ liệu phía chúng tôi.

Cấu trúc tổng thể (chi tiết từng trường ở [mục 6](#6-mô-tả-chi-tiết-các-trường-dữ-liệu)):

```json
{
  "type": "event.created",
  "publishedAt": "2026-10-08T07:15:51.521Z",
  "recordId": "ABS_068_...",
  "situationId": "SIT_ABS_068_...",
  "recordType": "VehicleObstruction",
  "eventCode": "EVT:639270657291513802",
  "category": {
    "typeId": 12,
    "subtypeId": 41,
    "description": "12/41 (Incident - broken down vehicle(s))",
    "rule": "broken down vehicle"
  },
  "event": {
    "Description": "...",
    "Category": { "Type": { "Id": 12 }, "Subtype": { "Id": 41 } },
    "StartTime": { "Value": "2026-09-25T03:48:54Z" },
    "EndTime": null,
    "Classification": { "Id": "8" },
    "Status": { "Id": "A" },
    "CreatorUserName": "fptvds",
    "ValidatorUserName": null,
    "AssigneeUserName": null,
    "Source": { "Processor": "FPT VDS", "Delegate": "Unassigned" },
    "Published": false,
    "Location": {
      "Type": "Geometry",
      "Geometry": { "Type": "Point" },
      "X": 106.688,
      "Y": 10.78393
    },
    "Attachments": [
      { "Name": "snapshot.jpg", "Content": "<base64>" }
    ],
    "IncidentAttributes": { "...": "..." },
    "IsReadOnly": false,
    "Reliability": 100
  },
  "attachmentCount": 1
}
```

---

## 6. Mô tả chi tiết các trường dữ liệu

Cột **Bắt buộc** là yêu cầu từ phía hệ thống chúng tôi để chấp nhận bản tin — không phải tất cả trường OMNIA có đều là bắt buộc phải gửi.

### 6.1. Cấp ngoài cùng (envelope)

| Trường | Kiểu | Bắt buộc | Mô tả |
|---|---|---|---|
| `type` | string | **Có** | Loại sự kiện. Hiện hỗ trợ `"event.created"`. Giá trị khác (vd `event.updated`) sẽ được chấp nhận và xử lý như cập nhật nếu `recordId` đã tồn tại — xem [mục 8](#8-cơ-chế-chống-trùng-lặp-idempotency). |
| `publishedAt` | string (RFC 3339, UTC) | Khuyến nghị | Thời điểm OMNIA phát hành bản tin. Chỉ dùng để tham khảo/đối chiếu log, **không** dùng làm thời gian hiển thị sự cố. |
| `recordId` | string | **Có** | Định danh duy nhất của sự kiện phía OMNIA. **Đây là khoá chống trùng** — xem mục 8. Phải ổn định cho cùng một sự kiện xuyên suốt các lần gửi. |
| `situationId` | string | Khuyến nghị | Định danh nhóm tình huống (nếu một tình huống có thể sinh nhiều bản ghi liên quan). |
| `recordType` | string | Khuyến nghị | Loại bản ghi, vd `VehicleObstruction`. Dùng làm gợi ý phân loại khi chưa có rule mapping theo `category`. |
| `eventCode` | string | Không | Mã định danh bổ sung, chỉ lưu tham khảo. |
| `category` | object | **Có** | Xem 6.2. |
| `event` | object | **Có** | Xem 6.3. |
| `attachmentCount` | integer | Không | Số lượng ảnh đính kèm — mang tính thông tin, hệ thống chúng tôi tự đếm theo `event.Attachments`, không bắt buộc phải khớp tuyệt đối. |

### 6.2. `category`

| Trường | Kiểu | Bắt buộc | Mô tả |
|---|---|---|---|
| `category.typeId` | integer | **Có** | Mã nhóm sự kiện (vd `12` = Incident). Dùng làm khoá chính để ánh xạ sang biểu tượng hiển thị bên chúng tôi. |
| `category.subtypeId` | integer | Khuyến nghị | Mã loại chi tiết (vd `41` = broken down vehicle). |
| `category.description` | string | Khuyến nghị | Mô tả dạng text của category — dùng làm nhãn hiển thị tạm thời nếu `typeId`/`subtypeId` đó **chưa được cấu hình** ánh xạ bên chúng tôi. |
| `category.rule` | string | Không | Tên rule nội bộ phía OMNIA đã kích hoạt sự kiện — chỉ lưu tham khảo. |

> **Về việc thêm category mới:** OMNIA có thể gửi bất kỳ tổ hợp `typeId`/`subtypeId` nào mà không cần báo trước — hệ thống chúng tôi tự động ghi nhận các tổ hợp mới xuất hiện và vẫn hiển thị được (bằng biểu tượng mặc định) trong lúc chờ được cấu hình ánh xạ cụ thể. Không có bước "đăng ký category" nào bắt buộc trước khi gửi.

### 6.3. `event`

| Trường | Kiểu | Bắt buộc | Mô tả |
|---|---|---|---|
| `event.Description` | string | Khuyến nghị | Mô tả sự kiện, hiển thị trong chi tiết sự cố. |
| `event.Category.Type.Id` / `event.Category.Subtype.Id` | integer | Không | Trùng thông tin với `category.typeId`/`subtypeId` ở cấp ngoài — gửi kèm để nhất quán với cấu trúc gốc, hệ thống chúng tôi ưu tiên đọc từ `category` ở cấp ngoài. |
| `event.StartTime.Value` | string (RFC 3339, UTC) | **Có** | Thời điểm sự cố **thực tế xảy ra** — đây là mốc thời gian chính được dùng để hiển thị/lọc sự cố, khác với `publishedAt`. |
| `event.EndTime` | object `{ "Value": "..." }` hoặc `null` | Không | `null` nếu sự cố chưa kết thúc. |
| `event.Classification.Id` | string | Không | Mã phân loại nội bộ OMNIA — lưu tham khảo. |
| `event.Status.Id` | string | Không | Trạng thái xử lý phía OMNIA — lưu tham khảo, hiện **không** điều khiển trạng thái hiển thị bên chúng tôi. |
| `event.CreatorUserName` | string | Không | Lưu tham khảo. |
| `event.ValidatorUserName` | string hoặc `null` | Không | Lưu tham khảo. |
| `event.AssigneeUserName` | string hoặc `null` | Không | Lưu tham khảo. |
| `event.Source.Processor` | string | Khuyến nghị | Tên hệ thống/nguồn phát hiện, hiển thị như "nguồn dữ liệu" trong chi tiết sự cố. |
| `event.Source.Delegate` | string | Không | Lưu tham khảo. |
| `event.Published` | boolean | Không | Lưu tham khảo. |
| `event.Location` | object | Khuyến nghị mạnh | Xem 6.4. **Nếu thiếu, sự cố vẫn được lưu nhưng sẽ không hiển thị được trên bản đồ** (chỉ xem được qua tra cứu khác nếu có). |
| `event.Attachments` | array | Không | Xem [mục 7](#7-ảnh-đính-kèm). |
| `event.IncidentAttributes` | object | Không | Các thuộc tính chi tiết (số xe liên quan, thời tiết...) — hiện **lưu nguyên trạng để tham khảo**, chưa có hiển thị riêng trên giao diện bản đồ. |
| `event.IsReadOnly` | boolean | Không | Lưu tham khảo. |
| `event.Reliability` | integer (0–100) | Không | Độ tin cậy — lưu tham khảo, hiện chưa dùng để lọc hiển thị. |

### 6.4. `event.Location`

| Trường | Kiểu | Bắt buộc | Mô tả |
|---|---|---|---|
| `event.Location.Type` | string | Khuyến nghị | Hiện luôn là `"Geometry"`. |
| `event.Location.Geometry.Type` | string | Khuyến nghị | Hiện luôn là `"Point"` — **hệ thống chúng tôi hiện chỉ hỗ trợ điểm (Point)**, không hỗ trợ đường/vùng (LineString/Polygon) ở phiên bản này. |
| `event.Location.X` | number | **Có** (nếu gửi `Location`) | Kinh độ (longitude), hệ toạ độ WGS84. |
| `event.Location.Y` | number | **Có** (nếu gửi `Location`) | Vĩ độ (latitude), hệ toạ độ WGS84. |

---

## 7. Ảnh đính kèm

| Trường | Kiểu | Mô tả |
|---|---|---|
| `event.Attachments[].Name` | string | Tên file, bao gồm phần mở rộng (vd `snapshot.jpg`). |
| `event.Attachments[].Content` | string | Nội dung ảnh mã hoá **Base64 chuẩn (RFC 4648)**, **không** kèm tiền tố `data:image/jpeg;base64,` — chỉ chuỗi base64 thuần. |

**Quy định:**

- Định dạng ảnh chấp nhận: **JPEG, PNG**. Định dạng khác sẽ bị từ chối lưu (nhưng không làm hỏng toàn bộ bản tin — xem phần xử lý lỗi từng phần ở mục 10).
- Kích thước tối đa **mỗi ảnh** (trước khi encode base64): **5 MB**.
- Số lượng ảnh tối đa **mỗi sự kiện**: **5 ảnh**.
- **Thay thế, không cộng dồn**: nếu gửi lại cùng một `recordId` kèm `Attachments` mới, danh sách ảnh **mới sẽ thay thế hoàn toàn** danh sách ảnh cũ của bản ghi đó (không giữ lại ảnh cũ không có trong lần gửi mới). Nếu muốn giữ ảnh cũ, phải gửi lại kèm trong `Attachments` của lần cập nhật đó.
- **Ảnh chỉ được lưu trữ trong thời gian giới hạn** (mặc định 4 giờ kể từ khi nhận, có thể thay đổi theo cấu hình vận hành phía chúng tôi) — sau thời hạn này, ảnh bị xoá khỏi hệ thống dù dữ liệu sự kiện (text) vẫn được giữ lại. **Vui lòng không coi hệ thống chúng tôi là nơi lưu trữ ảnh lâu dài.**

---

## 8. Cơ chế chống trùng lặp (Idempotency)

- `recordId` là **khoá duy nhất** để nhận diện một sự kiện xuyên suốt các lần gửi.
- Gửi một request với `recordId` **chưa từng thấy** → tạo bản ghi mới.
- Gửi một request với `recordId` **đã tồn tại** → **cập nhật đè** lên bản ghi đó (toàn bộ các trường trong request mới sẽ thay thế giá trị cũ của các trường tương ứng; trường không có trong request mới giữ nguyên giá trị cũ).
- **An toàn khi gửi lại (retry)**: nếu gửi trùng y hệt một request (vd do timeout nhưng thực ra đã xử lý thành công), việc gửi lại **không** tạo ra bản ghi trùng lặp thứ hai.
- Do đó, **khuyến nghị OMNIA luôn dùng cùng một `recordId` cho mọi lần cập nhật của cùng một sự kiện thực tế** (ví dụ khi `Status`/`EndTime`/độ tin cậy thay đổi theo thời gian), thay vì sinh `recordId` mới cho mỗi lần gửi.

---

## 9. Giới hạn (Limits)

| Giới hạn | Giá trị |
|---|---|
| Kích thước tối đa toàn bộ request body | **25 MB** |
| Số lượng request tối đa | **60 request/phút** cho mỗi API Key (đủ rộng so với tần suất dự kiến 3–5 bản tin/2–5 phút; nếu nhu cầu thực tế cao hơn, báo trước để điều chỉnh) |
| Thời gian xử lý mỗi request | Hệ thống phản hồi trong vòng **5 giây** trong điều kiện bình thường — khuyến nghị OMNIA đặt timeout phía client ở mức **10 giây** |

Vượt giới hạn kích thước → `413 Payload Too Large`. Vượt giới hạn tần suất → `429 Too Many Requests` kèm header `Retry-After` (giây).

---

## 10. Định dạng response

Mọi response (thành công lẫn lỗi) đều có `Content-Type: application/json; charset=utf-8`.

### 10.1. Thành công

**`200 OK`**

```json
{
  "status": "ok",
  "action": "created",
  "recordId": "ABS_068_VoThiSau_LeQuyDon_10_9_162_34_...",
  "id": "a1b2c3d4e5f6"
}
```

| Trường | Mô tả |
|---|---|
| `status` | Luôn là `"ok"` khi HTTP status là 2xx. |
| `action` | `"created"` (bản ghi mới) hoặc `"updated"` (đã có `recordId` này, vừa cập nhật). |
| `recordId` | Trả lại đúng `recordId` đã gửi, để đối chiếu. |
| `id` | Định danh nội bộ phía chúng tôi — không bắt buộc OMNIA phải lưu, chỉ hỗ trợ khi cần tra cứu/hỗ trợ sự cố. |

> **Lưu ý:** nếu category (`typeId`/`subtypeId`) gửi lên **chưa được cấu hình ánh xạ biểu tượng** phía chúng tôi, request vẫn trả **200 OK** (sự kiện vẫn được lưu, chỉ hiển thị bằng biểu tượng mặc định) — đây không phải lỗi của bên gửi. Tương tự, nếu một ảnh trong `Attachments` lỗi định dạng/giải mã, response vẫn `200 OK`, sự kiện vẫn được lưu **không kèm ảnh lỗi đó** (ảnh hợp lệ khác trong cùng request vẫn được lưu bình thường).

### 10.2. Lỗi

Mọi lỗi trả về theo cấu trúc thống nhất:

```json
{
  "status": "error",
  "error": {
    "code": "missing_required_field",
    "message": "event.StartTime.Value is required",
    "field": "event.StartTime.Value"
  }
}
```

`field` chỉ xuất hiện khi lỗi gắn với một trường cụ thể.

| HTTP Status | `error.code` | Khi nào xảy ra | Nên tự động gửi lại? |
|---|---|---|---|
| 400 | `invalid_json` | Body không phải JSON hợp lệ | Không — sửa dữ liệu trước |
| 400 | `missing_required_field` | Thiếu trường bắt buộc (xem mục 6) | Không — sửa dữ liệu trước |
| 400 | `invalid_field_value` | Trường có giá trị sai định dạng (vd thời gian không đúng RFC 3339) | Không — sửa dữ liệu trước |
| 401 | `missing_api_key` / `invalid_api_key` | Thiếu/sai khoá xác thực | Không — kiểm tra lại khoá |
| 403 | `integration_disabled` / `endpoint_not_allowed` | Khoá hợp lệ nhưng bị khoá/sai phạm vi | Không — liên hệ chúng tôi |
| 413 | `payload_too_large` | Vượt giới hạn mục 9 | Không — giảm kích thước (vd nén ảnh) |
| 415 | `unsupported_media_type` | Sai `Content-Type` | Không — sửa header |
| 429 | `rate_limited` | Vượt giới hạn tần suất | **Có**, sau thời gian ở header `Retry-After` |
| 500 | `internal_error` | Lỗi hệ thống phía chúng tôi | **Có**, theo chính sách retry ở mục 11 |

---

## 11. Chính sách gửi lại (Retry)

Khuyến nghị cho hệ thống OMNIA:

| Nhóm lỗi | Hành động khuyến nghị |
|---|---|
| 2xx | Thành công — không cần gửi lại. |
| 400, 401, 403, 413, 415 | **Không tự động gửi lại** — lỗi do dữ liệu/cấu hình, gửi lại y hệt sẽ luôn lỗi. Ghi log để rà soát thủ công. |
| 429 | Gửi lại sau thời gian trong header `Retry-After`. |
| 5xx hoặc timeout/mất kết nối | Gửi lại tối đa **3 lần**, giãn cách tăng dần: **5s → 15s → 60s**. Nếu vẫn thất bại sau 3 lần, ghi log và cảnh báo vận hành — tránh gửi lại vô hạn. |

Vì `recordId` đảm bảo an toàn khi gửi trùng (mục 8), việc retry không có rủi ro tạo dữ liệu trùng lặp.

---

## 12. Ví dụ đầy đủ

### 12.1. Request mẫu

```
POST /api/aievent/omnia/v1/push HTTP/1.1
Host: <domain-he-thong-cua-chung-toi>
Content-Type: application/json; charset=utf-8
X-API-Key: 7f3a1c9e8b2d4f6a0c1e5b7d9f2a4c6e8b0d1f3a5c7e9b1d3f5a7c9e1b3d5f7a

{
  "type": "event.created",
  "publishedAt": "2026-10-08T07:15:51.521Z",
  "recordId": "ABS_068_VoThiSau_LeQuyDon_10_9_162_34_af40d99f-9bb4-4f8f-8996-c19b965fd840_b9fa5d0b-e573-4163-ba88-dc08ca507d84",
  "situationId": "SIT_ABS_068_VoThiSau_LeQuyDon_10_9_162_34_af40d99f-9bb4-4f8f-8996-c19b965fd840_b9fa5d0b-e573-4163-ba88-dc08ca507d84",
  "recordType": "VehicleObstruction",
  "eventCode": "EVT:639270657291513802",
  "category": {
    "typeId": 12,
    "subtypeId": 41,
    "description": "12/41 (Incident - broken down vehicle(s))",
    "rule": "broken down vehicle"
  },
  "event": {
    "Description": "Incident - broken down vehicle(s)",
    "Category": { "Type": { "Id": 12 }, "Subtype": { "Id": 41 } },
    "StartTime": { "Value": "2026-09-25T03:48:54Z" },
    "EndTime": null,
    "Classification": { "Id": "8" },
    "Status": { "Id": "A" },
    "CreatorUserName": "fptvds",
    "ValidatorUserName": null,
    "AssigneeUserName": null,
    "Source": { "Processor": "FPT VDS", "Delegate": "Unassigned" },
    "Published": false,
    "Location": {
      "Type": "Geometry",
      "Geometry": { "Type": "Point" },
      "X": 106.688,
      "Y": 10.78393
    },
    "Attachments": [
      { "Name": "snapshot.jpg", "Content": "/9j/4AAQSkZJRgABAgAAAQABAAD..." }
    ],
    "IncidentAttributes": {
      "LightVehicles": 0, "HeavyVehicles": 0, "TrafficFlow": 0,
      "Weather": 0, "Fatalities": 0, "PersonInjured": 0,
      "DamageToMotorway": 0, "SpillageOnMotorway": 0,
      "LanesBitmask": null, "Roundabout": 0, "OffRamp": 0,
      "OnRamp": 0, "ServiceRoad": 0, "Shoulder": 0,
      "Bridge": 0, "RoadTypeId": null
    },
    "IsReadOnly": false,
    "Reliability": 100
  },
  "attachmentCount": 1
}
```

### 12.2. Response thành công

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8

{
  "status": "ok",
  "action": "created",
  "recordId": "ABS_068_VoThiSau_LeQuyDon_10_9_162_34_af40d99f-9bb4-4f8f-8996-c19b965fd840_b9fa5d0b-e573-4163-ba88-dc08ca507d84",
  "id": "a1b2c3d4e5f6"
}
```

### 12.3. Response lỗi — thiếu trường bắt buộc

```
HTTP/1.1 400 Bad Request
Content-Type: application/json; charset=utf-8

{
  "status": "error",
  "error": {
    "code": "missing_required_field",
    "message": "event.StartTime.Value is required",
    "field": "event.StartTime.Value"
  }
}
```

### 12.4. Response lỗi — sai khoá

```
HTTP/1.1 401 Unauthorized
Content-Type: application/json; charset=utf-8

{
  "status": "error",
  "error": {
    "code": "invalid_api_key",
    "message": "The provided API key is invalid or has been revoked"
  }
}
```

---

## 13. Môi trường kiểm thử (Sandbox)

Trước khi gửi dữ liệu thật, đề xuất OMNIA kiểm thử theo 2 bước:

1. **Chế độ `dry_run`** — gọi cùng endpoint, cùng xác thực, nhưng thêm query param `?dry_run=1`. Hệ thống sẽ validate toàn bộ request **và trả về response y hệt như thật** nhưng **không lưu dữ liệu, không ghi ảnh xuống đĩa**. Dùng để OMNIA tự kiểm tra định dạng dữ liệu trước khi tích hợp chính thức.
2. **API Key riêng cho môi trường sandbox** (không phải khoá dùng cho production) — sẽ được cấp kèm theo domain sandbox riêng khi đăng ký tích hợp, dữ liệu gửi vào sandbox không xuất hiện trên hệ thống production.

---

## 14. Quy trình đăng ký tích hợp

1. Đội OMNIA/FPT VDS xác nhận đã đọc và đồng ý với đặc tả này (qua email hoặc kênh làm việc chung).
2. Chúng tôi khởi tạo 1 **Integration record** cho OMNIA trong hệ thống quản lý tích hợp, gồm: tên tích hợp, endpoint được phép gọi, sinh API Key.
3. API Key được gửi cho đội OMNIA qua kênh bảo mật đã thống nhất (**không** qua email thường).
4. OMNIA kiểm thử bằng `dry_run` + khoá sandbox (mục 13) trước.
5. Sau khi xác nhận hoạt động đúng, chuyển sang khoá production, bắt đầu gửi dữ liệu thật.
6. Chúng tôi theo dõi log request trong **48 giờ đầu** sau go-live để phát hiện sớm bất thường (tỉ lệ lỗi cao, category lạ chưa cấu hình icon...).

Nếu sau này cần **thu hồi/tạo lại khoá** (nghi lộ, đổi hợp đồng...), chúng tôi sẽ chủ động liên hệ trước, khoá cũ sẽ có thời gian ân hạn ngắn trước khi vô hiệu hẳn (trừ trường hợp khẩn cấp về bảo mật).

---

## 15. Versioning & thay đổi trong tương lai

- Endpoint có mang số phiên bản trong đường dẫn (`v1`) — nếu có thay đổi **không tương thích ngược** (breaking change) trong tương lai, chúng tôi sẽ phát hành `v2` **song song**, giữ `v1` hoạt động trong thời gian chuyển tiếp đã thông báo trước, không tắt đột ngột.
- Các thay đổi **tương thích ngược** (thêm trường mới tuỳ chọn, thêm `error.code` mới...) có thể được áp dụng trên `v1` mà không cần nâng phiên bản, và sẽ được cập nhật vào tài liệu này.
- Trường không nằm trong đặc tả này nhưng được OMNIA gửi kèm sẽ **bị bỏ qua** (không gây lỗi) — vui lòng không phụ thuộc vào hành vi này cho dữ liệu quan trọng, nhưng nó cho phép OMNIA gửi dư field mà không sợ vỡ tích hợp.

---

## 16. Checklist trước khi go-live

- [ ] Đã nhận API Key production (khác với khoá sandbox).
- [ ] Đã test thành công với `?dry_run=1` — nhận `200 OK` với dữ liệu mẫu thật.
- [ ] Đã test ít nhất 1 trường hợp lỗi (vd thiếu `recordId`) và xác nhận nhận đúng `400` kèm `error.code` tương ứng.
- [ ] Đã xác nhận ảnh gửi lên hiển thị đúng ở phía chúng tôi (không bị méo/lỗi giải mã).
- [ ] Đã cấu hình retry theo đúng khuyến nghị ở [mục 11](#11-chính-sách-gửi-lại-retry) (không retry vô hạn cho lỗi 4xx).
- [ ] Đã thống nhất kênh liên hệ khi có sự cố tích hợp (mục 17).

---

## 17. Liên hệ hỗ trợ

*(Điền thông tin đầu mối kỹ thuật cụ thể trước khi gửi tài liệu này cho đội OMNIA/FPT VDS — email/kênh chat kỹ thuật, giờ hỗ trợ.)*

---

## Phụ lục A — Tổng hợp mã lỗi (`error.code`)

| Code | HTTP | Mô tả |
|---|---|---|
| `invalid_json` | 400 | Body không phải JSON hợp lệ |
| `missing_required_field` | 400 | Thiếu trường bắt buộc |
| `invalid_field_value` | 400 | Giá trị trường sai định dạng |
| `missing_api_key` | 401 | Thiếu header `X-API-Key` |
| `invalid_api_key` | 401 | Khoá sai/không tồn tại |
| `integration_disabled` | 403 | Khoá bị tạm khoá/thu hồi |
| `endpoint_not_allowed` | 403 | Khoá không có quyền gọi endpoint này |
| `unsupported_media_type` | 415 | Sai `Content-Type` |
| `payload_too_large` | 413 | Vượt giới hạn kích thước |
| `rate_limited` | 429 | Vượt giới hạn tần suất |
| `internal_error` | 500 | Lỗi hệ thống phía chúng tôi |

## Phụ lục B — Lịch sử thay đổi tài liệu

| Phiên bản | Ngày | Thay đổi |
|---|---|---|
| 1.0 | 2026-10-08 | Phát hành bản đặc tả đầu tiên |
