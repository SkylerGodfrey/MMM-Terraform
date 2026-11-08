package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestChoresStatusReturnsCompletionsActivationsAndDefaults(t *testing.T) {
	s, db := testServer(t)
	s.router.GET("/portal/api/chores-status", s.choresStatus)

	if _, err := db.Exec(`INSERT INTO completions (chore_id, user, status, completed_at, updated_at)
		VALUES ('cindep', 'Gavin', 'done', '2026-11-07T15:00:00.000Z', '2026-11-07T15:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('schedule_activations', '{"cindep|Gavin":"2026-11-07T06:00:00.000Z"}')`); err != nil {
		t.Fatal(err)
	}

	w := do(t, s, http.MethodGet, "/portal/api/chores-status")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Available         bool              `json:"available"`
		Completions       []map[string]any  `json:"completions"`
		Activations       map[string]string `json:"activations"`
		CalendarSchedules bool              `json:"calendarSchedules"`
		Schedule          struct {
			Defaults map[string]string `json:"defaults"`
		} `json:"schedule"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Available {
		t.Error("want available:true with a DB")
	}
	if len(body.Completions) != 1 || body.Completions[0]["status"] != "done" {
		t.Errorf("completions: %+v", body.Completions)
	}
	if body.Activations["cindep|Gavin"] != "2026-11-07T06:00:00.000Z" {
		t.Errorf("activations: %+v", body.Activations)
	}
	if !body.CalendarSchedules {
		t.Error("calendarSchedules should default true without module config")
	}
	if body.Schedule.Defaults["morning"] != "06:00" || body.Schedule.Defaults["nightly"] != "17:00" {
		t.Errorf("schedule defaults: %+v", body.Schedule.Defaults)
	}
}

func TestChoresStatusDegradesWithoutDB(t *testing.T) {
	s, _ := testServer(t)
	s.choresDBPath = s.choresDBPath + ".missing"
	s.router.GET("/portal/api/chores-status", s.choresStatus)

	w := do(t, s, http.MethodGet, "/portal/api/chores-status")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["available"] != false {
		t.Errorf("want available:false, got %v", body["available"])
	}
	if _, ok := body["schedule"]; !ok {
		t.Error("schedule settings should still be returned")
	}
}
