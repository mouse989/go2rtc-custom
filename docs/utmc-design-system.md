# UTMC Platform — Hệ thống thiết kế dùng chung (Design System)

## 0. Mục đích & phạm vi áp dụng

Tài liệu này mô tả **bộ nhận diện giao diện dùng chung** cho toàn bộ các hệ thống thuộc UTMC Platform:

- Trang đăng nhập (`www/login.html` — go2rtc; trang đăng nhập KPI tại `kpi.utmc.io.vn/utmc`)
- Trang điều hướng "UTMC Platform" (xem `docs/utmc-platform-landing-spec.md`)
- Trang quản trị https-proxy (`cmd/https-proxy/admin.html`)
- Từng hệ thống nghiệp vụ: Điều hành Giao thông (go2rtc), Quản lý hiệu suất công việc (KPI), Quản lý tuần tra, và các hệ thống tương lai

**Hiện trạng:** Các giao diện trên (login.html, landing page UTMC Platform, https-proxy admin) đã triển khai theo **1 theme duy nhất** — nền rất tối, gọi là **Dark theme** (mặc định/chính thức). Tài liệu này bổ sung thêm **Light theme** làm lựa chọn thứ hai, và chuẩn hoá toàn bộ token/thành phần để mọi hệ thống — kể cả hệ thống KPI, Quản lý tuần tra sau này — dùng chung một ngôn ngữ thiết kế, chuyển đổi được giữa 2 theme.

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

## 2. Design tokens — CSS variables dùng chung

**Nguyên tắc:** `:root` chứa giá trị **Dark theme (mặc định)** — giữ nguyên hành vi hiện tại của mọi hệ thống đã triển khai, không ai cần đổi gì nếu chưa làm theme toggle. **Light theme** là override, kích hoạt bằng thuộc tính `data-theme="light"` trên thẻ `<html>`.

```css
:root {
  /* ---- Dark theme (mặc định) ---- */
  --bg: #050b16;
  --bg-glow-1: rgba(0,119,255,.16);
  --bg-glow-2: rgba(0,240,255,.12);

  --surface: rgba(10,19,35,.82);
  --surface-border: rgba(255,255,255,.1);
  --surface-hover-border: rgba(0,240,255,.45);

  --glass-bg: rgba(255,255,255,.055);
  --glass-border: rgba(255,255,255,.32);
  --glass-sheen: rgba(255,255,255,.07);

  --text-primary: #e6edf3;
  --text-secondary: #94a3b8;
  --text-muted: #64748b;

  --brand-grad: linear-gradient(135deg, #0077ff, #00f0ff);
  --brand-grad-text: linear-gradient(135deg, #ffffff 40%, #00f0ff 100%);
  --brand-on-grad: #04121f;      /* chữ/icon đặt TRÊN nền gradient */
  --accent: #00f0ff;             /* dùng cho icon/chữ nhấn, viền hover */
  --accent-2: #0077ff;

  --border-subtle: rgba(255,255,255,.08);
  --shadow-card: 0 14px 34px -10px rgba(0,0,0,.6), 0 0 0 1px rgba(0,240,255,.12);
  --shadow-btn: 0 6px 18px -4px rgba(0,119,255,.5);

  --input-bg: #0b1422;
  --input-border: rgba(255,255,255,.14);
  --error: #ef4444;

  --particle-color: #00f0ff;     /* nền network canvas: màu hạt */
  --particle-line: rgba(0,240,255,.18); /* nền network canvas: màu đường nối */
}

:root[data-theme="light"] {
  /* ---- Light theme (mới) ---- */
  --bg: #f2f6fb;
  --bg-glow-1: rgba(0,119,255,.08);
  --bg-glow-2: rgba(0,180,255,.08);

  --surface: rgba(255,255,255,.92);
  --surface-border: rgba(15,27,45,.09);
  --surface-hover-border: rgba(0,119,255,.4);

  --glass-bg: rgba(255,255,255,.6);
  --glass-border: rgba(15,27,45,.14);
  --glass-sheen: rgba(255,255,255,.5);

  --text-primary: #0f1b2d;
  --text-secondary: #46536b;
  --text-muted: #7c8aa0;

  --brand-grad: linear-gradient(135deg, #0077ff, #00c2ff);
  --brand-grad-text: linear-gradient(135deg, #0b3a66 20%, #0090c7 100%);
  --brand-on-grad: #ffffff;
  --accent: #0077ff;
  --accent-2: #0062d6;

  --border-subtle: rgba(15,27,45,.08);
  --shadow-card: 0 12px 28px -12px rgba(15,27,45,.18), 0 0 0 1px rgba(0,119,255,.07);
  --shadow-btn: 0 6px 16px -4px rgba(0,119,255,.3);

  --input-bg: #ffffff;
  --input-border: rgba(15,27,45,.16);
  --error: #dc2626;

  --particle-color: #0077ff;
  --particle-line: rgba(0,119,255,.16);
}
```

**Vì sao chữ/gradient thương hiệu khác nhẹ giữa 2 theme:** `#00f0ff` (cyan sáng) đọc rất rõ trên nền tối nhưng **mờ/kém tương phản trên nền trắng** — ở Light theme dùng `#0077ff`/`#00c2ff` đậm hơn cho chữ/viền nhấn, trong khi nút bấm nền-gradient vẫn đủ tương phản ở cả 2 theme vì luôn có nền đặc phía sau.

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

> Script đọc màu hạt/đường nối trực tiếp từ CSS variable `--particle-color` / `--particle-line` mỗi khung hình — khi người dùng đổi theme (đổi `data-theme` trên `<html>`), nền tự đổi màu theo, không cần viết lại logic canvas.

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

| Hệ thống | Việc cần làm |
|---|---|
| **go2rtc** (`www/login.html`, `map.html`, `admin.html`, `ptz-control.js`...) | Đã dùng đúng bảng màu Dark ở mục 2 (hard-code). Để hỗ trợ Light theme: refactor các giá trị màu hard-code hiện có sang CSS variable theo mục 2, thêm nút theme toggle (mục 4.6) vào topbar. |
| **UTMC Platform landing** (xem `docs/utmc-platform-landing-spec.md`, host trên hạ tầng KPI) | Khi code, dùng ngay CSS variables ở mục 2 thay vì màu hard-code trong mockup gốc, để Light theme hoạt động ngay từ đầu. |
| **https-proxy admin** (`cmd/https-proxy/admin.html`) | Tương tự go2rtc — trang đăng nhập đã theo Dark theme; phần dashboard quản trị còn lại giữ nguyên giao diện cũ, không bắt buộc đổi theo bộ token này trong giai đoạn 1. |
| **KPI** (`kpi.utmc.io.vn/utmc`) | Trang đăng nhập + landing đã triển khai theo Dark theme thống nhất. Bổ sung Light theme bằng cách nhúng bộ CSS variables + script toggle ở mục 2/3/4.6 vào trang hiện có, đổi các màu hard-code (nếu có) sang `var(--...)`. |
| **Quản lý tuần tra** (tương lai) | Khi phát triển giao diện, dùng trực tiếp bộ token + component ở tài liệu này làm nền tảng, không tạo bảng màu riêng. |

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
