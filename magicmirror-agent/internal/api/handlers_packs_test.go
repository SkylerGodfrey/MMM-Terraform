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

// packsTestServer wires just the pack + pokedex routes against a seeded DB whose
// schema (ddlV1) deliberately omits the packs table — exercising the agent's
// create-if-missing path through the handlers.
func packsTestServer(t *testing.T, pokedexPath string) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chores.db")

	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ddlV1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s := &Server{
		config:       &config.Config{},
		router:       gin.New(),
		choresDBPath: dbPath,
		pokedexPath:  pokedexPath,
	}
	api := s.router.Group("/portal/api")
	api.GET("/packs", s.listPacks)
	api.POST("/packs", s.createPack)
	api.PUT("/packs/:id", s.updatePack)
	api.DELETE("/packs/:id", s.deletePack)
	api.GET("/pokedex", s.getPokedex)
	return s
}

func TestPacksCRUDEndpoints(t *testing.T) {
	s := packsTestServer(t, filepath.Join(t.TempDir(), "missing.json"))

	// Empty list (table created on first write; list before any write returns
	// an empty list because openChoresDB + ListPacks tolerate the absent table
	// once created — but here the first call is a create).
	w := doJSON(t, s, http.MethodPost, "/portal/api/packs",
		`{"name":"Kanto","startDate":"2026-07-01","endDate":"2026-07-07","members":["1","4","7"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create: code %d body %s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create should return an id: %s", w.Body.String())
	}

	// List shows it, marked inactive (range is in the past relative to "today").
	w = do(t, s, http.MethodGet, "/portal/api/packs")
	if w.Code != http.StatusOK {
		t.Fatalf("list: code %d", w.Code)
	}
	var listed struct {
		Packs []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Active bool   `json:"active"`
		} `json:"packs"`
		Available bool `json:"available"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if !listed.Available || len(listed.Packs) != 1 || listed.Packs[0].ID != id {
		t.Fatalf("list mismatch: %s", w.Body.String())
	}

	// Overlapping create is rejected with a 400 + clear message.
	w = doJSON(t, s, http.MethodPost, "/portal/api/packs",
		`{"name":"Clash","startDate":"2026-07-05","endDate":"2026-07-10"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("overlap create should be 400, got %d body %s", w.Code, w.Body.String())
	}

	// Update (move the range, keep name) succeeds.
	w = doJSON(t, s, http.MethodPut, "/portal/api/packs/"+id,
		`{"startDate":"2026-07-02","endDate":"2026-07-08"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update: code %d body %s", w.Code, w.Body.String())
	}

	// Delete returns 204.
	w = do(t, s, http.MethodDelete, "/portal/api/packs/"+id)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: code %d", w.Code)
	}
}

func TestPokedexMissingDegrades(t *testing.T) {
	s := packsTestServer(t, filepath.Join(t.TempDir(), "nope.json"))
	w := do(t, s, http.MethodGet, "/portal/api/pokedex")
	if w.Code != http.StatusOK {
		t.Fatalf("missing pokedex should degrade to 200, got %d", w.Code)
	}
	var body struct {
		Available bool `json:"available"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Available {
		t.Fatalf("missing dataset should report available:false, got %s", w.Body.String())
	}
}

func TestPokedexServesDataset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pokedex.json")
	dataset := `[{"id":1,"name":"Bulbasaur","forms":[{"key":"1","category":"normal","shiny":false,"label":"Bulbasaur"}]}]`
	if err := os.WriteFile(path, []byte(dataset), 0o644); err != nil {
		t.Fatal(err)
	}
	s := packsTestServer(t, path)
	w := do(t, s, http.MethodGet, "/portal/api/pokedex")
	if w.Code != http.StatusOK {
		t.Fatalf("code %d", w.Code)
	}
	var body struct {
		Species   json.RawMessage `json:"species"`
		Available bool            `json:"available"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Available {
		t.Fatalf("should report available:true: %s", w.Body.String())
	}
	var species []map[string]any
	if err := json.Unmarshal(body.Species, &species); err != nil || len(species) != 1 {
		t.Fatalf("species should round-trip: %v / %s", err, body.Species)
	}
}
