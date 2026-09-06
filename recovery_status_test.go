package main

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryStatusRequiresTokenAndShowsIdentificationTask(t *testing.T) {
	t.Setenv("BOT_SERVICE_TOKEN", "test-token")
	t.Setenv("RECOVERY_STATE_PATH", filepath.Join(t.TempDir(), "recovery.json"))
	t.Setenv("LINK_FEEDBACK_PATH", filepath.Join(t.TempDir(), "feedback.json"))
	s, e := loadRecoveryMonitor()
	if e != nil {
		t.Fatal(e)
	}
	s.Unknown[99] = "Example Movie"
	if e := saveRecoveryMonitor(s); e != nil {
		t.Fatal(e)
	}
	r := gin.New()
	r.GET("/recovery/status", recoveryStatus)
	for _, auth := range []string{"", "Bearer wrong", "Bearer test-token"} {
		req := httptest.NewRequest("GET", "/recovery/status", nil)
		req.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if auth == "Bearer test-token" {
			if w.Code != 200 || !strings.Contains(w.Body.String(), "Example Movie") {
				t.Fatalf("missing task: %d %s", w.Code, w.Body)
			}
		} else if w.Code != 401 || strings.Contains(w.Body.String(), "Example Movie") {
			t.Fatal("private task exposed")
		}
	}
}
