package serve

import (
	"context"
	"errors"
	"runtime"
	"testing"
)

func TestOpenUnixDirectoryPickerUnavailableWithoutDisplay(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skipf("unix picker display check not used on %s", runtime.GOOS)
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	_, err := openUnixDirectoryPicker(context.Background(), t.TempDir())
	if !errors.Is(err, errNativeDirectoryPickerUnavailable) {
		t.Fatalf("err = %v, want errNativeDirectoryPickerUnavailable", err)
	}
}
