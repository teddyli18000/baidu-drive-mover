# v0.9 真实迁移验收手册

本手册是受控的真实 Baidu/Google 验收阶梯，不是普通 CI。任何一步失败都应暂停任务、保留 `temp/`，记录非敏感证据并先调查；不得用“看起来成功”替代持久化事实。

## 0. 前置条件与 package identity

1. 仅使用 Windows x64 ZIP；先按 `SHA256SUMS.txt` 核对 ZIP SHA-256，再解压到新的、可写的应用目录。
2. 包内只应有 `BaiduDriveMover.exe`；运行 `-version`，核对版本、完整 commit 和构建身份与验收记录一致。
3. 在首次真实任务前运行 `BaiduDriveMover.exe -check`；确认应用目录首次只新增 `temp/`，没有 AppData、注册表、服务或计划任务写入。
4. 保留应用目录原位运行。`temp/` 含 SQLite、cookies、OAuth 配置、私有清单、缓存和日志，不得同步、上传或作为支持包发送。
5. 选择静态、可重复的小型测试分享；确认本机 Chrome、百度/Google 账号、网络和磁盘空间可用。

## 1. 只读扫描入口

1. 启动 `BaiduDriveMover.exe -scan-only`，不得同时传入 `-check`、`-list`、`-resume` 或 `-new`。
2. 它按默认规则处理最新未完成任务；没有可恢复任务时再粘贴分享链接。扫描只允许读取分享并持久化清单/会话，不能暂存、下载、访问 Drive 或清理。
3. 用户在独立 Chrome 窗口完成百度登录、验证码/二次验证和必要的提取码输入。
4. 成功后保存非敏感证据：task ID、目录数、文件数、总字节数、时间和退出码。不要保存分享 URL、提取码或 cookie。
5. 复核清单范围、根文件、嵌套目录、空目录和数量；逻辑路径必须从分享根开始，不能包含百度源账号的绝对目录前缀。确认无误后，才明确运行 `BaiduDriveMover.exe -resume <task-id>` 进入迁移。

## 2. 用户必须亲自完成的敏感步骤

- 在隔离 Chrome profile 中登录正确的百度账号；只在交互窗口输入验证码和提取码。
- 在 Google OAuth 浏览器流程中确认正确的 Google 账号，并同意明确的 `drive.file` 权限；不要授权更宽范围。
- 迁移期间不要移动、重命名或编辑 `BaiduDriveMover-<task-id>` Drive 根目录。
- 明确接受：Drive 验证后程序会清理工具自有本地 cache 和百度暂存批次，但不会删除 Drive 目标或清空百度回收站。
- 保持应用目录和 `temp/` 不变；不要手工删除远端暂存来“修复”任务，也不要运行第二个同目录进程。

## 3. 强制阶梯

1. **tiny**：先完成只读扫描、一个小暂存批次、下载、Drive 上传/核验、一次受控清理。
2. **medium**：tiny 全部证据通过后，验证多批次、目录树、空目录和至少一次重启。
3. **large**：medium 通过后，才运行多小时、大目录、多次中断任务。

不得从 mock 直接跳到 destructive 或 large live test；每一级均应使用新的测试任务或明确可恢复的任务 ID。

## 4. Ctrl+C / restart cut points

在 tiny、medium、large 至少覆盖以下边界；每次只用 Ctrl+C，等待进程退出，再用 `-resume <task-id>` 重启：

- 扫描未完成、扫描完成但尚未迁移；
- 百度 transfer 前后及批次 reconcile 后；
- 下载 `.part` 正在增长时、下载完成写入 SQLite 前后；
- `LOCAL_READY` 后、Drive `copyto` 前后、远端提交但本地状态尚未更新时；
- Drive 上传后、独立 ID/size/MD5 核验前后；
- cleanup 授权提交后、任一本地文件删除后、百度批次删除后但证据尚未持久化时；
- task root 清理前后。

每次重启都要确认状态从持久化事实恢复，不重复上传、不回退下游进度、不扩大删除范围；任何不确定都保持暂停/阻塞。

## 5. Drive 证据矩阵

每个文件必须同时满足：

- 目标位于正确的 `BaiduDriveMover-<task-id>` 逻辑树，空目录也存在；
- 远端对象是唯一的普通文件，Drive ID 非空且持久化；
- 字节大小与已验证本地 cache 完全相等；
- Drive MD5 与本地计算的 MD5 完全相等；
- 只有完成独立核验后才进入 `DRIVE_VERIFIED`，不能用 rclone 退出码代替。

若源 manifest 的 MD5 为空，下载阶段只能证明大小；Drive MD5 只证明“本地缓存字节→Drive”一致，不能证明“源文件→本地缓存”具备密码学等价性。此情况必须在证据中标注，并用可信源哈希或人工抽样比对补强；不得宣称完整内容证明。

## 6. Cache 与失败门槛

- 默认本地 cache 水位为 30 GiB；任何单文件大于 30 GiB 会被明确阻塞，不得绕过。
- 预留足够磁盘空间，保留 `.part` 直到恢复或明确安全清理；水位、磁盘错误、配额、认证过期均应暂停/阻塞而非扩大范围。

## 7. 精确 cleanup 验收

1. 只有批次内每个文件都已持久化 Drive ID 且 `DRIVE_VERIFIED`，才可授权 cleanup。
2. 本地只允许删除已登记、位于 `temp/cache/<task-id>/` 的精确 opaque 文件。
3. 百度只允许删除已登记的 `/BaiduDriveMover/<task-id>/<batch-id>`；删除前必须 fresh-listing 只包含该批次预期对象。
4. task root 仅在所有批次清理完成且 fresh listing 为空时删除；全局 `/BaiduDriveMover` 永不删除。
5. Drive 目标、无关本地/百度对象和百度回收站永远不是自动清理目标。
6. 最后一个非完成任务进入 durable `COMPLETED` 后，确认进程退出时整个 `temp/` 被删除；其中包括专用 Chrome profile、cookies、OAuth 配置、rclone、缓存、日志和任务数据库。若仍有其他非完成任务，必须保留共享 runtime 以便恢复。
7. 通过 Ctrl+C、崩溃、失败、阻塞或 `-scan-only` 退出时不得删除恢复所需的 `temp/`。

## 8. 证据与保密

可记录：包哈希、版本/commit、退出码、task ID、计数、字节数、阶段、重启时间、非敏感错误类别和最终状态。禁止记录或截图：BDUSS/STOKEN、Cookie、OAuth access/refresh token、`rclone.conf`、提取码、私有分享 URL、签名下载 URL、浏览器 profile、`state.db`、完整 `temp/` 或下载内容。

验收结论必须包含 tiny→medium→large 的每级结果、所有 cut point 是否恢复、Drive ID/size/MD5 证据、源 MD5 是否缺失、cache 上限和 cleanup 路径核对；缺一项即不通过。
