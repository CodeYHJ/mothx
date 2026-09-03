package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBrowseFilesystemRootsSingleRootOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix root layout not used on windows")
	}
	roots := browseFilesystemRoots("/home/user/projects")
	if len(roots) != 1 || roots[0] != "/" {
		t.Fatalf("roots = %#v, want single filesystem root", roots)
	}
}

func TestIsWindowsDriveRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows volume semantics only apply on windows")
	}
	if !isWindowsDriveRoot(`C:\`) {
		t.Fatal(`C:\ should be a volume root`)
	}
	if isWindowsDriveRoot(`C:\Users`) {
		t.Fatal(`C:\Users should not be a volume root`)
	}
	if isWindowsDriveRoot(`\\server\share\`) {
		t.Fatal(`a UNC share root should not lead to the virtual drive list`)
	}
}

func TestWindowsDrivePathSpellings(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows path semantics only apply on windows")
	}
	if got := filepath.Clean(`D://projects/mothx`); got != `D:\projects\mothx` {
		t.Fatalf("cleaned drive path = %q, want %q", got, `D:\projects\mothx`)
	}
	if !isWindowsDriveRoot(filepath.Clean(`c:/`)) {
		t.Fatal(`c:/ should normalize to a Windows drive root`)
	}
}

func TestBrowseFilesystemRootsIncludesCurrentUNCShare(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("UNC path semantics only apply on windows")
	}
	const uncRoot = `\\server\share\`
	roots := browseFilesystemRoots(uncRoot + `project`)
	if !pathWithinAnyRoot(uncRoot, roots) {
		t.Fatalf("roots = %#v, want current UNC share %q", roots, uncRoot)
	}
}

func TestHandleBrowseWindowsDriveList(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive list virtual root only exists on windows")
	}
	rt := &channelRuntime{cfg: DefaultConfig()}
	req := httptest.NewRequest(http.MethodGet, "/api/browse?path="+url.QueryEscape(`\`), nil)
	w := httptest.NewRecorder()
	rt.handleBrowse(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		Path       string `json:"path"`
		Parent     string `json:"parent"`
		Selectable bool   `json:"selectable"`
		Entries    []struct {
			Name  string `json:"name"`
			Path  string `json:"path"`
			IsDir bool   `json:"isDir"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Path != `\` || got.Parent != `\` {
		t.Fatalf("path/parent = %q/%q, want virtual drive list root", got.Path, got.Parent)
	}
	if got.Selectable {
		t.Fatal("virtual drive list must not be selectable as a work directory")
	}
	if len(got.Entries) == 0 {
		t.Fatal("expected at least one drive entry")
	}
	for _, entry := range got.Entries {
		if !isWindowsDriveRoot(entry.Path) {
			t.Fatalf("entry %q is not a volume root", entry.Path)
		}
	}
}
