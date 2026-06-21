package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/choresdb"
	"github.com/SkylerGodfrey/magicmirror-agent/internal/packs"
	"github.com/gin-gonic/gin"
)

// This file implements the Pokémon Theme v2 pack-scheduling surfaces (HOM-150,
// ticket #4 of epic HOM-146): family-portal CRUD over the household-global,
// date-ranged encounter pools in the MMM-Chores SQLite `packs` table, plus a
// read-only dataset endpoint that powers the member picker. The
// sequential/non-overlapping rule and the active-pool resolver live in
// internal/packs (the Go port of store/packs.js); these handlers are the HTTP
// shell + the same family-friendly error mapping as the other portal surfaces.

// packView decorates a pack with whether it's the active pool today, so the
// portal can mark today's pack on the timeline without a second round trip.
type packView struct {
	choresdb.Pack
	Active bool `json:"active"`
}

// packsError maps internal/packs + choresdb errors to family-friendly responses.
// Validation/overlap errors carry a human message and surface as 400; storage
// and schema errors reuse choresDBError's mapping.
func packsError(c *gin.Context, err error) {
	if errors.Is(err, packs.ErrValidation) {
		// internal/packs wraps the human-readable detail after the sentinel; show
		// it as-is (it's written for parents, e.g. the overlap message).
		c.JSON(http.StatusBadRequest, gin.H{"error": packs.Message(err)})
		return
	}
	choresDBError(c, err)
}

// packInput is the create/update body. Dates are 'yyyy-mm-dd'; members is the
// JSON array of form-entry keys. On update, omitted fields keep their stored
// value (a nil Members means "unchanged"; an explicit [] clears the pool).
type packInput struct {
	Name      string    `json:"name"`
	StartDate string    `json:"startDate"`
	EndDate   string    `json:"endDate"`
	Members   *[]string `json:"members"`
}

func (in packInput) fields() packs.Fields {
	f := packs.Fields{Name: in.Name, StartDate: in.StartDate, EndDate: in.EndDate}
	if in.Members != nil {
		f.Members = *in.Members
	}
	return f
}

func (s *Server) listPacks(c *gin.Context) {
	db, err := s.openChoresDB()
	if err != nil {
		packsError(c, err)
		return
	}
	if db == nil {
		c.JSON(http.StatusOK, gin.H{"packs": []any{}, "today": packs.LocalDate(time.Now()), "available": false})
		return
	}
	defer db.Close()

	list, err := packs.List(db)
	if err != nil {
		packsError(c, err)
		return
	}
	today := packs.LocalDate(time.Now())
	out := make([]packView, 0, len(list))
	for _, p := range list {
		out = append(out, packView{Pack: p, Active: p.StartDate <= today && today <= p.EndDate})
	}
	c.JSON(http.StatusOK, gin.H{"packs": out, "today": today, "available": true})
}

func (s *Server) createPack(c *gin.Context) {
	var in packInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	db, err := s.openChoresDB()
	if err != nil {
		packsError(c, err)
		return
	}
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "The mirror's chore database isn't ready yet."})
		return
	}
	defer db.Close()

	p, err := packs.Create(db, in.fields())
	if err != nil {
		packsError(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}

func (s *Server) updatePack(c *gin.Context) {
	var in packInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	db, err := s.openChoresDB()
	if err != nil {
		packsError(c, err)
		return
	}
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "The mirror's chore database isn't ready yet."})
		return
	}
	defer db.Close()

	p, err := packs.Update(db, c.Param("id"), in.fields())
	if err != nil {
		packsError(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}

func (s *Server) deletePack(c *gin.Context) {
	db, err := s.openChoresDB()
	if err != nil {
		packsError(c, err)
		return
	}
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "The mirror's chore database isn't ready yet."})
		return
	}
	defer db.Close()

	if err := packs.Delete(db, c.Param("id")); err != nil {
		packsError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- dataset (member picker) ------------------------------------------------

// getPokedex serves the Pokémon Theme v2 dataset (themes/pokemon/data/pokedex.json)
// so the portal's member picker can browse/search species + forms client-side.
// The dataset is owned by MMM-Chores (HOM-147) and read-only here. When it isn't
// present yet (the assets ticket hasn't shipped, or the agent runs off-Pi) the
// endpoint degrades to an empty list + available:false so the packs UI still
// loads — the picker just shows "dataset not on the mirror yet".
func (s *Server) getPokedex(c *gin.Context) {
	raw, err := os.ReadFile(s.pokedexPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusOK, gin.H{"species": []any{}, "available": false})
			return
		}
		log.Printf("portal pokedex: reading %s: %v", s.pokedexPath, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "The mirror couldn't read the Pokémon dataset."})
		return
	}

	// The dataset is an array of species. Validate it parses, then stream it back
	// untouched (the theme owns the shape; the picker reads forms[] directly).
	var species json.RawMessage
	if err := json.Unmarshal(raw, &species); err != nil || !json.Valid(raw) {
		log.Printf("portal pokedex: %s is not valid JSON: %v", s.pokedexPath, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "The mirror's Pokémon dataset is corrupt."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"species": species, "available": true})
}
