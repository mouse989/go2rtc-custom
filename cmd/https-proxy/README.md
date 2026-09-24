# https-proxy — HTTPS dùng chung cho go2rtc và các trang web khác

Chương trình độc lập (một file `https-proxy.exe`) chiếm cổng 80/443 của máy chủ,
tự xin và gia hạn chứng chỉ Let's Encrypt, rồi chuyển tiếp từng domain vào đúng
trang web nội bộ: go2rtc và mọi ứng dụng web khác chạy trên cùng máy.

Trước đây phần proxy này nằm bên trong go2rtc, nên go2rtc treo là tất cả các
trang web khác cũng chết theo. Giờ hai bên chạy thành hai tiến trình riêng: go2rtc
treo hoặc khởi động lại thì chỉ domain camera bị ảnh hưởng (hiện trang lỗi 502/504),
các trang còn lại vẫn chạy bình thường.

```
Internet ──443/80──► https-proxy.exe ──► http://127.0.0.1:1984  (go2rtc)
                                     ├─► http://127.0.0.1:3000  (trang web A)
                                     └─► http://127.0.0.1:8080  (trang web B)
```

## Build

```bat
scripts\build_https_proxy_win.bat      REM → dist\https-proxy.exe
```

hoặc trên Linux/macOS: `scripts/build_https_proxy.sh`.

## Chuyển từ proxy cũ trong go2rtc sang https-proxy

1. **Sửa `go2rtc.yaml`**: go2rtc chỉ lắng nghe HTTP nội bộ và không giữ cổng 80/443 nữa:
   ```yaml
   api:
     listen: ":1984"        # go2rtc giờ chỉ phục vụ HTTP nội bộ
     # xoá (hoặc comment) các dòng: tls_listen, acme_domain, acme_email
   ```
   Khởi động lại go2rtc.
2. Chép `dist\https-proxy.exe` vào thư mục go2rtc (cùng thư mục với `go2rtc.yaml`).
   Nếu thư mục đó có sẵn `reverse_proxy.json` (danh sách trang của proxy cũ),
   lần chạy đầu sẽ tự nhập. Thư mục `certs` của go2rtc dùng lại được luôn
   (cùng định dạng), nên không phải xin lại chứng chỉ.
3. Chạy `run-https-proxy.bat` (hoặc cài thành service, xem dưới).
4. Mở `http://127.0.0.1:8090` **trên máy chủ**, đăng nhập `admin` với mật khẩu trong
   file `https-proxy-initial-password.txt`, rồi đổi mật khẩu.
5. Bấm **+ Thêm trang** cho domain camera: domain `cam.example.com` →
   `http://127.0.0.1:1984`, chứng chỉ Let's Encrypt. Thêm tiếp các trang web khác.
   Chọn trang mặc định nếu người dùng vào bằng IP.

## Chạy như Windows service (khuyến nghị)

Mở cửa sổ lệnh **Run as Administrator**:

```bat
dist\https-proxy.exe -service install
dist\https-proxy.exe -service start
```

Service tự chạy khi Windows khởi động và tự khởi động lại nếu tiến trình bị dừng.
Gỡ: `-service stop`, rồi `-service uninstall`. Service đọc file cấu hình nằm cạnh
exe (hoặc file chỉ định bằng `-c` lúc install).

## Trang cấu hình

- **Các trang web**: domain → địa chỉ nội bộ, loại chứng chỉ (Let's Encrypt tự động /
  file .crt+.key có sẵn / không HTTPS), bật/tắt, cho phép HTTP thường. Có trạng thái
  máy chủ phía sau (kết nối được hay không) và hạn chứng chỉ.
- **Cài đặt chung**: cổng HTTPS/HTTP (nhiều cổng cách nhau dấu phẩy, ví dụ
  `:443,:8443`), địa chỉ trang quản trị, email Let's Encrypt, thư mục chứng chỉ,
  domain riêng cho trang quản trị (tuỳ chọn), trang mặc định.
- **Nhật ký**: 500 dòng gần nhất. Đầy đủ hơn trong `https-proxy.log`.

Thay đổi được áp dụng ngay, không cần khởi động lại.

## Ghi chú kỹ thuật

- Header `X-Forwarded-For`, `X-Forwarded-Proto` và `X-Forwarded-Host` được tạo lại
  từ kết nối thật (giá trị client tự gửi bị bỏ), nên go2rtc ghi đúng IP người dùng
  vào lịch sử đăng nhập và nhận biết truy cập qua HTTPS (cookie Secure, `wss://`).
  Header `X-Internal` bị xoá.
- Hỗ trợ WebSocket, HTTP/2, MJPEG/HLS streaming (không đệm).
- Máy chủ phía sau không phản hồi: trả 502 sau 5 giây nếu không kết nối được,
  504 nếu quá thời gian chờ (mặc định 120 giây, chỉnh được từng trang).
- Chứng chỉ file thủ công được đọc lại tự động khi file thay đổi (ví dụ khi
  win-acme gia hạn).
- Let's Encrypt cần: DNS của domain trỏ về IP máy chủ, và cổng 80 (HTTP-01) hoặc 443
  (TLS-ALPN-01) mở từ Internet.
