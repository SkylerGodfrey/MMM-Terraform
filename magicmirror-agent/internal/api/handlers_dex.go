package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/choresdb"
	"github.com/gin-gonic/gin"
)

// nowISOAgent matches db.js nowIso() / choresdb.nowISO: a UTC millisecond ISO-
// 8601 timestamp with a trailing Z, so a granted entry's firstAt is shaped like
// the theme's own catch timestamps.
func nowISOAgent() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// This file implements the Pokémon Theme v2 caught-dex admin surfaces (HOM-153,
// ticket #7 of epic HOM-146): a parent can browse, manually grant, and remove a
// single user's caught form-entries. State lives in the MMM-Chores SQLite
// theme_kv table (theme_id="pokemon", key="state"), whose JSON shape is owned by
// the theme (CONTRACT.md §2). Each grant/remove is logged as a REVERTIBLE event
// reusing the HOM-140 action-log/revert infra — the revert path
// (handlers_chores_state.go revertEvent) dispatches the dex-admin types here
// rather than inventing a parallel undo engine.

const (
	pokemonThemeID  = "pokemon"
	pokemonStateKey = "state"

	eventDexGrant  = "pokemon_admin_grant"
	eventDexRemove = "pokemon_admin_remove"
)

// isDexAdminEvent reports whether an event type is one of the dex-admin types
// this file reverts (used by revertEvent to route to revertDexAdmin).
func isDexAdminEvent(t string) bool {
	return t == eventDexGrant || t == eventDexRemove
}

// caughtEntry is one caught form-entry in a user's dex (CONTRACT.md §2). The
// JSON tags match the theme-owned shape exactly so a round-trip through this
// package is byte-faithful to what the module writes.
type caughtEntry struct {
	FormKey  string `json:"formKey"`
	Species  int    `json:"species"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Shiny    bool   `json:"shiny"`
	Master   bool   `json:"master"`
	Count    int    `json:"count"`
	FirstAt  string `json:"firstAt"`
}

// userState is the per-user slice of the pokemon state document. Extra fields
// the theme owns (missStreak, pending, …) are preserved via the raw map round-
// trip in load/saveThemeState — this typed view only touches `caught`.
//
// We deliberately decode/encode the WHOLE document as a generic map and only
// reach into users[<name>].caught, leaving every other field (and any future
// additions) untouched — the same "touch only what the portal owns" discipline
// the chores/rewards stores use against shared files.

// loadPokemonState reads theme_kv pokemon/state as a generic map. A missing key
// (no catches yet) yields an empty document rather than an error.
func loadPokemonState(db *choresdb.Store) (map[string]any, error) {
	raw, err := db.ThemeGet(pokemonThemeID, pokemonStateKey)
	if err != nil {
		if errors.Is(err, choresdb.ErrNotFound) {
			return map[string]any{"users": map[string]any{}}, nil
		}
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	if _, ok := doc["users"].(map[string]any); !ok {
		doc["users"] = map[string]any{}
	}
	return doc, nil
}

// savePokemonState writes the document back to theme_kv pokemon/state.
func savePokemonState(db *choresdb.Store, doc map[string]any) error {
	encoded, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return db.ThemeSet(pokemonThemeID, pokemonStateKey, encoded)
}

// usersMap returns the users object, creating it if absent.
func usersMap(doc map[string]any) map[string]any {
	users, ok := doc["users"].(map[string]any)
	if !ok {
		users = map[string]any{}
		doc["users"] = users
	}
	return users
}

// userCaught decodes one user's caught list into typed entries. A missing user
// or missing/!array caught yields an empty slice.
func userCaught(doc map[string]any, user string) []caughtEntry {
	users := usersMap(doc)
	u, ok := users[user].(map[string]any)
	if !ok {
		return nil
	}
	rawList, ok := u["caught"].([]any)
	if !ok {
		return nil
	}
	// Re-marshal the slice and decode into the typed shape (cheap and avoids a
	// hand-rolled per-field cast; the shape is small).
	b, err := json.Marshal(rawList)
	if err != nil {
		return nil
	}
	var out []caughtEntry
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// setUserCaught writes a typed caught slice back into the document for a user,
// creating the user object if needed and preserving its other fields.
func setUserCaught(doc map[string]any, user string, caught []caughtEntry) error {
	users := usersMap(doc)
	u, ok := users[user].(map[string]any)
	if !ok {
		u = map[string]any{}
		users[user] = u
	}
	// Encode through JSON so the stored shape matches the theme's exactly.
	b, err := json.Marshal(caught)
	if err != nil {
		return err
	}
	var generic []any
	if err := json.Unmarshal(b, &generic); err != nil {
		return err
	}
	if generic == nil {
		generic = []any{}
	}
	u["caught"] = generic
	return nil
}

// ---- list -------------------------------------------------------------------

func (s *Server) listDex(c *gin.Context) {
	user := strings.TrimSpace(c.Param("user"))
	if user == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Pick a person first."})
		return
	}
	db, err := s.openChoresDB()
	if err != nil {
		choresDBError(c, err)
		return
	}
	if db == nil {
		c.JSON(http.StatusOK, gin.H{"caught": []any{}, "available": false})
		return
	}
	defer db.Close()

	doc, err := loadPokemonState(db)
	if err != nil {
		choresDBError(c, err)
		return
	}
	caught := userCaught(doc, user)
	if caught == nil {
		caught = []caughtEntry{}
	}
	c.JSON(http.StatusOK, gin.H{"user": user, "caught": caught, "available": true})
}

// ---- grant ------------------------------------------------------------------

// dexFormInput is the body for grant/remove: the form-entry key to act on.
type dexFormInput struct {
	FormKey string `json:"formKey"`
}

// grantDex adds a form-entry to a user's caught list (or bumps its count if
// already present) and logs a revertible pokemon_admin_grant event. The form's
// metadata (species/name/category/shiny) is resolved from the dataset so the
// stored entry matches what the theme would write on a real catch.
func (s *Server) grantDex(c *gin.Context) {
	user := strings.TrimSpace(c.Param("user"))
	if user == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Pick a person first."})
		return
	}
	var in dexFormInput
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.FormKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Pick a Pokémon to grant."})
		return
	}
	formKey := strings.TrimSpace(in.FormKey)

	form, ok := s.lookupForm(formKey)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "That Pokémon isn't in the mirror's dataset."})
		return
	}

	db, err := s.openChoresDB()
	if err != nil {
		choresDBError(c, err)
		return
	}
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "The mirror's chore database isn't ready yet."})
		return
	}
	defer db.Close()

	doc, err := loadPokemonState(db)
	if err != nil {
		choresDBError(c, err)
		return
	}
	caught := userCaught(doc, user)

	// Whether the entry already existed determines how a revert undoes this:
	//   - created -> revert removes the entry entirely
	//   - bumped  -> revert decrements the count back
	created := true
	for i := range caught {
		if caught[i].FormKey == formKey {
			caught[i].Count++
			created = false
			break
		}
	}
	if created {
		caught = append(caught, caughtEntry{
			FormKey:  form.FormKey,
			Species:  form.Species,
			Name:     form.Name,
			Category: form.Category,
			Shiny:    form.Shiny,
			Master:   false,
			Count:    1,
			FirstAt:  nowISOAgent(),
		})
	}
	if err := setUserCaught(doc, user, caught); err != nil {
		log.Printf("portal dex grant %s/%s: encoding state: %v", user, formKey, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "The mirror couldn't update that catch."})
		return
	}
	if err := savePokemonState(db, doc); err != nil {
		choresDBError(c, err)
		return
	}

	payload, _ := json.Marshal(gin.H{"formKey": formKey, "name": form.Name, "created": created})
	if _, err := db.InsertEvent(choresdb.EventInput{Type: eventDexGrant, User: user, Payload: payload}); err != nil {
		log.Printf("portal dex grant %s/%s: logging event: %v", user, formKey, err)
	}

	c.JSON(http.StatusOK, gin.H{"user": user, "formKey": formKey, "created": created})
}

// ---- remove -----------------------------------------------------------------

// removeDex removes a form-entry from a user's caught list and logs a revertible
// pokemon_admin_remove event carrying the FULL removed entry, so a revert can
// restore it byte-for-byte (count, firstAt, master, …).
func (s *Server) removeDex(c *gin.Context) {
	user := strings.TrimSpace(c.Param("user"))
	if user == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Pick a person first."})
		return
	}
	var in dexFormInput
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.FormKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Pick a Pokémon to remove."})
		return
	}
	formKey := strings.TrimSpace(in.FormKey)

	db, err := s.openChoresDB()
	if err != nil {
		choresDBError(c, err)
		return
	}
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "The mirror's chore database isn't ready yet."})
		return
	}
	defer db.Close()

	doc, err := loadPokemonState(db)
	if err != nil {
		choresDBError(c, err)
		return
	}
	caught := userCaught(doc, user)

	idx := -1
	for i := range caught {
		if caught[i].FormKey == formKey {
			idx = i
			break
		}
	}
	if idx == -1 {
		c.JSON(http.StatusNotFound, gin.H{"error": "That Pokémon isn't in this person's dex."})
		return
	}
	removed := caught[idx]
	caught = append(caught[:idx], caught[idx+1:]...)

	if err := setUserCaught(doc, user, caught); err != nil {
		log.Printf("portal dex remove %s/%s: encoding state: %v", user, formKey, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "The mirror couldn't update that catch."})
		return
	}
	if err := savePokemonState(db, doc); err != nil {
		choresDBError(c, err)
		return
	}

	// The full entry rides in the payload so revert restores it exactly.
	entryJSON, _ := json.Marshal(removed)
	payload, _ := json.Marshal(gin.H{"formKey": formKey, "name": removed.Name, "entry": json.RawMessage(entryJSON)})
	if _, err := db.InsertEvent(choresdb.EventInput{Type: eventDexRemove, User: user, Payload: payload}); err != nil {
		log.Printf("portal dex remove %s/%s: logging event: %v", user, formKey, err)
	}

	c.JSON(http.StatusOK, gin.H{"user": user, "formKey": formKey})
}

// ---- revert -----------------------------------------------------------------

// dexAdminPayload is the union of fields the two dex-admin events carry.
type dexAdminPayload struct {
	FormKey string          `json:"formKey"`
	Created bool            `json:"created"` // grant: did it create (vs bump) the entry
	Entry   json.RawMessage `json:"entry"`   // remove: the full removed entry to restore
}

// revertDexAdmin undoes a dex-admin event against theme_kv (pokemon/state),
// reusing the same event/revert plumbing as the chore/token reverts:
//
//	pokemon_admin_grant  -> remove the entry (if it was created) or decrement
//	                        its count (if the grant only bumped an existing one)
//	pokemon_admin_remove -> restore the full removed entry
//
// Then it marks the event reverted (idempotent), exactly like revertEvent does
// for the generic types. Called from revertEvent.
func (s *Server) revertDexAdmin(c *gin.Context, db *choresdb.Store, event *choresdb.Event) {
	var p dexAdminPayload
	if len(event.Payload) > 0 {
		_ = json.Unmarshal(event.Payload, &p)
	}
	if p.FormKey == "" || event.User == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "This action is missing the details needed to revert it."})
		return
	}

	doc, err := loadPokemonState(db)
	if err != nil {
		choresDBError(c, err)
		return
	}
	caught := userCaught(doc, event.User)

	switch event.Type {
	case eventDexGrant:
		// Undo a grant: drop the entry if this grant created it, else decrement.
		for i := range caught {
			if caught[i].FormKey == p.FormKey {
				if p.Created || caught[i].Count <= 1 {
					caught = append(caught[:i], caught[i+1:]...)
				} else {
					caught[i].Count--
				}
				break
			}
		}
	case eventDexRemove:
		// Undo a remove: restore the entry, unless it's somehow back already.
		present := false
		for i := range caught {
			if caught[i].FormKey == p.FormKey {
				present = true
				break
			}
		}
		if !present {
			var entry caughtEntry
			if len(p.Entry) > 0 && json.Unmarshal(p.Entry, &entry) == nil && entry.FormKey != "" {
				caught = append(caught, entry)
			} else {
				// No stored entry to restore — refuse rather than fabricate one.
				c.JSON(http.StatusBadRequest, gin.H{"error": "This removal can't be undone — the original details are gone."})
				return
			}
		}
	}

	if err := setUserCaught(doc, event.User, caught); err != nil {
		log.Printf("portal dex revert %s: encoding state: %v", event.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "The mirror couldn't apply that revert."})
		return
	}
	if err := savePokemonState(db, doc); err != nil {
		choresDBError(c, err)
		return
	}
	if _, err := db.MarkEventReverted(event.ID); err != nil {
		choresDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"eventId": event.ID, "type": event.Type})
}

// ---- dataset lookup ---------------------------------------------------------

// formMeta is the slice of a dataset form-entry the grant flow needs.
type formMeta struct {
	FormKey  string
	Species  int
	Name     string
	Category string
	Shiny    bool
}

// lookupForm resolves a formKey to its dataset metadata (species/name/category/
// shiny) from themes/pokemon/data/pokedex.json. Returns ok=false when the
// dataset is absent or the key isn't found — the grant handler rejects then,
// rather than fabricating an entry the viewer can't render.
func (s *Server) lookupForm(formKey string) (formMeta, bool) {
	raw, err := os.ReadFile(s.pokedexPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("portal dex: reading dataset %s: %v", s.pokedexPath, err)
		}
		return formMeta{}, false
	}
	var species []struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Forms []struct {
			Key      string `json:"key"`
			Category string `json:"category"`
			Shiny    bool   `json:"shiny"`
			Label    string `json:"label"`
		} `json:"forms"`
	}
	if err := json.Unmarshal(raw, &species); err != nil {
		log.Printf("portal dex: dataset %s is not valid JSON: %v", s.pokedexPath, err)
		return formMeta{}, false
	}
	for _, sp := range species {
		for _, f := range sp.Forms {
			if f.Key == formKey {
				name := f.Label
				if name == "" {
					name = sp.Name
				}
				category := f.Category
				if category == "" {
					category = "normal"
				}
				return formMeta{FormKey: f.Key, Species: sp.ID, Name: name, Category: category, Shiny: f.Shiny}, true
			}
		}
	}
	return formMeta{}, false
}
