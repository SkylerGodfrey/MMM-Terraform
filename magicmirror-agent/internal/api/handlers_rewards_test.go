package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/mmconfig"
)

func TestFilterDisabledUsers(t *testing.T) {
	users := []map[string]any{
		{"name": "Dad", "tokens": 0},
		{"name": "Gavin", "tokens": 4},
		{"name": "Mom", "tokens": 0},
		{"name": "Savannah", "tokens": 1},
	}

	t.Run("no disabled set returns input unchanged", func(t *testing.T) {
		got := filterDisabledUsers(users, nil)
		if len(got) != len(users) {
			t.Fatalf("expected %d users, got %d", len(users), len(got))
		}
	})

	t.Run("drops disabled names, preserves order", func(t *testing.T) {
		got := filterDisabledUsers(users, []string{"Dad", "Mom"})
		want := []string{"Gavin", "Savannah"}
		if len(got) != len(want) {
			t.Fatalf("expected %d users, got %d", len(want), len(got))
		}
		for i, name := range want {
			if got[i]["name"] != name {
				t.Errorf("at %d: expected %q, got %q", i, name, got[i]["name"])
			}
		}
	})
}

// configWithDisabled writes a minimal config.js declaring an MMM-Chores module
// with the given rewardsDisabledUsers, and points a fresh mmManager at it.
func configWithDisabled(t *testing.T, disabled []string) *mmconfig.Manager {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.js")
	list, err := json.Marshal(disabled)
	if err != nil {
		t.Fatal(err)
	}
	js := `var config = { modules: [
		{ module: "clock", position: "top_left" },
		{ module: "MMM-Chores", position: "fullscreen_below", config: { rewardsDisabledUsers: ` + string(list) + ` } }
	] };`
	if err := os.WriteFile(path, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	return mmconfig.NewManager(path, "")
}

func TestDisabledRewardUsers(t *testing.T) {
	t.Run("nil manager means no one disabled", func(t *testing.T) {
		s := &Server{}
		if got := s.disabledRewardUsers(); got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("reads the MMM-Chores module config", func(t *testing.T) {
		s := &Server{mmManager: configWithDisabled(t, []string{"Dad", "Mom"})}
		got := s.disabledRewardUsers()
		if len(got) != 2 || got[0] != "Dad" || got[1] != "Mom" {
			t.Fatalf("expected [Dad Mom], got %v", got)
		}
	})

	t.Run("missing option yields no disabled users", func(t *testing.T) {
		s := &Server{mmManager: configWithDisabled(t, nil)}
		// json.Marshal(nil) -> "null", so the option is present but null —
		// which decodes to a non-[]any value and must be treated as empty.
		if got := s.disabledRewardUsers(); len(got) != 0 {
			t.Fatalf("expected no disabled users, got %v", got)
		}
	})
}

func TestListRewardsFiltersDisabled(t *testing.T) {
	s, _ := testServer(t)
	s.mmManager = configWithDisabled(t, []string{"Savannah"})
	s.router.GET("/portal/api/rewards", s.listRewards)

	w := do(t, s, "GET", "/portal/api/rewards")
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Users         []map[string]any `json:"users"`
		DisabledUsers []string         `json:"disabledUsers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.DisabledUsers) != 1 || resp.DisabledUsers[0] != "Savannah" {
		t.Fatalf("expected disabledUsers [Savannah], got %v", resp.DisabledUsers)
	}
	for _, u := range resp.Users {
		if u["name"] == "Savannah" {
			t.Fatalf("Savannah should have been filtered out of balances: %v", resp.Users)
		}
	}
}
