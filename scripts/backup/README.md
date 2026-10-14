# Backup go2rtc-custom (Windows Server 2016)

Kiến trúc và lý do chọn (phân tích đầy đủ dữ liệu/rủi ro): xem
[`docs/data-backup-plan.md`](../../docs/data-backup-plan.md). Tóm tắt:

- Dùng **Restic** — 1 binary, tự nén + dedupe + mã hoá + quản lý retention,
  không cần tự viết logic đó.
- **2 repo Restic độc lập**: 1 trên ổ cứng local khác ổ chứa dữ liệu, 1 trên
  NAS Synology qua SMB/CIFS (UNC path) — hỏng 1 bên không ảnh hưởng bên kia.
- Thư mục dữ liệu thật trên server: `D:\Working\Counting\` (chứa
  `go2rtc.yaml` + toàn bộ thư mục con dữ liệu). go2rtc chạy trực tiếp bằng
  `.exe` (không phải Windows Service).
- `trafficflow.db` (SQLite, WAL) được chụp qua `sqlite3 .backup` thành 1 bản
  nhất quán trước mỗi lần backup, thay vì để Restic đọc trực tiếp file đang
  mở — xem `Snapshot-TrafficFlowDb` trong `Backup-Go2rtc.ps1`.

## Cài đặt lần đầu

### 1. Cài `restic.exe` và `sqlite3.exe`

- Restic: tải bản Windows amd64 từ trang release chính thức của Restic, đặt
  tại `C:\Tools\restic\restic.exe` (hoặc sửa `$ResticExe` trong script nếu
  đặt nơi khác).
- SQLite CLI: tải `sqlite-tools-win32-x86-*.zip` từ trang chính thức SQLite,
  giải nén lấy `sqlite3.exe`, đặt tại `C:\Tools\sqlite3\sqlite3.exe` (hoặc
  sửa `$Sqlite3Exe`).

Không cần cài đặt/registry gì thêm — cả 2 chỉ là 1 file `.exe` độc lập.

### 2. Tạo mật khẩu cho Restic repo + giới hạn quyền đọc file đó

```powershell
New-Item -ItemType Directory -Path "C:\go2rtc-backup-secrets" -Force
# Tự sinh 1 passphrase mạnh, dán vào file (KHÔNG commit, KHÔNG để trong D:\Working\Counting):
notepad C:\go2rtc-backup-secrets\restic-password.txt

# Giới hạn quyền đọc file này chỉ cho SYSTEM + admin:
icacls "C:\go2rtc-backup-secrets\restic-password.txt" /inheritance:r
icacls "C:\go2rtc-backup-secrets\restic-password.txt" /grant:r "SYSTEM:(R)" "Administrators:(R)"
```

⚠️ **Mất passphrase này = mất toàn bộ dữ liệu đã backup (Restic mã hoá
AES-256, không có cách khôi phục nếu quên mật khẩu).** Lưu 1 bản sao ở nơi
khác (két, password manager của bạn) — không chỉ lưu trên server đang được
backup.

### 3. Lưu thông tin đăng nhập NAS (SMB/CIFS)

Chạy lệnh dưới trong 1 cửa sổ PowerShell/cmd **chạy tay, không lưu vào file
script** (để mật khẩu NAS không nằm trong lịch sử/log):

```powershell
cmdkey /add:NAS-IP-OR-HOSTNAME /user:NASUSER /pass:NASPASSWORD
```

Nên dùng 1 user riêng trên Synology chỉ có quyền đọc/ghi đúng thư mục share
dùng cho backup (không dùng tài khoản admin NAS). Sau khi chạy `cmdkey`,
tài khoản dùng để chạy Task Scheduler (bước 5) sẽ tự dùng credential này mỗi
khi truy cập `\\NAS-IP-OR-HOSTNAME\...` — không cần map ổ đĩa.

### 4. Sửa phần Config đầu `Backup-Go2rtc.ps1`

Mở `Backup-Go2rtc.ps1`, sửa các giá trị đầu file cho đúng với server thật —
quan trọng nhất là `$NasRepo` (đổi `NAS-IP-OR-HOSTNAME` + tên share thành
giá trị thật của NAS Synology của bạn). Lần chạy đầu tiên, script tự
`restic init` cả 2 repo nếu chưa tồn tại — không cần tạo tay.

### 5. Chạy thử bằng tay trước khi đăng ký lịch

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "D:\Working\Counting\scripts\backup\Backup-Go2rtc.ps1"
```

Kiểm tra `D:\go2rtc-backups\backup.log` và chạy
`restic.exe -r <repo> --password-file <password-file> snapshots` để xác
nhận cả 2 repo đều có snapshot mới.

### 6. Đăng ký Task Scheduler (chạy hàng ngày 02:00)

Cách dễ nhất: mở **Task Scheduler** (GUI) → Create Task:

- **General**: chạy dưới tài khoản đã làm bước 3 (`cmdkey`) ở trên, chọn
  "Run whether user is logged on or not", tick "Run with highest
  privileges".
- **Triggers**: Daily, 02:00.
- **Actions**: Start a program —
  - Program: `powershell.exe`
  - Arguments:
    `-NoProfile -ExecutionPolicy Bypass -File "D:\Working\Counting\scripts\backup\Backup-Go2rtc.ps1"`

(Tương đương dòng lệnh, nếu muốn làm qua `schtasks` — sẽ hỏi mật khẩu tài
khoản khi chạy, không gõ `/rp` trực tiếp để tránh lưu mật khẩu trong lịch sử
lệnh):

```powershell
schtasks /create /tn "go2rtc-backup" ^
  /tr "powershell.exe -NoProfile -ExecutionPolicy Bypass -File \"D:\Working\Counting\scripts\backup\Backup-Go2rtc.ps1\"" ^
  /sc daily /st 02:00 /ru "TENMAY\tenuser" /rl HIGHEST
```

## Restore (khôi phục)

Dùng `Restore-Go2rtc.ps1`, luôn restore vào 1 thư mục **trống/mới**, không
đè trực tiếp lên thư mục đang chạy — xem hướng dẫn in ra cuối script và
`docs/data-backup-plan.md` §5.

```powershell
.\Restore-Go2rtc.ps1 -Repo "D:\go2rtc-backups\restic-repo-local" -RestoreTo "D:\go2rtc-restore-test"
```

## Kiểm tra định kỳ

Nên test restore thật (không chỉ xem log backup "thành công") mỗi quý, trên
1 thư mục riêng — xem `docs/data-backup-plan.md` §5.
