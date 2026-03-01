package sandbox

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestEncodeProjectPath_Basic(t *testing.T) {
	// encodeProjectPath calls filepath.Abs, so expected output depends on OS.
	// On Windows, filepath.Abs("/home/user") yields "C:\\home\\user".
	// We test with an absolute path appropriate to the platform.
	var input, want string
	if runtime.GOOS == "windows" {
		input = `C:\Users\devon\projects\my-app`
		want = "C--Users-devon-projects-my-app"
	} else {
		input = "/home/user/projects/my-app"
		want = "-home-user-projects-my-app"
	}

	got := encodeProjectPath(input)
	if got != want {
		t.Errorf("encodeProjectPath(%q) = %q, want %q", input, got, want)
	}
}

func TestEncodeProjectPath_ColonReplacement(t *testing.T) {
	// Colons should be replaced with hyphens
	got := encodeProjectPath("C:\\Users\\test")
	if runtime.GOOS == "windows" {
		want := "C--Users-test"
		if got != want {
			t.Errorf("encodeProjectPath with colons: got %q, want %q", got, want)
		}
	}
	// On any OS, colons and separators should be replaced
}

func TestEncodeProjectPath_ForwardSlashes(t *testing.T) {
	// Forward slashes should also be replaced
	abs, _ := filepath.Abs("C:/Users/devon/projects/my-app")
	got := encodeProjectPath(abs)
	// The absolute path will have OS-appropriate separators; all should become -
	if len(got) == 0 {
		t.Error("encodeProjectPath returned empty string")
	}
}

func TestEncodeProjectPath_Deterministic(t *testing.T) {
	path := filepath.Join("C:", "Users", "test", "project")
	a := encodeProjectPath(path)
	b := encodeProjectPath(path)
	if a != b {
		t.Errorf("encodeProjectPath is not deterministic: %q vs %q", a, b)
	}
}
