# MothX Desktop

The desktop client is a thin Electron shell around `mothx serve`. It loads the
same embedded Svelte Web UI used by browser users; there is no second renderer
application and no ACP dependency.

The packaged app also contains the platform-native MothX CLI binary at
`vendor/mothx/bin/mothx` (or `mothx.exe` on Windows). The desktop shell starts
this bundled binary, never relies on a globally installed MothX, and validates
it with `mothx --version` before packaging.

The release packaging flow builds `cmd/mothx` from the checked-out source in an
`afterPack` hook for each Electron target platform and architecture, then places
that binary at `vendor/mothx/bin/`. It does not use the published npm runtime
package, so the desktop CLI and the desktop shell are built from the same commit.

`npm run vendor` remains available for local development and `npm start`, but is
not part of release packaging.


From the repository root:

```bash
make build
make desktop-vendor MOTHX_LOCAL=1
make desktop-build
cd desktop && npx electron .
```

Published runtime vendoring can use `MOTHX_VERSION=1.1.76 make desktop-vendor`.
For a local npm tarball use `MOTHX_TARBALL=/path/to/mothx-installer.tgz`.
The vendor step resolves the platform package nested under `mothx-installer`,
then normalizes its executable into `desktop/vendor/mothx/bin/`.

- `npm run dist:dev:mac` — 当前机器构建 macOS 开发包（dmg + zip）
- `npm run dist:dev:win` — 当前机器构建 Windows portable 单文件 exe
- `npm run dist:dev:linux` — 当前机器构建 Linux AppImage
- `npm run dist:mac` / `dist:win` / `dist:linux` — 对应发布构建，允许配置 publish

等价的仓库根目录快捷命令：

```bash
make desktop-dist-dev-mac
make desktop-dist-dev-win
make desktop-dist-dev-linux
```

这些命令和 mothxwork 的 `bun run electron:dist:dev:*` 作用一致，但使用 npm。`dist:dev:*` 强制 `--publish never`，不会创建或上传 GitHub Release。注意跨平台构建仍建议在对应 runner 上执行：Electron 原生运行时、macOS 签名/notarization、Windows portable 最终验证都由各平台 CI 完成。
