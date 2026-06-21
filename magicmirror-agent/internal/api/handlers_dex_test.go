package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/config"
	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"
)

const dexDataset = `[
  {"id":25,"name":"Pikachu","forms":[
    {"key":"25","category":"normal","shiny":false,"label":"Pikachu"},
    {"key":"25-shiny","category":"normal","shiny":true,"label":"Pikachu (Shiny)"}
  ]},
  {"id":6,"name":"Charizard","forms":[
    {"key":"6-mega-x","category":"mega","shiny":false,"label":"Mega Charizard X"}
  ]}
]`

func dexTestServer(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chores.db")
	pokedexPath := filepath.Join(dir, "pokedex.json")
	if err := os.WriteFile(pokedexPath, []byte(dexDataset), 0o644); err != nil {
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
		choresDBPath: dbPath,
		pokedexPath:  pokedexPath,
	}
	api := s.router.Group("/portal/api")
	api.GET("/dex/:user", s.listDex)
	api.POST("/dex/:user/grant", s.grantDex)
	api.POST("/dex/:user/remove", s.removeDex)
	api.GET("/events", s.listEvents)
	api.POST("/events/:id/revert", s.revertEvent)
	return s, db
}

// caughtKeys reads a user's caught formKeys directly from theme_kv.
func caughtKeys(t *testing.T, db *sql.DB, user string) []string {
	t.Helper()
	var value string
	err := db.QueryRow("SELECT value FROM theme_kv WHERE theme_id='pokemon' AND key='state'").Scan(&value)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Users map[string]struct {
			Caught []struct {
				FormKey string `json:"formKey"`
				Count   int    `json:"count"`
			} `json:"caught"`
		} `json:"users"`
	}
	if err := json.Unmarshal([]byte(value), &doc); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range doc.Users[user].Caught {
		out = append(out, e.FormKey)
	}
	return out
}

func latestEventID(t *testing.T, s *Server, typ string) string {
	t.Helper()
	w := do(t, s, http.MethodGet, "/portal/api/events?type="+typ)
	var body struct {
		Events []struct {
			ID string `json:"id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) == 0 {
		t.Fatalf("no %s event logged", typ)
	}
	return body.Events[0].ID
}

// dexEventByCreated returns the id of the event of the given type whose payload
// `created` flag matches want — used to disambiguate grants that share a
// millisecond timestamp.
func dexEventByCreated(t *testing.T, s *Server, typ string, want bool) string {
	t.Helper()
	w := do(t, s, http.MethodGet, "/portal/api/events?type="+typ)
	var body struct {
		Events []struct {
			ID      string `json:"id"`
			Payload struct {
				Created bool `json:"created"`
			} `json:"payload"`
		} `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, e := range body.Events {
		if e.Payload.Created == want {
			return e.ID
		}
	}
	t.Fatalf("no %s event with created=%v", typ, want)
	return ""
}

func TestDexGrantListRemove(t *testing.T) {
	s, db := dexTestServer(t)

	// Grant rejects an unknown formKey.
	w := doJSON(t, s, http.MethodPost, "/portal/api/dex/Gavin/grant", `{"formKey":"9999"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown formKey should be 400, got %d", w.Code)
	}

	// Grant a real form.
	w = doJSON(t, s, http.MethodPost, "/portal/api/dex/Gavin/grant", `{"formKey":"25"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("grant: %d %s", w.Code, w.Body.String())
	}
	if keys := caughtKeys(t, db, "Gavin"); len(keys) != 1 || keys[0] != "25" {
		t.Fatalf("Gavin should have caught 25, got %v", keys)
	}

	// List shows it.
	w = do(t, s, http.MethodGet, "/portal/api/dex/Gavin")
	var listed struct {
		Caught []struct {
			FormKey  string `json:"formKey"`
			Species  int    `json:"species"`
			Category string `json:"category"`
		} `json:"caught"`
		Available bool `json:"available"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if !listed.Available || len(listed.Caught) != 1 || listed.Caught[0].Species != 25 {
		t.Fatalf("list mismatch: %s", w.Body.String())
	}

	// Remove it.
	w = doJSON(t, s, http.MethodPost, "/portal/api/dex/Gavin/remove", `{"formKey":"25"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if keys := caughtKeys(t, db, "Gavin"); len(keys) != 0 {
		t.Fatalf("Gavin's dex should be empty, got %v", keys)
	}

	// Removing something absent is a 404.
	w = doJSON(t, s, http.MethodPost, "/portal/api/dex/Gavin/remove", `{"formKey":"25"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("removing absent entry should be 404, got %d", w.Code)
	}
}

func TestDexGrantRevertRemovesEntry(t *testing.T) {
	s, db := dexTestServer(t)

	doJSON(t, s, http.MethodPost, "/portal/api/dex/Savannah/grant", `{"formKey":"6-mega-x"}`)
	if keys := caughtKeys(t, db, "Savannah"); len(keys) != 1 {
		t.Fatalf("setup: expected 1 catch, got %v", keys)
	}

	id := latestEventID(t, s, "pokemon_admin_grant")
	w := do(t, s, http.MethodPost, "/portal/api/events/"+id+"/revert")
	if w.Code != http.StatusOK {
		t.Fatalf("revert grant: %d %s", w.Code, w.Body.String())
	}
	if keys := caughtKeys(t, db, "Savannah"); len(keys) != 0 {
		t.Fatalf("reverting a grant should remove the entry, got %v", keys)
	}

	// Re-reverting is a conflict (already reverted).
	w = do(t, s, http.MethodPost, "/portal/api/events/"+id+"/revert")
	if w.Code != http.StatusConflict {
		t.Fatalf("second revert should be 409, got %d", w.Code)
	}
}

func TestDexRemoveRevertRestoresEntry(t *testing.T) {
	s, db := dexTestServer(t)

	doJSON(t, s, http.MethodPost, "/portal/api/dex/Gavin/grant", `{"formKey":"25-shiny"}`)
	doJSON(t, s, http.MethodPost, "/portal/api/dex/Gavin/remove", `{"formKey":"25-shiny"}`)
	if keys := caughtKeys(t, db, "Gavin"); len(keys) != 0 {
		t.Fatalf("setup: dex should be empty after remove, got %v", keys)
	}

	id := latestEventID(t, s, "pokemon_admin_remove")
	w := do(t, s, http.MethodPost, "/portal/api/events/"+id+"/revert")
	if w.Code != http.StatusOK {
		t.Fatalf("revert remove: %d %s", w.Code, w.Body.String())
	}
	keys := caughtKeys(t, db, "Gavin")
	if len(keys) != 1 || keys[0] != "25-shiny" {
		t.Fatalf("reverting a remove should restore the entry, got %v", keys)
	}
}

func TestDexGrantBumpThenRevertDecrements(t *testing.T) {
	s, db := dexTestServer(t)

	doJSON(t, s, http.MethodPost, "/portal/api/dex/Gavin/grant", `{"formKey":"25"}`)
	doJSON(t, s, http.MethodPost, "/portal/api/dex/Gavin/grant", `{"formKey":"25"}`) // bump to count 2

	// Revert the bump grant (created:false): entry stays, count back to 1. The
	// two grants can share a millisecond timestamp, so pick by payload rather
	// than relying on newest-first ordering.
	id := dexEventByCreated(t, s, "pokemon_admin_grant", false)
	w := do(t, s, http.MethodPost, "/portal/api/events/"+id+"/revert")
	if w.Code != http.StatusOK {
		t.Fatalf("revert bump: %d %s", w.Code, w.Body.String())
	}

	var value string
	if err := db.QueryRow("SELECT value FROM theme_kv WHERE theme_id='pokemon' AND key='state'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Users map[string]struct {
			Caught []struct {
				FormKey string `json:"formKey"`
				Count   int    `json:"count"`
			} `json:"caught"`
		} `json:"users"`
	}
	if err := json.Unmarshal([]byte(value), &doc); err != nil {
		t.Fatal(err)
	}
	caught := doc.Users["Gavin"].Caught
	if len(caught) != 1 || caught[0].Count != 1 {
		t.Fatalf("reverting a bump should decrement to count 1, got %+v", caught)
	}
}
