# Đặc tả: "UTMC Platform"

## 1. Mục đích & bối cảnh

Lãnh đạo/người dùng hiện phải nhớ nhiều đường dẫn khác nhau để vào các hệ thống riêng biệt (Điều hành Giao thông, Quản lý hiệu suất công việc, Quản lý tuần tra...). Cần một **trang điều hướng (landing page) duy nhất** làm nơi tập trung, từ đó bấm vào icon/thẻ tương ứng để đi đến hệ thống cần dùng.

**Lưu ý quan trọng — thuật ngữ:** Khái niệm/tên gọi chính thức là **"UTMC Platform"**. Tuyệt đối **không dùng từ "Portal" / "cổng"** ở bất kỳ đâu trong giao diện, mã nguồn, tên biến hiển thị, tài liệu, hay trong các trao đổi liên quan đến trang này.

**Mô hình xác thực:** Đây **không phải SSO/portal dùng chung một lượt đăng nhập**. Trang này **không yêu cầu đăng nhập** để xem — ai cũng xem được danh sách hệ thống. Khi bấm vào một thẻ hệ thống, trình duyệt điều hướng (chuyển trang, không phải mở trong iframe) sang URL riêng của hệ thống đó; hệ thống đó tự xử lý đăng nhập của nó (mỗi hệ thống có tài khoản và trang đăng nhập hoàn toàn độc lập, không chia sẻ session với trang này).

**Yêu cầu hạ tầng/triển khai (RẤT QUAN TRỌNG):**
Trang này **không được host trên hệ thống go2rtc** (hệ thống Điều hành Giao thông), vì go2rtc đôi lúc hoạt động chậm/không ổn định — nếu trang điều hướng tổng phụ thuộc vào go2rtc, có lúc người dùng sẽ không vào được trang điều hướng luôn. Trang này cần được **triển khai trên hạ tầng của hệ thống KPI** (hệ thống Quản lý hiệu suất công việc), vì đây là hệ thống nhẹ và ổn định hơn. Đây là lý do đặc tả này được viết thành file độc lập để bàn giao cho agent đang phát triển hệ thống KPI, không phải code trong repo go2rtc.

## 2. Cấu trúc trang (layout)

Trang một màn hình (single page), không cuộn nếu màn hình đủ cao, bố cục từ trên xuống:

1. **Topbar** (thanh trên cùng)
2. **Khoảng trống linh hoạt** (căn giữa nội dung theo chiều dọc)
3. **Lưới các thẻ hệ thống** (system grid) — phần nội dung chính, căn giữa trang
4. **Khoảng trống linh hoạt**
5. **Footer** (chân trang, dòng bản quyền)

Không có khối "hero" (không có tiêu đề lớn kiểu "Cổng truy cập hệ thống" hay đoạn mô tả phụ đề) — phần này đã bị loại bỏ theo yêu cầu, trang đi thẳng từ topbar vào lưới thẻ hệ thống.

### 2.1. Topbar

- Bên trái: logo vuông góc tròn (border-radius ~11px, 40×40px) có chữ "U" màu `#04121f` trên nền gradient `linear-gradient(135deg, #0077ff, #00f0ff)`, cộng với chữ **"UTMC PLATFORM"** ngay bên phải logo — chữ này dùng kiểu gradient text (nền gradient trắng→cyan, `background-clip: text`), font `Space Grotesk`, 700, 20px, letter-spacing .3px.
- Bên phải: **chỉ 1 nút** dạng pill (bo tròn 30px), viền mỏng, "Giới thiệu" (icon vòng tròn chứa "i") — màu chữ xám `#94a3b8`, viền `rgba(255,255,255,.18)`. Hover: viền sáng cyan, chữ sáng hơn.
- **KHÔNG có nút/liên kết nào dẫn đến trang quản trị https-proxy trên trang này.** Đây là yêu cầu bảo mật rõ ràng: trang điều hướng là trang công khai (ai cũng xem được), nên **không được để lộ sự tồn tại hay đường dẫn của trang quản trị** ở bất kỳ đâu trên trang này — không nút, không link ẩn, không ghi chú. Người quản trị truy cập https-proxy bằng URL riêng mà họ đã biết từ trước, ngoài phạm vi trang này hoàn toàn.
- Có viền phân cách mảnh (`border-bottom: 1px solid rgba(255,255,255,.08)`) với phần nội dung bên dưới.
- Padding: `20px 48px`, có `flex-wrap` để co giãn trên màn hình hẹp.

### 2.2. Lưới thẻ hệ thống (system grid)

- Container căn giữa, `max-width: 920px`, grid `repeat(auto-fit, minmax(260px, 1fr))`, khoảng cách giữa thẻ `26px`.
- **Mỗi thẻ hệ thống KHÔNG có**: mô tả chi tiết (description text) và **KHÔNG hiển thị đường dẫn/domain** (không có dòng caption kiểu `dieuhanh.utmc.io.vn →`). Thẻ chỉ gồm: **hình minh họa (illustration)** ở trên + **tên hệ thống** ở dưới, căn giữa.
- Thẻ là một thẻ `<a>` bấm được toàn vùng, bo góc 20px, nền `rgba(10,19,35,.82)`, viền `1px solid rgba(255,255,255,.1)`.
- Hover: viền sáng cyan `rgba(0,240,255,.45)`, nhấc lên nhẹ (`translateY(-5px)`), đổ bóng.
- Hiện tại có **3 thẻ** (danh sách hệ thống đang hoạt động — không hiển thị thẻ "placeholder" cho hệ thống chưa có; khi có hệ thống mới sẽ thêm thẻ mới tương tự, không để sẵn ô trống):

Cả 3 thẻ dùng **chung một bố cục minh họa 2 tầng** để đồng bộ phong cách: **tầng trên** là một **cụm 3 icon nhỏ chủ đề** (mỗi icon trong một khung vuông viền màu riêng), **tầng dưới** là một **"màn hình" lớn hơn** minh họa nội dung chính của hệ thống đó. Không dùng icon đơn lẻ hay ảnh raster/photo — tất cả vẽ bằng SVG thuần.

  1. **Điều hành Giao thông** — cụm icon trên: (a) biểu tượng khúc cua/đường, (b) vòng tròn quét giống ống kính camera, (c) đường xu hướng nhỏ. Màn hình dưới: sơ đồ giao lộ (2 trục đường cắt nhau), đèn tín hiệu giao thông (3 chấm đỏ/vàng/xanh), và vài phương tiện. Tông màu chủ đạo: cyan (`#00f0ff`) + xanh dương (`#0077ff`).
  2. **Quản lý hiệu suất công việc** — cụm icon trên: (a) mục tiêu/target (vòng tròn đồng tâm), (b) danh sách công việc (checklist 3 dòng), (c) mũi tên xu hướng tăng. Màn hình dưới: biểu đồ cột (bar chart) kèm đường xu hướng đi lên (trend line) và chấm điểm dữ liệu — tái sử dụng đúng kiểu biểu đồ hiệu suất. Tông màu chủ đạo: xanh lá (`#4ade80` / `#22c55e` / `#bbf7d0`).
  3. **Quản lý tuần tra** — chủ đề xoay quanh 4 yếu tố: vị trí/địa điểm, mã QR, thiết bị hệ thống, con người. Cụm icon trên: (a) ghim định vị/map pin (vị trí), (b) lưới QR nhỏ 3×3 ô vuông (mã QR checkin), (c) thiết bị cầm tay (khung thiết bị có ăng-ten + màn hình nhỏ — máy quét/bộ đàm tuần tra, đại diện cho thiết bị hệ thống). Màn hình dưới: tuyến tuần tra dạng đường chấm (dashed route) nối các điểm chốt kiểm tra (checkpoint), kết thúc bằng **hình người** (đầu + thân) kèm huy hiệu dấu check nhỏ — đại diện cho nhân viên tuần tra đã xác nhận có mặt tại điểm chốt (con người). Tông màu chủ đạo: vàng hổ phách (`#fbbf24`). Không dùng bất kỳ logo thương hiệu/hãng thứ ba nào — toàn bộ icon tự vẽ, trung lập.

  (Tên cũ "Giám sát KPI" → đổi thành **"Quản lý hiệu suất công việc"**; tên cũ "QR Checkin" → đổi thành **"Quản lý tuần tra"**. Tên "Điều hành Giao thông" giữ nguyên.)

- Mỗi minh họa nên là **SVG vẽ tay dạng chi tiết** (không dùng icon đơn giản 1 nét, không dùng emoji) — xem khối SVG mẫu ở Phụ lục A để agent KPI có thể dùng trực tiếp hoặc vẽ lại theo đúng phong cách này (đường nét kỹ thuật, nhiều chi tiết nhỏ, phối màu theo từng hệ thống).
- Khi có thêm hệ thống mới trong tương lai: thêm thẻ mới cùng kiểu, cùng kích cỡ minh họa (SVG viewBox `0 0 100 80`), chọn một tông màu nhận diện riêng chưa dùng.

### 2.3. Footer

- Một dòng căn giữa, chữ nhỏ (12px) màu xám `#64748b`, có viền phân cách mỏng phía trên:
  `© 2026 UTMC Platform — Trung tâm Quản lý Điều hành Giao thông Đô thị`

## 3. Design tokens (màu sắc, font, hiệu ứng)

Bộ nhận diện này **đã được áp dụng đồng bộ** ở trang đăng nhập mới (`www/login.html`) và trang đăng nhập quản trị https-proxy — trang điều hướng này cần dùng **đúng** bộ tokens này để nhất quán thương hiệu:

| Token | Giá trị |
|---|---|
| Nền chính | `#050b16` (navy rất tối) |
| Hiệu ứng nền | 2 `radial-gradient` mờ: 1 xanh dương `rgba(0,119,255,.16)` góc trên-trái, 1 cyan `rgba(0,240,255,.12)` góc dưới-phải, `transparent` 45–50% |
| Chữ chính | `#e6edf3` |
| Chữ phụ/xám | `#94a3b8` (nhạt hơn: `#64748b`) |
| Gradient nhận diện (logo, nút chính, chữ nhấn) | `linear-gradient(135deg, #0077ff, #00f0ff)` |
| Font tiêu đề/thương hiệu | `Space Grotesk` (600/700) |
| Font nội dung | `Plus Jakarta Sans` (400/500/600/700) — cả hai load từ Google Fonts |
| Bo góc thẻ | 18–20px |
| Bo góc nút pill | 30px (full pill) |
| Nền thẻ | `rgba(10,19,35,.82)` + viền `rgba(255,255,255,.1)` |
| Hover thẻ | viền `rgba(0,240,255,.45)`, nhấc `translateY(-5px)`, box-shadow 2 lớp |

Màu tông riêng theo hệ thống (dùng cho icon-box/illustration của từng thẻ):
- Điều hành Giao thông: cyan `#00f0ff` / xanh dương `#0077ff`
- Quản lý hiệu suất công việc: xanh lá `#4ade80` / `#22c55e`
- Quản lý tuần tra: vàng hổ phách `#fbbf24`

## 4. Hành vi điều hướng (click behavior)

- Click vào **bất kỳ thẻ hệ thống** → điều hướng toàn trang (`window.location.href` hoặc `<a href>` thường) sang URL gốc của hệ thống đó. **Không mở tab mới, không iframe.** Hệ thống đích tự quyết định có cần đăng nhập hay không và hiển thị trang đăng nhập của riêng nó.
- Click nút **"Giới thiệu"** — hiện tại có thể chỉ là placeholder (ví dụ mở một mô tả ngắn/modal giới thiệu hệ thống UTMC Platform là gì) hoặc để trống/ẩn nếu chưa cần — không bắt buộc có hành vi phức tạp.
- **Không có nút/đường dẫn admin nào trên trang này.** Trang quản trị https-proxy **không được liên kết, không được gợi ý tồn tại** từ trang điều hướng công khai này dưới bất kỳ hình thức nào (không nút ẩn, không link trong footer, không ghi trong mã nguồn dưới dạng comment lộ URL). Đây là yêu cầu bảo mật tường minh từ chủ hệ thống.

## 5. Thông tin cần xác nhận trước khi code (TBD)

Các giá trị dưới đây là **giả định/placeholder**, cần đội vận hành xác nhận domain thật trước khi gắn cứng vào code:

| Hệ thống | URL đích (cần xác nhận) |
|---|---|
| Điều hành Giao thông | *(domain thật của hệ thống go2rtc hiện tại)* |
| Quản lý hiệu suất công việc | *(domain thật của hệ thống KPI — cũng là nơi host trang này)* |
| Quản lý tuần tra | *(domain thật của hệ thống tuần tra/QR checkin)* |

Agent phía KPI nên để các URL này ở dạng biến/config dễ sửa (không hard-code rải rác), vì giá trị thật sẽ được cung cấp sau khi đặc tả này được duyệt.

## 6. Responsive

- Grid dùng `repeat(auto-fit, minmax(260px, 1fr))` nên tự co từ 3 cột → 1 cột khi màn hình hẹp (điện thoại/tablet).
- Topbar dùng `flex-wrap: wrap` để 2 nút bên phải tự xuống dòng khi không đủ chỗ.
- Không cần media query riêng cho layout cơ bản này; nếu cần tối ưu thêm cho điện thoại rất nhỏ, có thể giảm `padding` ngang của topbar và grid.

## 7. Mockup tham khảo

Mockup trực quan (Claude Artifact) đã được dựng để tham khảo bố cục/màu sắc/kiểu chữ chính xác:
`https://claude.ai/artifact/QPyCs13xh2p18AyjXXGyUj`

## Phụ lục A — SVG minh họa mẫu cho 3 thẻ hệ thống

Cả 3 minh họa dùng chung khuôn `viewBox="0 0 100 80"`, bố cục 2 tầng (cụm 3 icon nhỏ ở trên, "màn hình" nội dung lớn ở dưới), để đồng bộ phong cách khi đặt cạnh nhau trong lưới.

```html
<!-- 1. Điều hành Giao thông -->
<svg width="100" height="80" viewBox="0 0 100 80" aria-hidden="true">
  <!-- top icon cluster: khúc cua / camera quét / xu hướng -->
  <rect x="4" y="4" width="28" height="20" rx="2.5" fill="#0b1422" stroke="#00f0ff" stroke-width="1.6"></rect>
  <rect x="36" y="4" width="28" height="20" rx="2.5" fill="#0b1422" stroke="#00f0ff" stroke-width="1.6"></rect>
  <rect x="68" y="4" width="28" height="20" rx="2.5" fill="#0b1422" stroke="#0077ff" stroke-width="1.6"></rect>
  <path d="M8 20 L14 8 M24 20 L20 8" stroke="#00f0ff" stroke-width="1.3" opacity=".8" fill="none"></path>
  <circle cx="17" cy="12" r="1.6" fill="#00f0ff"></circle>
  <circle cx="50" cy="14" r="4" fill="none" stroke="#00f0ff" stroke-width="1.4"></circle>
  <circle cx="50" cy="14" r="1.4" fill="#00f0ff"></circle>
  <polyline points="72,20 78,14 84,17 90,9" fill="none" stroke="#7dd3fc" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"></polyline>
  <!-- main screen: sơ đồ giao lộ -->
  <rect x="4" y="30" width="92" height="46" rx="3" fill="#0b1422" stroke="#1e3a52" stroke-width="2"></rect>
  <path d="M4 53 H96 M50 30 V76" stroke="#27405c" stroke-width="6" fill="none"></path>
  <path d="M4 53 H96 M50 30 V76" stroke="#0f1b2d" stroke-width="1.4" stroke-dasharray="4 5" opacity=".7" fill="none"></path>
  <g transform="translate(58,40)">
    <rect x="-4" y="-7" width="8" height="16" rx="2" fill="#162238"></rect>
    <circle cx="0" cy="-3" r="1.6" fill="#ef4444"></circle>
    <circle cx="0" cy="1" r="1.6" fill="#fbbf24"></circle>
    <circle cx="0" cy="5" r="1.6" fill="#4ade80"></circle>
  </g>
  <rect x="14" y="49" width="12" height="6" rx="1.8" fill="#0077ff"></rect>
  <rect x="70" y="60" width="12" height="6" rx="1.8" fill="#f59e0b"></rect>
  <rect x="40" y="62" width="7" height="11" rx="1.8" fill="#00f0ff" opacity=".85"></rect>
</svg>

<!-- 2. Quản lý hiệu suất công việc -->
<svg width="100" height="80" viewBox="0 0 100 80" aria-hidden="true">
  <!-- top icon cluster: target / checklist / xu hướng tăng -->
  <rect x="4" y="4" width="28" height="20" rx="2.5" fill="#0b1422" stroke="#4ade80" stroke-width="1.6"></rect>
  <rect x="36" y="4" width="28" height="20" rx="2.5" fill="#0b1422" stroke="#4ade80" stroke-width="1.6"></rect>
  <rect x="68" y="4" width="28" height="20" rx="2.5" fill="#0b1422" stroke="#22c55e" stroke-width="1.6"></rect>
  <circle cx="18" cy="14" r="6.2" fill="none" stroke="#4ade80" stroke-width="1.3"></circle>
  <circle cx="18" cy="14" r="3.4" fill="none" stroke="#4ade80" stroke-width="1.3"></circle>
  <circle cx="18" cy="14" r="1.2" fill="#4ade80"></circle>
  <circle cx="43" cy="10" r="1.3" fill="#4ade80"></circle>
  <line x1="47" y1="10" x2="57" y2="10" stroke="#4ade80" stroke-width="1.4"></line>
  <circle cx="43" cy="14.5" r="1.3" fill="#4ade80"></circle>
  <line x1="47" y1="14.5" x2="57" y2="14.5" stroke="#4ade80" stroke-width="1.4"></line>
  <circle cx="43" cy="19" r="1.3" fill="#4ade80"></circle>
  <line x1="47" y1="19" x2="54" y2="19" stroke="#4ade80" stroke-width="1.4" opacity=".7"></line>
  <path d="M73 20 L90 8" stroke="#bbf7d0" stroke-width="1.8" fill="none" stroke-linecap="round"></path>
  <path d="M82 8 H90 V16" fill="none" stroke="#bbf7d0" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"></path>
  <!-- main screen: biểu đồ hiệu suất -->
  <rect x="4" y="30" width="92" height="46" rx="3" fill="#0b1422" stroke="#174a30" stroke-width="2"></rect>
  <rect x="10" y="48" width="9" height="20" fill="#4ade80" opacity=".85"></rect>
  <rect x="23" y="40" width="9" height="28" fill="#4ade80" opacity=".9"></rect>
  <rect x="36" y="34" width="9" height="34" fill="#22c55e"></rect>
  <rect x="49" y="44" width="9" height="24" fill="#4ade80" opacity=".9"></rect>
  <polyline points="10,55 23,46 36,38 49,48 62,34 75,40 90,30" fill="none" stroke="#bbf7d0" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"></polyline>
  <circle cx="90" cy="30" r="2.6" fill="#bbf7d0"></circle>
</svg>

<!-- 3. Quản lý tuần tra -->
<svg width="100" height="80" viewBox="0 0 100 80" aria-hidden="true">
  <!-- top icon cluster: vị trí / QR code / thiết bị hệ thống -->
  <rect x="4" y="4" width="28" height="20" rx="2.5" fill="#0b1422" stroke="#fbbf24" stroke-width="1.6"></rect>
  <rect x="36" y="4" width="28" height="20" rx="2.5" fill="#0b1422" stroke="#fbbf24" stroke-width="1.6"></rect>
  <rect x="68" y="4" width="28" height="20" rx="2.5" fill="#0b1422" stroke="#fbbf24" stroke-width="1.6"></rect>
  <path d="M18 8 C21.5 8 24 10.6 24 13.8 C24 17.8 18 22 18 22 C18 22 12 17.8 12 13.8 C12 10.6 14.5 8 18 8 Z" fill="none" stroke="#fbbf24" stroke-width="1.4"></path>
  <circle cx="18" cy="13.6" r="2" fill="#fbbf24"></circle>
  <rect x="43" y="7" width="4.4" height="4.4" fill="#fbbf24"></rect>
  <rect x="49" y="7" width="4.4" height="4.4" fill="#fbbf24" opacity=".5"></rect>
  <rect x="55" y="7" width="4.4" height="4.4" fill="#fbbf24"></rect>
  <rect x="43" y="13" width="4.4" height="4.4" fill="#fbbf24" opacity=".5"></rect>
  <rect x="49" y="13" width="4.4" height="4.4" fill="#fbbf24"></rect>
  <rect x="55" y="13" width="4.4" height="4.4" fill="#fbbf24" opacity=".5"></rect>
  <rect x="43" y="19" width="4.4" height="4.4" fill="#fbbf24"></rect>
  <rect x="49" y="19" width="4.4" height="4.4" fill="#fbbf24" opacity=".5"></rect>
  <rect x="55" y="19" width="4.4" height="4.4" fill="#fbbf24"></rect>
  <line x1="82" y1="5" x2="82" y2="8" stroke="#fbbf24" stroke-width="1.4" stroke-linecap="round"></line>
  <rect x="76" y="8" width="12" height="14" rx="2.2" fill="none" stroke="#fbbf24" stroke-width="1.4"></rect>
  <rect x="78.3" y="10.3" width="7.4" height="4" rx=".6" fill="#fbbf24" opacity=".5"></rect>
  <circle cx="82" cy="18" r="1.1" fill="#fbbf24"></circle>
  <!-- main screen: tuyến tuần tra, điểm chốt, nhân viên tuần tra -->
  <rect x="4" y="30" width="92" height="46" rx="3" fill="#0b1422" stroke="#4a3a12" stroke-width="2"></rect>
  <polyline points="12,66 30,48 48,58 68,40 88,52" fill="none" stroke="#fbbf24" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" stroke-dasharray="1 6"></polyline>
  <circle cx="12" cy="66" r="3.2" fill="#fbbf24"></circle>
  <circle cx="48" cy="58" r="3.2" fill="#fbbf24"></circle>
  <g transform="translate(88,52) scale(0.95)">
    <circle cx="0" cy="-5.4" r="3.4" fill="#fbbf24"></circle>
    <path d="M-6 10 C-6 2 -3.2 -1 0 -1 C3.2 -1 6 2 6 10 Z" fill="#fbbf24" opacity=".85"></path>
    <circle cx="5.4" cy="6.4" r="3.4" fill="#0b1422" stroke="#fbbf24" stroke-width="1.1"></circle>
    <path d="M3.9 6.4 L4.9 7.5 L7 5.1" fill="none" stroke="#fbbf24" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"></path>
  </g>
</svg>
```

Ghi chú: cả 3 minh họa đều vẽ bằng SVG thuần (không dùng ảnh raster/photo nhúng base64) để trang nhẹ, sắc nét ở mọi độ phân giải, và không phụ thuộc file ảnh ngoài — đúng tinh thần "trang KPI nhẹ và ổn định".

## Phụ lục B — Checklist triển khai cho agent KPI

- [ ] Host trang trên hạ tầng KPI (không phải go2rtc).
- [ ] Không dùng từ "Portal"/"cổng" ở bất kỳ đâu trong UI, code, tài liệu.
- [ ] Không yêu cầu đăng nhập để xem trang này.
- [ ] Không hiển thị domain/URL dưới mỗi thẻ.
- [ ] Không hiển thị mô tả dưới mỗi thẻ — chỉ icon minh họa + tên.
- [ ] Không có khối hero/tiêu đề phụ đề ở đầu trang.
- [ ] 3 thẻ: Điều hành Giao thông / Quản lý hiệu suất công việc / Quản lý tuần tra — không có thẻ "Hệ thống khác" placeholder.
- [ ] Click thẻ → điều hướng toàn trang sang URL hệ thống tương ứng (không iframe).
- [ ] **Không có nút/link admin nào trên trang** — trang quản trị https-proxy không được liên kết hay gợi ý tồn tại từ trang này dưới bất kỳ hình thức nào.
- [ ] Minh họa 3 thẻ vẽ bằng SVG thuần theo mẫu Phụ lục A (không dùng ảnh raster/photo, không logo hãng thứ ba).
- [ ] Dùng đúng bộ màu/font ở mục 3 để đồng bộ với `www/login.html` đã triển khai.
- [ ] Xác nhận lại các URL thật ở mục 5 trước khi gắn cứng vào code.
