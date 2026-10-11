# UTMC Platform — Hệ thống thiết kế dùng chung (Design System)

## 0. Mục đích & phạm vi áp dụng

Tài liệu này mô tả **bộ nhận diện giao diện dùng chung** cho toàn bộ các hệ thống thuộc UTMC Platform:

- Trang đăng nhập (`www/login.html` — go2rtc; trang đăng nhập KPI tại `kpi.utmc.io.vn/utmc`)
- Trang điều hướng "UTMC Platform" (xem `docs/utmc-platform-landing-spec.md`)
- Trang quản trị https-proxy (`cmd/https-proxy/admin.html`)
- Từng hệ thống nghiệp vụ: Điều hành Giao thông (go2rtc), Quản lý hiệu suất công việc (KPI), Quản lý tuần tra, và các hệ thống tương lai

**Hiện trạng (đã triển khai thật trong go2rtc):** `www/login.html` và toàn bộ trang nội bộ của go2rtc (`map.html`, `index.html`, `admin.html`, `incidents.html`, `config.html`... — mọi trang dùng chung `www/app.css`/`www/app.js`) nay đều có **Dark theme** (mặc định) và **Light theme** (bật bằng nút chuyển đổi), cùng một cơ chế `[data-theme="light"]` trên `<html>` + khoá `localStorage['utmc_theme']` dùng chung. Đây là bản tham chiếu thực tế (nguồn chân lý về giá trị token) cho các hệ thống khác — KPI, Quản lý tuần tra, https-proxy — áp dụng theo, thay vì chỉ coi các bảng dưới đây là đề xuất.

**Lưu ý quan trọng — 2 bộ token song song trong go2rtc, CÙNG MỘT MÀU NHƯNG KHÁC TÊN BIẾN:**
- `www/login.html` (trang đăng nhập + landing-style): các biến `--bg-deep`, `--bg-card`, `--text-primary`, `--accent-cyan`... (namespace riêng cho giao diện "thương hiệu", xem mục 2.1).
- `www/app.css` (khung ứng dụng nội bộ — sidebar/topbar/bảng/dialog của mọi trang nghiệp vụ): các biến `--bg`, `--surface`, `--text`, `--accent`... (namespace ngắn gọn hơn, xem mục 2.2) — đây là bộ biến **đã có từ trước** khi xây dựng tài liệu này, nay chỉ đổi **giá trị** sang bộ màu navy/cyan, giữ nguyên **tên biến** để không phải sửa hàng trăm chỗ `var(--bg)` rải rác trong `app.css`.

Một hệ thống mới (KPI, tuần tra...) có thể chọn 1 trong 2 bộ tên biến này tuỳ loại trang (trang "thương hiệu" như landing/login dùng bộ 2.1; trang "khung ứng dụng" nhiều bảng/dialog dùng bộ 2.2) — quan trọng là **giá trị màu** và **cơ chế `data-theme` + `utmc_theme`** phải giống nhau.

**Phong cách chủ đạo:** hiện đại, công nghệ, tinh thần "trung tâm điều hành số / chuyển đổi số" — nền động dạng lưới mạng (network canvas), gradient thương hiệu xanh dương → cyan, bo góc mềm, hiệu ứng kính mờ (glassmorphism) cho các khối nổi, không dùng icon emoji (chỉ SVG tự vẽ).

---

## 1. Font chữ

| Vai trò | Font | Trọng lượng |
|---|---|---|
| Thương hiệu / tiêu đề | `Space Grotesk` | 600, 700 |
| Nội dung / form / body | `Plus Jakarta Sans` | 400, 500, 600, 700 |

```html
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700&family=Space+Grotesk:wght@600;700&display=swap" rel="stylesheet">
```

---

## 2. Design tokens — CSS variables (giá trị thật, lấy từ code đã chạy)

**Nguyên tắc chung:** `:root` chứa giá trị **Dark theme (mặc định)**. **Light theme** là override qua `:root[data-theme="light"]`, kích hoạt bằng thuộc tính `data-theme="light"` trên thẻ `<html>` (không phải class trên `<body>`). Một script nhỏ, đồng bộ, đặt ngay đầu `<head>` (trước mọi CSS) đọc `localStorage['utmc_theme']` và set thuộc tính này sớm nhất có thể để tránh nháy (flash) sai theme lúc tải trang — xem mẫu ở mục 4.6.

### 2.1 Trang "thương hiệu" (đăng nhập, landing) — theo đúng `www/login.html`

```css
:root {
  --bg-deep: #050b16;
  --bg-card: rgba(10, 19, 35, 0.82);
  --bg-card-border: rgba(0, 240, 255, 0.35);
  --accent-cyan: #00f0ff;
  --accent-blue: #0077ff;
  --accent-glow: rgba(0, 240, 255, 0.5);
  --text-primary: #ffffff;
  --text-secondary: #94a3b8;
  --text-muted: #64748b;
  --input-bg: rgba(255, 255, 255, 0.05);
  --input-border: rgba(148, 163, 184, 0.28);
  --input-focus: #00f0ff;
  --input-text: #ffffff;
  --input-hover-border: rgba(0, 240, 255, 0.45);
  --input-hover-bg: rgba(255, 255, 255, 0.08);
  --focus-tint-bg: rgba(0, 240, 255, 0.06);
  --focus-ring: rgba(0, 240, 255, 0.2);
  --field-label-color: #cbd5e1;
  --link-hover-color: #ffffff;
  --danger: #f85149;
  --glow-1: rgba(0, 119, 255, 0.18);       /* radial-gradient góc trên-trái */
  --glow-2: rgba(0, 240, 255, 0.14);       /* radial-gradient góc dưới-phải */
  --vignette: linear-gradient(to bottom, rgba(5, 11, 22, 0.5) 0%, rgba(3, 7, 15, 0.85) 100%);
  --topbar-bg: rgba(5, 11, 22, 0.72);
  --badge-bg: rgba(255, 255, 255, 0.06);   /* khung logo giữa thẻ */
  --badge-border: rgba(255, 255, 255, 0.14);
  --hairline: rgba(255, 255, 255, 0.08);   /* viền phân cách mảnh */
  --overlay-bg: rgba(5, 11, 22, 0.9);      /* lớp phủ loading radar */
  --title-grad: linear-gradient(135deg, #ffffff 40%, var(--accent-cyan) 100%);
  --status-pill-text: #e2e8f0;
  --radius-lg: 22px;
  --radius-md: 12px;
  --transition: all 0.28s cubic-bezier(0.4, 0, 0.2, 1);
}

:root[data-theme="light"] {
  --bg-deep: #f2f6fb;
  --bg-card: rgba(255, 255, 255, 0.92);
  --bg-card-border: rgba(0, 119, 255, 0.25);
  --accent-cyan: #0077ff;    /* cyan #00f0ff gần như vô hình trên nền trắng — dùng xanh dương đậm hơn */
  --accent-blue: #0077ff;
  --accent-glow: rgba(0, 119, 255, 0.35);
  --text-primary: #0f1b2d;
  --text-secondary: #46536b;
  --text-muted: #7c8aa0;
  --input-bg: rgba(15, 27, 45, 0.035);
  --input-border: rgba(15, 27, 45, 0.16);
  --input-focus: #0077ff;
  --input-text: #0f1b2d;
  --input-hover-border: rgba(0, 119, 255, 0.4);
  --input-hover-bg: rgba(15, 27, 45, 0.05);
  --focus-tint-bg: rgba(0, 119, 255, 0.05);
  --focus-ring: rgba(0, 119, 255, 0.16);
  --field-label-color: #334155;
  --link-hover-color: #0f1b2d;
  --danger: #dc2626;
  --glow-1: rgba(0, 119, 255, 0.1);
  --glow-2: rgba(0, 150, 255, 0.08);
  --vignette: linear-gradient(to bottom, rgba(255, 255, 255, 0) 0%, rgba(214, 227, 242, 0.55) 100%);
  --topbar-bg: rgba(255, 255, 255, 0.78);
  --badge-bg: rgba(15, 27, 45, 0.04);
  --badge-border: rgba(15, 27, 45, 0.12);
  --hairline: rgba(15, 27, 45, 0.08);
  --overlay-bg: rgba(242, 246, 251, 0.92);
  /* Gradient tiêu đề "trắng → cyan" không đọc được trên nền trắng — bắt
     đầu bằng màu chữ tối thay vì trắng. Cùng lý do với --status-pill-text:
     chữ xám-trắng #e2e8f0 trên pill xanh lá NHẠT (nền sáng) gần như vô
     hình, nên light theme cần chữ pill tối màu hơn. */
  --title-grad: linear-gradient(135deg, #0f1b2d 20%, var(--accent-cyan) 100%);
  --status-pill-text: #065f46;
}
```

Màu nút gradient chính (`.btn-submit-login`, `linear-gradient(135deg, #0077ff 0%, #00f0ff 100%)`) và nền `.auth-card::before` **không đổi theo theme** — luôn có nền đặc phía sau nên chữ/viền trắng trên đó vẫn đủ tương phản ở cả 2 theme; đây là phần "thương hiệu" cố định, không nên biến thành biến theo theme.

### 2.2 Khung ứng dụng nội bộ (sidebar/topbar/bảng/dialog mọi trang nghiệp vụ) — theo đúng `www/app.css`

Bộ biến này **đã tồn tại từ trước** (tên biến ngắn, dùng ở hàng trăm nơi trong `app.css` và các trang nghiệp vụ); khi thống nhất nhận diện, chỉ cần đổi **giá trị** sang navy/cyan, giữ nguyên **tên biến** — nên nếu hệ thống khác (KPI...) đã có sẵn một bộ biến tương tự, cách rẻ nhất là làm y hệt: đổi giá trị, giữ tên.

```css
:root {
  --bg:          #050b16;
  --surface:     #0b1422;
  --surface2:    #111c30;
  --border:      #1e2d45;
  --border-soft: #152238;
  --text:        #e6edf3;
  --text-muted:  #94a3b8;
  --accent:      #0077ff;
  --accent-h:    #00c2ff;
  --green:       #3fb950;   /* trạng thái — không đổi theo thương hiệu/theme */
  --red:         #f85149;
  --yellow:      #d29922;
  --radius:      8px;
  --radius-lg:   12px;
  --sidebar-w:   220px;
  --topbar-h:    56px;
  --transition:  .15s ease;
}

:root[data-theme="light"] {
  --bg:          #f2f6fb;
  --surface:     #ffffff;
  --surface2:    #eef2f8;
  --border:      #d7dfea;
  --border-soft: #e2e8f1;
  --text:        #0f1b2d;
  --text-muted:  #46536b;
  --accent:      #0077ff;
  --accent-h:    #0062d6;
  --green:       #1a7f37;
  --red:         #d1242f;
  --yellow:      #9a6700;
}
```

Màu trạng thái (`--green`/`--red`/`--yellow`) chỉ đổi sắc độ nhẹ cho đủ tương phản ở Light, **không** đổi theo logic thương hiệu — đây là màu ngữ nghĩa (thành công/lỗi/cảnh báo), giữ tinh thần "xanh=ổn, đỏ=lỗi" xuyên suốt, không phải là thứ cần đồng bộ theo mục 2.1.

---

## 3. Nền "chuyển đổi số" (network canvas + ambient glow)

Đây là yếu tố thị giác đặc trưng nhất — một lớp `<canvas>` vẽ các điểm (node) chuyển động chậm, tự nối với nhau bằng đường kẻ mờ khi đủ gần, gợi liên tưởng mạng lưới dữ liệu/hạ tầng số. Dùng làm nền cho trang đăng nhập và các trang "cổng vào" (landing). Với các trang nghiệp vụ nhiều nội dung (dashboard, bảng dữ liệu) có thể bỏ canvas động, chỉ giữ `--bg` + ambient glow tĩnh để đỡ phân tán.

```html
<canvas id="netCanvas" style="position:fixed;inset:0;width:100%;height:100%;z-index:0;"></canvas>
<div style="position:fixed;inset:0;z-index:0;pointer-events:none;
  background:
    radial-gradient(circle at 15% 10%, var(--bg-glow-1) 0%, transparent 45%),
    radial-gradient(circle at 85% 85%, var(--bg-glow-2) 0%, transparent 50%);"></div>
```

```js
(function () {
  const canvas = document.getElementById('netCanvas');
  const ctx = canvas.getContext('2d');
  let w, h, nodes;

  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  function resize() {
    w = canvas.width = window.innerWidth;
    h = canvas.height = window.innerHeight;
    const count = Math.min(70, Math.floor((w * h) / 18000));
    nodes = Array.from({ length: count }, () => ({
      x: Math.random() * w,
      y: Math.random() * h,
      vx: (Math.random() - 0.5) * 0.25,
      vy: (Math.random() - 0.5) * 0.25
    }));
  }

  function render() {
    const dot = cssVar('--particle-color') || '#00f0ff';
    const line = cssVar('--particle-line') || 'rgba(0,240,255,.18)';
    ctx.clearRect(0, 0, w, h);
    for (const n of nodes) {
      n.x += n.vx; n.y += n.vy;
      if (n.x < 0 || n.x > w) n.vx *= -1;
      if (n.y < 0 || n.y > h) n.vy *= -1;
    }
    for (let i = 0; i < nodes.length; i++) {
      for (let j = i + 1; j < nodes.length; j++) {
        const dx = nodes[i].x - nodes[j].x, dy = nodes[i].y - nodes[j].y;
        const d = Math.hypot(dx, dy);
        if (d < 130) {
          ctx.strokeStyle = line;
          ctx.globalAlpha = 1 - d / 130;
          ctx.lineWidth = 1;
          ctx.beginPath();
          ctx.moveTo(nodes[i].x, nodes[i].y);
          ctx.lineTo(nodes[j].x, nodes[j].y);
          ctx.stroke();
        }
      }
    }
    ctx.globalAlpha = 1;
    ctx.fillStyle = dot;
    for (const n of nodes) {
      ctx.beginPath();
      ctx.arc(n.x, n.y, 1.6, 0, Math.PI * 2);
      ctx.fill();
    }
    requestAnimationFrame(render);
  }

  window.addEventListener('resize', resize);
  resize();
  render();
})();
```

> Script đọc màu hạt/đường nối trực tiếp từ CSS variable mỗi khung hình — khi người dùng đổi theme (đổi `data-theme` trên `<html>`), nền tự đổi màu theo, không cần viết lại logic canvas.

**Bản đầy đủ đã triển khai thật** (đậm chất hơn: node "nhịp thở"/pulse, các chấm telemetry trôi dọc theo cạnh giữa 2 node với nhiều màu khác nhau) nằm ở `www/login.html` (hàm `initMeshNetwork()`/`render()`/`currentPalette()`). Nguyên tắc tương tự khối code trên — chỉ phức tạp hơn; `currentPalette()` đọc `document.documentElement.getAttribute('data-theme')` mỗi khung hình thay vì đọc CSS variable trực tiếp (để đổi được nhiều giá trị màu cùng lúc theo theme — mảng màu particle, màu ring...), nhưng là cùng một kỹ thuật "tính lại palette mỗi frame, không cần reinit". Hệ thống nào muốn hiệu ứng nền giống hệt trang đăng nhập go2rtc nên copy trực tiếp khối JS này thay vì viết lại từ mẫu rút gọn ở trên.

---

## 4. Thành phần dùng chung (component patterns)

Toàn bộ dùng biến từ mục 2, không hard-code màu, để tự động đúng ở cả 2 theme.

### 4.1 Topbar / brand lockup

```html
<div style="display:flex;align-items:center;gap:14px;">
  <div style="width:40px;height:40px;border-radius:11px;background:var(--brand-grad);
    display:flex;align-items:center;justify-content:center;
    font-family:'Space Grotesk',sans-serif;font-weight:700;color:var(--brand-on-grad);font-size:15px;">U</div>
  <div style="font-family:'Space Grotesk',sans-serif;font-size:20px;font-weight:700;letter-spacing:.3px;
    background:var(--brand-grad-text);-webkit-background-clip:text;-webkit-text-fill-color:transparent;">
    UTMC PLATFORM
  </div>
</div>
```

### 4.2 Nút bấm (buttons)

```html
<!-- Primary: nền gradient thương hiệu -->
<a href="#" style="display:inline-flex;align-items:center;gap:8px;padding:9px 20px;border-radius:30px;
  background:var(--brand-grad);color:var(--brand-on-grad);text-decoration:none;
  font-size:13px;font-weight:700;box-shadow:var(--shadow-btn);">Đăng nhập</a>

<!-- Ghost: viền mỏng, dùng cho hành động phụ -->
<a href="#" style="display:inline-flex;align-items:center;gap:8px;padding:9px 18px;border-radius:30px;
  border:1px solid var(--surface-border);color:var(--text-secondary);text-decoration:none;
  font-size:13px;font-weight:600;">Giới thiệu</a>
```

### 4.3 Thẻ (card) — 2 biến thể

```html
<!-- Solid-elevated: dùng cho thẻ hệ thống, khối nội dung chính -->
<div style="padding:28px 24px;border-radius:18px;background:var(--surface);
  border:1px solid var(--surface-border);transition:border-color .2s,transform .2s,box-shadow .2s;">
  ...
</div>
<!-- hover (JS/CSS :hover) → border-color: var(--surface-hover-border); transform: translateY(-4px); box-shadow: var(--shadow-card); -->

<!-- Glass/frosted: dùng cho badge nổi bật, huy hiệu trạng thái -->
<div style="padding:16px;border-radius:17px;background:var(--glass-bg);
  border:1px solid var(--glass-border);backdrop-filter:blur(6px);">
  ...
</div>
```

### 4.4 Trường nhập liệu (input) có icon

```html
<label style="display:block;font-size:12.5px;color:var(--text-secondary);margin-bottom:6px;">Tên đăng nhập</label>
<div style="display:flex;align-items:center;gap:10px;background:var(--input-bg);
  border:1px solid var(--input-border);border-radius:12px;padding:11px 14px;">
  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="var(--text-muted)" stroke-width="2">
    <circle cx="12" cy="8" r="4"></circle><path d="M4 20c0-4.4 3.6-7 8-7s8 2.6 8 7"></path>
  </svg>
  <input type="text" placeholder="Nhập tên đăng nhập" style="flex:1;border:0;background:transparent;
    outline:none;color:var(--text-primary);font-family:'Plus Jakarta Sans',sans-serif;font-size:14px;">
</div>
<!-- lỗi: đổi border-color input thành var(--error), thêm dòng text màu var(--error) bên dưới -->
```

### 4.5 Badge / status pill

```html
<span style="display:inline-flex;align-items:center;gap:6px;padding:4px 10px;border-radius:20px;
  background:var(--glass-bg);border:1px solid var(--glass-border);color:var(--text-secondary);
  font-size:11.5px;font-weight:600;">
  <span style="width:6px;height:6px;border-radius:50%;background:#4ade80;"></span>
  Online
</span>
```

### 4.6 Bộ chuyển đổi theme (Theme toggle)

```html
<button id="themeToggle" title="Đổi giao diện sáng/tối" aria-label="Đổi giao diện sáng/tối"
  style="width:34px;height:34px;border-radius:50%;border:1px solid var(--surface-border);
  background:var(--surface);color:var(--text-secondary);display:flex;align-items:center;justify-content:center;cursor:pointer;">
  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
    <circle cx="12" cy="12" r="4"></circle>
    <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"></path>
  </svg>
</button>
```

```js
const THEME_KEY = 'utmc_theme'; // dùng chung 1 key cho mọi hệ thống trên cùng domain cha (nếu có)

function applyTheme(theme) {
  if (theme === 'light') document.documentElement.setAttribute('data-theme', 'light');
  else document.documentElement.removeAttribute('data-theme'); // không set = dark (mặc định)
}

// Khởi tạo: ưu tiên lựa chọn đã lưu, mặc định là Dark (giữ nguyên hành vi hiện tại của toàn hệ thống)
applyTheme(localStorage.getItem(THEME_KEY) || 'dark');

document.getElementById('themeToggle').addEventListener('click', () => {
  const next = document.documentElement.getAttribute('data-theme') === 'light' ? 'dark' : 'light';
  localStorage.setItem(THEME_KEY, next);
  applyTheme(next);
});
```

> **Quan trọng:** mặc định vẫn là **Dark** (không set `data-theme`) để không làm thay đổi giao diện hiện có của người dùng cũ. Light theme là lựa chọn người dùng tự bật, lưu theo trình duyệt (`localStorage`). Mỗi hệ thống tự quản lý `localStorage` riêng trên domain của mình — nếu muốn đồng bộ lựa chọn theme giữa các hệ thống (ví dụ chọn Light ở trang KPI thì sang go2rtc cũng là Light), cần domain cha chung hoặc 1 cơ chế đồng bộ riêng (không bắt buộc ở giai đoạn này).

---

## 5. Hướng dẫn tích hợp theo từng hệ thống

| Hệ thống | Trạng thái |
|---|---|
| **go2rtc — `www/login.html`** | ✅ Đã triển khai đầy đủ: token mục 2.1, nền network canvas theme-aware (mục 3), nút toggle ở topbar (mục 4.6), script chống-nháy ở đầu `<head>`. |
| **go2rtc — mọi trang nội bộ** (`map.html`, `index.html`, `admin.html`, `incidents.html`, `config.html`... qua `www/app.css`/`www/app.js`) | ✅ Đã triển khai đầy đủ: token mục 2.2, nút toggle tự động chèn vào chân sidebar mọi trang (`#btnTheme`, `www/app.js`), cùng khoá `utmc_theme`. Map tự thân (basemap VietMap sáng/tối) là nút **khác**, không liên quan — xem ghi chú ở mục 0. |
| **UTMC Platform landing** (xem `docs/utmc-platform-landing-spec.md`, host trên hạ tầng KPI) | ⏳ Chưa code — khi code, dùng ngay CSS variables ở mục 2.1 (cùng namespace với login.html, vì đây cũng là trang "thương hiệu") thay vì màu hard-code trong mockup gốc. |
| **https-proxy admin** (`cmd/https-proxy/admin.html`) | ⏳ Trang đăng nhập đã theo Dark theme (chưa có Light/toggle); phần dashboard quản trị còn lại giữ giao diện cũ. Khi làm Light theme cho hệ thống này, áp dụng đúng mục 2.1 + 4.6 như đã làm cho `login.html`. |
| **KPI** (`kpi.utmc.io.vn/utmc`) | ⏳ Trang đăng nhập + landing đã triển khai Dark theo thống nhất trước đó. Bổ sung Light theme bằng cách nhúng bộ CSS variables mục 2.1 + script toggle mục 4.6, đổi màu hard-code sang `var(--...)` — theo đúng cách `www/login.html` vừa làm (xem diff của file đó nếu cần một ví dụ "trước/sau" cụ thể). |
| **Quản lý tuần tra** (tương lai) | ⏳ Khi phát triển giao diện, dùng trực tiếp bộ token + component ở tài liệu này làm nền tảng, không tạo bảng màu riêng. |

---

## 6. Checklist cho agent triển khai

- [ ] Nhúng 2 khối CSS variable (`:root` + `:root[data-theme="light"]`) ở mục 2 vào stylesheet gốc của hệ thống.
- [ ] Thay toàn bộ màu hard-code trong CSS/inline-style hiện có bằng `var(--token-tương-ứng)`.
- [ ] Thêm nút theme toggle (mục 4.6) vào topbar, mặc định hiển thị Dark theme nếu người dùng chưa từng chọn.
- [ ] Nếu trang có nền "network canvas" (trang đăng nhập, landing): dùng script mục 3, đảm bảo đọc màu qua CSS variable để tự đổi theo theme.
- [ ] Kiểm tra độ tương phản chữ/nền ở cả 2 theme (đặc biệt văn bản phụ `--text-secondary`/`--text-muted` trên nền `--surface`).
- [ ] Không dùng emoji làm icon — dùng SVG tự vẽ theo phong cách đã có (line-icon mảnh, bo góc, phối màu theo `--accent`).
- [ ] Test chuyển đổi theme qua lại, xác nhận lựa chọn được giữ lại sau khi tải lại trang (`localStorage`).

---

## Phụ lục — Mockup trực quan so sánh Dark/Light

Mockup so sánh trực tiếp 2 theme (topbar, thẻ, nút, input, badge, nền "network dots") đặt cạnh nhau để tham khảo màu sắc/tỷ lệ tương phản trước khi tích hợp vào từng hệ thống:
`https://claude.ai/artifact/BCXjDRkHQrt3R6gaAPi32K`

(Mockup trang UTMC Platform landing: `https://claude.ai/artifact/QPyCs13xh2p18AyjXXGyUj` — xem thêm `docs/utmc-platform-landing-spec.md`.)
