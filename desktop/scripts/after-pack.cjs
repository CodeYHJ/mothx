const fs = require('node:fs');
const path = require('node:path');

// Read the CPU architecture of a bare Go binary from its file header so we
// can refuse to bundle a CLI whose arch does not match the Electron app arch.
// Returns 'amd64', 'arm64', or null if the binary is not a recognized
// ELF / Mach-O / PE image.
function binaryArch(file) {
  const fd = fs.openSync(file, 'r');
  try {
    const buf = Buffer.alloc(64);
    const n = fs.readSync(fd, buf, 0, buf.length, 0);
    if (n < 24) return null;
    if (buf[0] === 0x7f && buf[1] === 0x45 && buf[2] === 0x4c && buf[3] === 0x46) {
      // ELF: e_machine (2 bytes) at offset 18.
      const machine = buf.readUInt16LE(18);
      if (machine === 62) return 'amd64'; // EM_X86_64
      if (machine === 183) return 'arm64'; // EM_AARCH64
      return null;
    }
    if (buf[0] === 0xfe && buf[1] === 0xed && buf[2] === 0xfa && buf[3] === 0xcf) {
      // Mach-O 64-bit: cputype (4 bytes) at offset 4.
      const cputype = buf.readUInt32LE(4);
      if (cputype === 0x01000007) return 'amd64'; // CPU_TYPE_X86_64
      if (cputype === 0x0100000c) return 'arm64'; // CPU_TYPE_ARM64
      return null;
    }
    if (buf[0] === 0x4d && buf[1] === 0x5a) {
      // PE: e_lfanew at 0x3c, then PE signature, then COFF Machine (2 bytes).
      const peOffset = buf.readUInt32LE(0x3c);
      const pe = Buffer.alloc(8);
      const pn = fs.readSync(fd, pe, 0, pe.length, peOffset);
      if (pn >= 6 && pe[0] === 0x50 && pe[1] === 0x45 && pe[2] === 0 && pe[3] === 0) {
        const machine = pe.readUInt16LE(4);
        if (machine === 0x8664) return 'amd64'; // IMAGE_FILE_MACHINE_AMD64
        if (machine === 0xaa64) return 'arm64'; // IMAGE_FILE_MACHINE_ARM64
      }
      return null;
    }
    return null;
  } finally {
    fs.closeSync(fd);
  }
}

function targetFor(packContext) {
  const platform = packContext.electronPlatformName;
  const arch = packContext.arch === 3 ? 'arm64' : packContext.arch === 1 ? 'amd64' : undefined;
  if (!arch) throw new Error(`Unsupported desktop architecture for bundled CLI: ${packContext.arch}`);
  const goos = platform === 'win32' ? 'windows' : platform;
  return { goos, goarch: arch, binaryName: goos === 'windows' ? 'mothx.exe' : 'mothx' };
}

module.exports = async function afterPack(packContext) {
  const { goos, goarch, binaryName } = targetFor(packContext);
  const source = path.join(__dirname, '..', 'vendor', 'mothx', 'bin', binaryName);
  const binary = path.join(packContext.appOutDir, 'vendor', 'mothx', 'bin', binaryName);
  if (!fs.existsSync(source)) {
    throw new Error(`Source-built MothX CLI not found at ${source}. Run npm run build:runtime before packaging.`);
  }
  fs.mkdirSync(path.dirname(binary), { recursive: true });
  fs.copyFileSync(source, binary);
  if (goos !== 'windows') fs.chmodSync(binary, 0o755);
  const detected = binaryArch(binary);
  if (detected && detected !== goarch) {
    throw new Error(
      `Bundled MothX CLI arch mismatch: binary is ${detected} but the app is ${goarch}. ` +
      `Rebuild the runtime with the matching --arch (npm run build:runtime -- --platform ${goos} --arch ${goarch}).`
    );
  }
  console.log(`Bundled source-built MothX CLI at ${binary} (${detected || goarch})`);
};
