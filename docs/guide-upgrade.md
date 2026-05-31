# seek 升级 / Self-upgrade

seek 内置了**自动升级**能力——从 GitHub Releases 拉取新版本二进制，原地替换当前运行的可执行文件。

> seek can upgrade itself by fetching the latest release binary from GitHub and replacing the running executable in-place.

---

## 1. 快速开始 / Quick start

```bash
# 检查更新（只打印，不下载）
seek -upgrade-check

# 升级到最新版
seek -upgrade
```

---

## 2. 工作方式 / How it works

1. **检查版本**——比较当前版本与 GitHub 最新 release 版本
2. **下载**——从 `https://github.com/whyiyhw/seek/releases/download/<tag>/seek_<version>_<os>_<arch>.tar.gz` 下载对应平台版本
3. **验证**——SHA-256 校验下载的压缩包
4. **升级**——解压并原地替换当前二进制（Unix 用 POSIX rename，Windows 用 rename-then-replace）
5. **完成**——下一次启动就是新版本

---

## 3. 验证后不替换 / Dry run

```bash
# 下载并校验 checksum，但不替换二进制
seek -upgrade-dry-run
```

---

## 4. 安全边界 / Safety

- **dev 版本保护**——如果当前运行的是 dev 构建（`IsDev(Current) == true`），默认**拒绝**升级——dev 构建通常比 release 更新，覆盖掉会丢失本地修改。设置 `-upgrade-force` 可强行升级。
- **回滚**——升级前 seek 不会备份旧二进制（回滚请从 GitHub Releases 手动下载）。

---

## 5. CLI 参考 / CLI reference

```text
seek -upgrade-check            # 检查更新状态（只打印，不下载）
seek -upgrade                  # 下载并应用更新
seek -upgrade-dry-run          # 下载 + 校验 checksum，但不替换二进制
seek -upgrade-force            # 允许从 dev 构建升级（默认不升）
```

> **注意**：以上均为 CLI 标志（flag）形式，不是子命令。TUI 内可使用 `/upgrade`、`/upgrade --force`、`/upgrade --dry-run`。
