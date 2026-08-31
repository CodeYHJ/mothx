# MothX Desktop

The desktop client is a thin Electron shell around `mothx serve`. It loads the
same embedded Svelte Web UI used by browser users; there is no second renderer
application and no ACP dependency.

The packaged app also contains the platform-native MothX CLI binary at
`vendor/mothx/bin/mothx` (or `mothx.exe` on Windows). The desktop shell starts
this bundled binary, never relies on a globally installed MothX, and validates
it with `mothx --version` before packaging.

The release packaging flow builds the Serve Web UI and `cmd/mothx` from the checked-out source before Electron packaging, then places that binary at `vendor/mothx/bin/`. It does not use the published npm runtime package, so the desktop CLI, Web UI, and desktop shell are built from the same commit.

Desktop `package.json` keeps a placeholder version. `npm run version:set` and packaging scripts resolve the real version from `MOTHX_VERSION` if set, otherwise the current git tag (`git describe --tags --abbrev=0`).

`npm run vendor` is retained as a compatibility alias for the source build and no longer installs `mothx-installer`.


From the repository root:

```bash
make desktop-vendor
make desktop-build
cd desktop && npx electron .
```

`make desktop-vendor` runs the same source build used by the npm scripts and CI:
it builds `ui/dist` first, then compiles the current checkout's Go runtime into
`desktop/vendor/mothx/bin/`. No published `mothx-installer` package is installed.

- `npm run dist:dev:mac` — 当前机器构建 macOS 开发包（`MothX-Desktop-macos-{arch}.dmg` + `.zip`）
- `npm run dist:dev:win` — 当前机器构建 Windows 开发包（`MothX-Desktop-windows-x64.exe` portable + `.zip`）
- `npm run dist:dev:linux` — 当前机器构建 Linux 开发包（`MothX-Desktop-linux-amd64.AppImage` + `.deb` + `.tar.gz`）
- `npm run dist:mac` / `dist:win` / `dist:linux` — 对应发布构建，允许配置 publish

等价的仓库根目录快捷命令：

```bash
make desktop-dist-dev-mac
make desktop-dist-dev-win
make desktop-dist-dev-linux
```

这些命令和 mothxwork 的 `bun run electron:dist:dev:*` 作用一致，但使用 npm。`dist:dev:*` 强制 `--publish never`，不会创建或上传 GitHub Release。注意跨平台构建仍建议在对应 runner 上执行：Electron 原生运行时、macOS 签名/notarization、Windows portable 最终验证都由各平台 CI 完成。

macOS 构建为单架构（`--arch $(node -p process.arch)`），即每次调用只构建当前机器的架构（arm64 或 x64），与源码编译的 Go CLI 架构保持一致。`after-pack.cjs` 会校验打包进应用的 CLI 二进制架构是否与应用架构匹配，不匹配时直接报错，避免静默打入错误架构的运行时。如需同时产出两个架构，请在对应架构的 runner 上分别执行。
