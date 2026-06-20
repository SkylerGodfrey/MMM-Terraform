package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/chores"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/choresdb"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/config"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/rewards"
	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"
)

// ddlV1 mirrors the MMM-Chores module's v1 schema (store/db.js) — the module
// owns the schema in production; this seeds a realistic DB for handler tests.
const ddlV1 = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE completions (
	chore_id TEXT NOT NULL, user TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'open', completed_at TEXT,
	updated_at TEXT NOT NULL, PRIMARY KEY (chore_id, user));
CREATE TABLE pending_queue (
	id TEXT PRIMARY KEY, chore_id TEXT NOT NULL, user TEXT NOT NULL,
	tokens INTEGER NOT NULL DEFAULT 0, theme_payload TEXT, created_at TEXT NOT NULL);
CREATE TABLE events (
	id TEXT PRIMARY KEY, type TEXT NOT NULL, user TEXT,
	payload TEXT NOT NULL, created_at TEXT NOT NULL, reverted_at TEXT);
CREATE TABLE theme_kv (
	theme_id TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL,
	PRIMARY KEY (theme_id, key));
`

const testChoresYAML = `chores:
  - name: Set the table
    assignees:
      - Gavin
      - Savannah
    mode: shared
    completed: false
    tokens: 2
    id: cshared
  - name: Make bed
    assignees:
      - Gavin
    mode: independent
    completed: false
    tokens: 1
    id: cindep
`

const testRewardsYAML = `users:
  - name: Gavin
    tokens: 4
  - name: Savannah
    tokens: 1
rewards:
  - name: Ice cream
    cost: 3
    quantity: 2
    assignedTo:
      - Gavin
    id: rice
redemptions: []
`

// testServer builds a Server wired to temp chores.yaml / rewards.yaml / chores.db
// and returns it plus the open *sql.DB so tests can seed/inspect rows directly.
func testServer(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()

	choresYAML := filepath.Join(dir, "chores.yaml")
	rewardsYAML := filepath.Join(dir, "rewards.yaml")
	dbPath := filepath.Join(dir, "chores.db")
	if err := os.WriteFile(choresYAML, []byte(testChoresYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rewardsYAML, []byte(testRewardsYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ddlV1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	s := &Server{
		config:       &config.Config{},
		router:       gin.New(),
		choreStore:   chores.NewStore(choresYAML),
		choresDBPath: dbPath,
		rewardStore:  rewards.NewStore(rewardsYAML, filepath.Join(dir, "imgs")),
	}
	portalAPI := s.router.Group("/portal/api")
	portalAPI.GET("/pending", s.listPending)
	portalAPI.POST("/pending/:id/approve", s.approvePending)
	portalAPI.POST("/pending/:id/deny", s.denyPending)
	portalAPI.GET("/events", s.listEvents)
	portalAPI.POST("/events/:id/revert", s.revertEvent)
	return s, db
}

func do(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func tokensFor(t *testing.T, s *Server, name string) int {
	t.Helper()
	users, err := s.rewardStore.Users()
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

func completionStatus(t *testing.T, db *sql.DB, choreID, user string) string {
	t.Helper()
	var status string
	err := db.QueryRow("SELECT status FROM completions WHERE chore_id=? AND user=?", choreID, user).Scan(&status)
	if err == sql.ErrNoRows {
		return "open"
	}
	if err != nil {
		t.Fatal(err)
	}
	return status
}

// ---- HOM-139 approve / deny --------------------------------------------------

func TestApproveSharedGrantsTokensAndClosesAll(t *testing.T) {
	s, db := testServer(t)
	// Gavin checks off the shared chore; both assignees go pending.
	if _, err := db.Exec("INSERT INTO pending_queue VALUES('p1','cshared','Gavin',2,NULL,'2026-06-20T10:00:00.000Z')"); err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT INTO completions VALUES('cshared','Gavin','pending',?, ?)", "2026-06-20T10:00:00.000Z", "2026-06-20T10:00:00.000Z")
	db.Exec("INSERT INTO completions VALUES('cshared','Savannah','pending',?, ?)", "2026-06-20T10:00:00.000Z", "2026-06-20T10:00:00.000Z")

	w := do(t, s, "POST", "/portal/api/pending/p1/approve")
	if w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	// Shared: every assignee closed.
	if completionStatus(t, db, "cshared", "Gavin") != "done" {
		t.Error("Gavin should be done")
	}
	if completionStatus(t, db, "cshared", "Savannah") != "done" {
		t.Error("Savannah should be done (shared)")
	}
	// Tokens granted to the acting user only.
	if got := tokensFor(t, s, "Gavin"); got != 6 {
		t.Errorf("Gavin tokens: want 6 (4+2), got %d", got)
	}
	// Queue drained.
	var n int
	db.QueryRow("SELECT COUNT(*) FROM pending_queue").Scan(&n)
	if n != 0 {
		t.Errorf("queue should be empty, got %d", n)
	}
	// chore_completed event logged.
	var etype string
	if err := db.QueryRow("SELECT type FROM events WHERE user='Gavin'").Scan(&etype); err != nil || etype != "chore_completed" {
		t.Errorf("want chore_completed event, got %q err=%v", etype, err)
	}
}

func TestApproveIndependentClosesOnlyActingUser(t *testing.T) {
	s, db := testServer(t)
	db.Exec("INSERT INTO pending_queue VALUES('p2','cindep','Gavin',1,NULL,'2026-06-20T10:00:00.000Z')")
	db.Exec("INSERT INTO completions VALUES('cindep','Gavin','pending',?,?)", "x", "x")

	w := do(t, s, "POST", "/portal/api/pending/p2/approve")
	if w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	if completionStatus(t, db, "cindep", "Gavin") != "done" {
		t.Error("Gavin should be done")
	}
	if got := tokensFor(t, s, "Gavin"); got != 5 {
		t.Errorf("Gavin tokens want 5, got %d", got)
	}
}

func TestDenyReopensWithoutTokens(t *testing.T) {
	s, db := testServer(t)
	db.Exec("INSERT INTO pending_queue VALUES('p3','cshared','Gavin',2,'{\"pokemon\":\"pikachu\"}','2026-06-20T10:00:00.000Z')")
	db.Exec("INSERT INTO completions VALUES('cshared','Gavin','pending',?,?)", "x", "x")
	db.Exec("INSERT INTO completions VALUES('cshared','Savannah','pending',?,?)", "x", "x")

	w := do(t, s, "POST", "/portal/api/pending/p3/deny")
	if w.Code != http.StatusOK {
		t.Fatalf("deny: %d %s", w.Code, w.Body.String())
	}
	if completionStatus(t, db, "cshared", "Gavin") != "open" {
		t.Error("Gavin should be reopened")
	}
	if completionStatus(t, db, "cshared", "Savannah") != "open" {
		t.Error("Savannah should be reopened (shared)")
	}
	if got := tokensFor(t, s, "Gavin"); got != 4 {
		t.Errorf("deny must not grant tokens, got %d", got)
	}
	// theme_payload echoed for the mirror-side Pokémon release.
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["themePayload"] == nil {
		t.Error("deny should echo themePayload")
	}
}

func TestListPendingDecoratesChoreName(t *testing.T) {
	s, db := testServer(t)
	db.Exec("INSERT INTO pending_queue VALUES('p4','cindep','Gavin',1,NULL,'2026-06-20T10:00:00.000Z')")
	w := do(t, s, "GET", "/portal/api/pending")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	var body struct {
		Pending []struct {
			ChoreName string `json:"choreName"`
			Mode      string `json:"mode"`
		} `json:"pending"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Pending) != 1 || body.Pending[0].ChoreName != "Make bed" || body.Pending[0].Mode != "independent" {
		t.Errorf("decoration missing: %+v", body.Pending)
	}
}

// ---- HOM-140 revert ----------------------------------------------------------

func TestRevertChoreCompletedReopensAndDebits(t *testing.T) {
	s, db := testServer(t)
	db.Exec("INSERT INTO completions VALUES('cindep','Gavin','done',?,?)", "x", "x")
	db.Exec("INSERT INTO events VALUES('e1','chore_completed','Gavin','{\"choreId\":\"cindep\",\"tokens\":2}','2026-06-20T10:00:00.000Z',NULL)")

	w := do(t, s, "POST", "/portal/api/events/e1/revert")
	if w.Code != http.StatusOK {
		t.Fatalf("revert: %d %s", w.Code, w.Body.String())
	}
	if completionStatus(t, db, "cindep", "Gavin") != "open" {
		t.Error("completion should be reopened")
	}
	if got := tokensFor(t, s, "Gavin"); got != 2 {
		t.Errorf("tokens should be debited 4-2=2, got %d", got)
	}
	var reverted sql.NullString
	db.QueryRow("SELECT reverted_at FROM events WHERE id='e1'").Scan(&reverted)
	if !reverted.Valid {
		t.Error("event should be marked reverted")
	}
}

func TestRevertRewardRedeemedRefundsAndRestocks(t *testing.T) {
	s, db := testServer(t)
	db.Exec("INSERT INTO events VALUES('e2','reward_redeemed','Gavin','{\"reward\":\"Ice cream\",\"cost\":3,\"restock\":true}','2026-06-20T10:00:00.000Z',NULL)")

	w := do(t, s, "POST", "/portal/api/events/e2/revert")
	if w.Code != http.StatusOK {
		t.Fatalf("revert: %d %s", w.Code, w.Body.String())
	}
	if got := tokensFor(t, s, "Gavin"); got != 7 {
		t.Errorf("refund: want 4+3=7, got %d", got)
	}
	list, _ := s.rewardStore.List()
	for _, r := range list {
		if r["name"] == "Ice cream" {
			q, ok := r["quantity"].(int)
			if !ok {
				if f, fok := r["quantity"].(float64); fok {
					q, ok = int(f), true
				}
			}
			if !ok || q != 3 {
				t.Errorf("restock: want 3, got %v", r["quantity"])
			}
		}
	}
}

func TestRevertTokensEarnedDebits(t *testing.T) {
	s, db := testServer(t)
	db.Exec("INSERT INTO events VALUES('e3','tokens_earned','Gavin','{\"amount\":3}','2026-06-20T10:00:00.000Z',NULL)")
	w := do(t, s, "POST", "/portal/api/events/e3/revert")
	if w.Code != http.StatusOK {
		t.Fatalf("revert: %d %s", w.Code, w.Body.String())
	}
	if got := tokensFor(t, s, "Gavin"); got != 1 {
		t.Errorf("debit grant: want 4-3=1, got %d", got)
	}
}

func TestRevertPokemonCatchRejected(t *testing.T) {
	s, db := testServer(t)
	db.Exec("INSERT INTO events VALUES('e4','pokemon_catch','Gavin','{}','2026-06-20T10:00:00.000Z',NULL)")
	w := do(t, s, "POST", "/portal/api/events/e4/revert")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("pokemon_catch revert should be rejected, got %d %s", w.Code, w.Body.String())
	}
}

func TestRevertAlreadyRevertedIsConflict(t *testing.T) {
	s, db := testServer(t)
	db.Exec("INSERT INTO events VALUES('e5','tokens_earned','Gavin','{\"amount\":1}','2026-06-20T10:00:00.000Z','2026-06-20T11:00:00.000Z')")
	w := do(t, s, "POST", "/portal/api/events/e5/revert")
	if w.Code != http.StatusConflict {
		t.Fatalf("already-reverted should be 409, got %d", w.Code)
	}
}

// ---- pure revertActions port (mirrors store/revert.js) -----------------------

func TestRevertActionsPortMatchesSpec(t *testing.T) {
	tokensEarned := &choresdb.Event{Type: "tokens_earned", User: "Gavin", Payload: json.RawMessage(`{"amount":5}`)}
	if d, ok := revertActions(tokensEarned); !ok || d.tokenDelta != -5 || d.user != "Gavin" || d.reopen != nil || d.restockReward != "" {
		t.Errorf("tokens_earned: %+v ok=%v", d, ok)
	}

	redeemed := &choresdb.Event{Type: "reward_redeemed", User: "Gavin", Payload: json.RawMessage(`{"cost":3,"reward":"Ice cream","restock":true}`)}
	if d, ok := revertActions(redeemed); !ok || d.tokenDelta != 3 || d.restockReward != "Ice cream" || d.reopen != nil {
		t.Errorf("reward_redeemed: %+v ok=%v", d, ok)
	}

	// restock:false => no restock target.
	noRestock := &choresdb.Event{Type: "reward_redeemed", User: "Gavin", Payload: json.RawMessage(`{"cost":3,"reward":"Ice cream","restock":false}`)}
	if d, _ := revertActions(noRestock); d.restockReward != "" {
		t.Errorf("restock:false should yield empty restock, got %q", d.restockReward)
	}

	completed := &choresdb.Event{Type: "chore_completed", User: "Gavin", Payload: json.RawMessage(`{"choreId":"c1","tokens":2}`)}
	d, ok := revertActions(completed)
	if !ok || d.tokenDelta != -2 || d.reopen == nil || d.reopen.choreID != "c1" || d.reopen.user != "Gavin" {
		t.Errorf("chore_completed: %+v ok=%v", d, ok)
	}

	if _, ok := revertActions(&choresdb.Event{Type: "pokemon_catch"}); ok {
		t.Error("pokemon_catch must not be revertible")
	}
	if _, ok := revertActions(&choresdb.Event{Type: "mystery"}); ok {
		t.Error("unknown type must not be revertible")
	}
}
