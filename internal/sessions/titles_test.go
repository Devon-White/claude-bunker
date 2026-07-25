package sessions

import "testing"

func TestSessionTitleStoreRoundTrip(t *testing.T) {
	if err := SetSessionTitle("cid", "sid-1", "my title"); err != nil {
		t.Fatalf("SetSessionTitle: %v", err)
	}
	if got := GetSessionTitle("cid", "sid-1"); got != "my title" {
		t.Errorf("GetSessionTitle = %q, want %q", got, "my title")
	}
	if got := GetSessionTitle("cid", "absent"); got != "" {
		t.Errorf("absent title = %q, want empty", got)
	}
}
