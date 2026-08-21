import io
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from unittest import mock

from mothx_installer import cli


class BinaryPathTests(unittest.TestCase):
    def test_prefers_current_binary_name(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            package_dir = Path(temp_dir)
            binary = package_dir / "bin" / "mothx"
            binary.parent.mkdir()
            binary.touch()

            with mock.patch.object(cli, "__file__", str(package_dir / "cli.py")), mock.patch.object(
                cli.sys, "platform", "linux"
            ):
                self.assertEqual(cli._binary_path(), binary)

    def test_falls_back_to_legacy_binary_name(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            package_dir = Path(temp_dir)
            legacy_binary = package_dir / "bin" / "vibecoding.exe"
            legacy_binary.parent.mkdir()
            legacy_binary.touch()

            with mock.patch.object(cli, "__file__", str(package_dir / "cli.py")), mock.patch.object(
                cli.sys, "platform", "win32"
            ):
                self.assertEqual(cli._binary_path(), legacy_binary)


class MainTests(unittest.TestCase):
    def test_reports_missing_binary_with_reinstall_hint(self):
        missing = Path("/missing/mothx")
        stderr = io.StringIO()

        with mock.patch.object(cli, "_binary_path", return_value=missing), redirect_stderr(stderr):
            self.assertEqual(cli.main(), 1)

        self.assertIn("MothX binary is missing", stderr.getvalue())
        self.assertIn("pip install --force-reinstall mothx-installer", stderr.getvalue())

    def test_unix_exec_replaces_process_and_forwards_arguments(self):
        with tempfile.NamedTemporaryFile() as binary:
            args = ["mothx", "serve", "--port", "9090"]
            with mock.patch.object(cli, "_binary_path", return_value=Path(binary.name)), mock.patch.object(
                cli.sys, "platform", "linux"
            ), mock.patch.object(cli.sys, "argv", args), mock.patch.object(cli.os, "execv") as execv:
                self.assertEqual(cli.main(), 1)

            execv.assert_called_once_with(binary.name, [binary.name, *args[1:]])

    def test_unix_exec_failure_is_reported(self):
        with tempfile.NamedTemporaryFile() as binary:
            stderr = io.StringIO()
            failure = OSError("permission denied")
            with mock.patch.object(cli, "_binary_path", return_value=Path(binary.name)), mock.patch.object(
                cli.sys, "platform", "linux"
            ), mock.patch.object(cli.sys, "argv", ["mothx", "doctor"]), mock.patch.object(
                cli.os, "execv", side_effect=failure
            ), redirect_stderr(stderr):
                self.assertEqual(cli.main(), 1)

            self.assertIn("Failed to execute MothX binary: permission denied", stderr.getvalue())

    def test_windows_forwards_arguments_and_exit_code(self):
        with tempfile.NamedTemporaryFile(suffix=".exe") as binary:
            args = ["mothx.exe", "--version"]
            with mock.patch.object(cli, "_binary_path", return_value=Path(binary.name)), mock.patch.object(
                cli.sys, "platform", "win32"
            ), mock.patch.object(cli.sys, "argv", args), mock.patch.object(
                cli.subprocess, "call", return_value=23
            ) as call:
                self.assertEqual(cli.main(), 23)

            call.assert_called_once_with([binary.name, "--version"])


if __name__ == "__main__":
    unittest.main()
