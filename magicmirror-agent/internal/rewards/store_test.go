package rewards

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleRewardsYAML = `users:
  - name: Gavin
    tokens: 5
  - name: Savannah
    tokens: 0
rewards:
  - name: Movie night
    cost: 10
    quantity: 2
    assignedTo:
      - Gavin
    id: rabc123
redemptions: []
`

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rewards.yaml")
	if err := os.WriteFile(path, []byte(sampleRewardsYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewStore(path, filepath.Join(dir, "rewards-images"))
}

func balanceOf(t *testing.T, s *Store, name string) int {
	t.Helper()
	users, err := s.Users()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u["name"] == name {
			switch v := u["tokens"].(type) {
			case int:
				return v
			case float64:
				return int(v)
			}
		}
	}
	t.Fatalf("user %q not found", name)
	return 0
}

func TestAdjustTokensCreditAndClamp(t *testing.T) {
	s := newTestStore(t)

	// Grant (approve path).
	bal, err := s.AdjustTokens("Gavin", 3)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 8 || balanceOf(t, s, "Gavin") != 8 {
		t.Fatalf("credit: want 8, got %d", bal)
	}

	// Debit clamps at zero, matching node_helper adjustTokens.
	bal, err = s.AdjustTokens("Savannah", -5)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 0 || balanceOf(t, s, "Savannah") != 0 {
		t.Fatalf("debit should clamp at 0, got %d", bal)
	}
}

func TestAdjustTokensCreatesUnknownUser(t *testing.T) {
	s := newTestStore(t)
	bal, err := s.AdjustTokens("NewKid", 2)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 2 || balanceOf(t, s, "NewKid") != 2 {
		t.Fatalf("unknown user should be created with clamped balance, got %d", bal)
	}
}

func TestAdjustTokensAndRestock(t *testing.T) {
	s := newTestStore(t)

	// Revert of a redemption: refund cost AND +1 the reward quantity.
	bal, err := s.AdjustTokensAndRestock("Gavin", 10, "Movie night")
	if err != nil {
		t.Fatal(err)
	}
	if bal != 15 {
		t.Fatalf("refund: want 15, got %d", bal)
	}

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var movie map[string]any
	for _, r := range list {
		if r["name"] == "Movie night" {
			movie = r
		}
	}
	if movie == nil {
		t.Fatal("reward missing")
	}
	q, ok := movie["quantity"].(int)
	if !ok {
		if f, fok := movie["quantity"].(float64); fok {
			q = int(f)
			ok = true
		}
	}
	if !ok || q != 3 {
		t.Fatalf("quantity should be restocked to 3, got %v", movie["quantity"])
	}
}

func TestAdjustTokensAndRestockUnknownRewardOnlyAdjusts(t *testing.T) {
	s := newTestStore(t)
	// restock target absent: token delta still applies, no panic.
	bal, err := s.AdjustTokensAndRestock("Gavin", 4, "Nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if bal != 9 {
		t.Fatalf("want 9, got %d", bal)
	}
}
