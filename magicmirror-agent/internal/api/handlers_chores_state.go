package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/choresdb"
	"github.com/gin-gonic/gin"
)

// This file implements the runtime-state portal surfaces backed by the
// MMM-Chores SQLite DB the module owns (HOM-132):
//
//   HOM-139  pending-approval queue  — list / approve / deny
//   HOM-140  activity log + revert   — list events / revert one
//
// Approve and revert deliberately replicate the module's node_helper.js
// semantics (completeNow, revertActions) so the agent and module compute the
// same balances and completion transitions against the shared file.

// dbUnavailable degrades gracefully when the module DB isn't there yet: the
// portal shows an empty queue/log instead of an error so the family page still
// works before the module's first run. Returns true if it wrote a response.
func (s *Server) dbUnavailable(c *gin.Context, db *choresdb.Store, emptyKey string) bool {
	if db == nil {
		c.JSON(http.StatusOK, gin.H{emptyKey: []any{}, "available": false})
		return true
	}
	return false
}

// choresDBError maps choresdb errors to family-friendly responses, logging the
// detail to the journal (matching choreError / rewardError).
func choresDBError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, choresdb.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "That item is gone — the mirror may have already handled it. Pull to refresh."})
	case errors.Is(err, choresdb.ErrSchemaMismatch):
		log.Printf("portal chores-state: %v", err)
		c.JSON(http.StatusConflict, gin.H{"error": "The mirror's chore database is a newer version than the portal understands. Update the agent."})
	default:
		log.Printf("portal chores-state: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "The mirror couldn't reach its chore database. Try again in a minute."})
	}
}

// ---- HOM-139: pending-approval queue ----------------------------------------

// pendingView decorates a queue row with the chore name + assignees + mode so
// the portal can render it without a second round trip.
type pendingView struct {
	choresdb.PendingItem
	ChoreName string   `json:"choreName"`
	Mode      string   `json:"mode"`
	Assignees []string `json:"assignees"`
}

func (s *Server) listPending(c *gin.Context) {
	db, err := s.openChoresDB()
	if err != nil {
		choresDBError(c, err)
		return
	}
	if s.dbUnavailable(c, db, "pending") {
		return
	}
	defer db.Close()

	items, err := db.ListPending()
	if err != nil {
		choresDBError(c, err)
		return
	}

	// Resolve chore names/modes once from the definition store.
	defs := s.choreDefIndex()
	out := make([]pendingView, 0, len(items))
	for _, it := range items {
		v := pendingView{PendingItem: it}
		if def, ok := defs[it.ChoreID]; ok {
			v.ChoreName = def.name
			v.Mode = def.mode
			v.Assignees = def.assignees
		}
		out = append(out, v)
	}
	c.JSON(http.StatusOK, gin.H{"pending": out, "available": true})
}

// choreDef is the slice of a chore definition the state handlers need.
type choreDef struct {
	name      string
	mode      string
	assignees []string
	anyone    bool
	tokens    *int
}

// choreDefIndex loads the chore definitions and indexes them by id, normalizing
// to the multi-assignee shape (List already migrates legacy assignee → assignees).
func (s *Server) choreDefIndex() map[string]choreDef {
	out := map[string]choreDef{}
	list, err := s.choreStore.List()
	if err != nil {
		log.Printf("portal chores-state: loading definitions: %v", err)
		return out
	}
	for _, ch := range list {
		id, _ := ch["id"].(string)
		if id == "" {
			continue
		}
		def := choreDef{}
		def.name, _ = ch["name"].(string)
		def.anyone, _ = ch["anyone"].(bool)
		def.mode, _ = ch["mode"].(string)
		if def.mode == "" {
			def.mode = "shared"
		}
		if raw, ok := ch["assignees"].([]any); ok {
			for _, v := range raw {
				if name, ok := v.(string); ok && name != "" {
					def.assignees = append(def.assignees, name)
				}
			}
		}
		switch t := ch["tokens"].(type) {
		case int:
			def.tokens = &t
		case float64:
			n := int(t)
			def.tokens = &n
		}
		out[id] = def
	}
	return out
}

// approvePending mirrors node_helper.completeNow: in one DB transaction it sets
// the affected completions to "done", removes the queue row, grants the item's
// tokens to its user via the rewards store, and logs a chore_completed event.
//
// Affected completions:
//   - independent mode (or unknown): just the pending item's own user.
//   - shared mode: every assignee of the chore (first-done closes it for all).
func (s *Server) approvePending(c *gin.Context) {
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

	item, err := db.GetPending(c.Param("id"))
	if err != nil {
		choresDBError(c, err)
		return
	}

	defs := s.choreDefIndex()
	def := defs[item.ChoreID]
	affected := affectedUsers(def, item.User)

	// DB transitions first (the module's completeNow does the DB writes in a
	// transaction; choresdb exposes per-row upserts, so we do them in sequence —
	// the queue is single-parent so there's no concurrent approver).
	for _, user := range affected {
		if _, err := db.UpsertCompletion(item.ChoreID, user, "done", ""); err != nil {
			choresDBError(c, err)
			return
		}
	}
	if err := db.DeletePending(item.ID); err != nil {
		choresDBError(c, err)
		return
	}

	// Grant tokens to the item's user (the earner), matching completeNow's
	// applyChoreTokens on the acting user. Best-effort balance read for the body.
	if item.Tokens != 0 {
		if _, err := s.rewardStore.AdjustTokens(item.User, item.Tokens); err != nil {
			// Tokens couldn't be granted, but the chore is already approved on
			// the mirror. Log and continue rather than leaving a half-approved
			// item in the queue (it's already removed).
			log.Printf("portal pending approve %s: granting %d tokens to %s: %v", item.ID, item.Tokens, item.User, err)
		}
	}

	// Log a revertible chore_completed event (matches logChoreCompleted).
	payload, _ := json.Marshal(gin.H{"choreId": item.ChoreID, "name": def.name, "tokens": item.Tokens})
	if _, err := db.InsertEvent(choresdb.EventInput{
		Type:    "chore_completed",
		User:    item.User,
		Payload: payload,
	}); err != nil {
		log.Printf("portal pending approve %s: logging event: %v", item.ID, err)
	}

	c.JSON(http.StatusOK, gin.H{"choreId": item.ChoreID, "user": item.User, "affected": affected})
}

// denyPending mirrors the deny half of node_helper.uncompleteChore for a
// queued item: set the affected completions back to "open", remove the queue
// row, grant NO tokens. The theme_payload is echoed back so the caller (and the
// mirror via HOM-137) can release a Pokémon; the DB transitions are all we own.
func (s *Server) denyPending(c *gin.Context) {
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

	item, err := db.GetPending(c.Param("id"))
	if err != nil {
		choresDBError(c, err)
		return
	}

	defs := s.choreDefIndex()
	affected := affectedUsers(defs[item.ChoreID], item.User)
	for _, user := range affected {
		if _, err := db.UpsertCompletion(item.ChoreID, user, "open", ""); err != nil {
			choresDBError(c, err)
			return
		}
	}
	if err := db.DeletePending(item.ID); err != nil {
		choresDBError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"choreId":      item.ChoreID,
		"user":         item.User,
		"affected":     affected,
		"themePayload": item.ThemePayload,
	})
}

// affectedUsers returns which completion rows a queue action touches: every
// assignee for a shared chore (first-done closes it for all), otherwise just
// the acting user. Mirrors node_helper's `affected` set.
func affectedUsers(def choreDef, actingUser string) []string {
	if def.mode == "shared" && len(def.assignees) > 0 {
		return append([]string(nil), def.assignees...)
	}
	return []string{actingUser}
}

// ---- HOM-140: activity log + revert -----------------------------------------

func (s *Server) listEvents(c *gin.Context) {
	db, err := s.openChoresDB()
	if err != nil {
		choresDBError(c, err)
		return
	}
	if db == nil {
		c.JSON(http.StatusOK, gin.H{"events": []any{}, "retentionDays": retentionDaysDefault, "available": false})
		return
	}
	defer db.Close()

	filter := choresdb.EventFilter{
		User:            c.Query("user"),
		Since:           c.Query("since"),
		IncludeReverted: true, // the log shows reverted entries struck-through
		Limit:           50,   // default page size
	}
	if raw := c.Query("type"); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				filter.Types = append(filter.Types, t)
			}
		}
	}
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			filter.Limit = n
		}
	}

	events, err := db.ListEvents(filter)
	if err != nil {
		choresDBError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"events":        events,
		"retentionDays": retentionDaysDefault,
		"available":     true,
	})
}

// revertEvent replicates store/revert.js revertActions EXACTLY, then marks the
// event reverted (idempotent), mirroring node_helper.revertEvent:
//
//	chore_completed -> reopen (choreId,user) completion + debit p.tokens
//	reward_redeemed -> refund p.cost + (if p.restock) +1 the reward quantity
//	tokens_earned   -> debit p.amount (the grant)
//	pokemon_catch   -> theme-specific release; out of scope here (rejected).
func (s *Server) revertEvent(c *gin.Context) {
	db, err := s.openChoresDB()
	if err != nil {
		choresDBError(c, err)
		return
	}
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Revert needs the mirror's chore database, which isn't ready yet."})
		return
	}
	defer db.Close()

	event, err := db.GetEvent(c.Param("id"))
	if err != nil {
		choresDBError(c, err)
		return
	}
	if event.RevertedAt != "" {
		c.JSON(http.StatusConflict, gin.H{"error": "That action was already reverted."})
		return
	}

	// HOM-153: dex-admin events (manual grant / remove of a caught Pokémon) are
	// reverted against theme_kv (pokemon/state), not the token/completion engine.
	// They reuse this same event/revert plumbing — handled here before the
	// generic revertActions dispatch, which only knows the chore/token types.
	if isDexAdminEvent(event.Type) {
		s.revertDexAdmin(c, db, event)
		return
	}

	act, ok := revertActions(event)
	if !ok {
		// pokemon_catch and any unknown type land here (revertActions returns
		// null in revert.js). The activity log shows them; their revert is the
		// theme's job (HOM-137), not the generic engine.
		c.JSON(http.StatusBadRequest, gin.H{"error": "This kind of action can't be reverted from the portal."})
		return
	}

	// 1) Reopen the completion, if any (chore_completed only).
	if act.reopen != nil {
		if _, err := db.UpsertCompletion(act.reopen.choreID, act.reopen.user, "open", ""); err != nil {
			choresDBError(c, err)
			return
		}
	}
	// 2) Apply the token delta and any restock atomically against rewards.yaml.
	if act.user != "" && (act.tokenDelta != 0 || act.restockReward != "") {
		if _, err := s.rewardStore.AdjustTokensAndRestock(act.user, act.tokenDelta, act.restockReward); err != nil {
			log.Printf("portal revert %s: adjusting tokens/restock for %s: %v", event.ID, act.user, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "The mirror couldn't update the token balance for that revert."})
			return
		}
	}
	// 3) Mark reverted (idempotent). markReverted is last so a token failure
	//    above leaves the event revertible for a retry.
	if _, err := db.MarkEventReverted(event.ID); err != nil {
		choresDBError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"eventId": event.ID, "type": event.Type})
}

// ---- manual token adjustment (portal-only) ----------------------------------

// adjustBalanceInput is the body for a manual token adjustment: step a user's
// balance by Delta, or Reset it to zero.
type adjustBalanceInput struct {
	User  string `json:"user"`
	Delta int    `json:"delta"`
	Reset bool   `json:"reset"`
}

// adjustUserBalance lets a parent reset or step a user's token balance from the
// portal — a control the mirror deliberately doesn't expose. Token balances
// live in rewards.yaml (via rewardStore.AdjustTokens, clamped at zero, matching
// node_helper.adjustTokens); the change is also recorded in the activity log as
// a best-effort tokens_adjusted event so it shows in the family log. Like
// approve/deny/revert, this is a parent action on the LAN portal and is not
// PIN-gated.
func (s *Server) adjustUserBalance(c *gin.Context) {
	var in adjustBalanceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(in.User) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Pick a person first."})
		return
	}

	delta := in.Delta
	if in.Reset {
		cur, err := s.userBalance(in.User)
		if err != nil {
			rewardError(c, err)
			return
		}
		delta = -cur // bring the balance to zero
	}

	// Nothing to change (e.g. −1 at a zero balance, or reset when already empty).
	if delta == 0 {
		bal, err := s.userBalance(in.User)
		if err != nil {
			rewardError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"user": in.User, "tokens": bal, "delta": 0})
		return
	}

	balance, err := s.rewardStore.AdjustTokens(in.User, delta)
	if err != nil {
		rewardError(c, err)
		return
	}

	// Best-effort activity-log entry. Balances live in rewards.yaml, so the
	// adjustment stands even if the chore DB isn't up; logging just no-ops then.
	if db, derr := s.openChoresDB(); derr == nil && db != nil {
		defer db.Close()
		payload, _ := json.Marshal(gin.H{"delta": delta, "reset": in.Reset, "balance": balance})
		if _, err := db.InsertEvent(choresdb.EventInput{
			Type:    "tokens_adjusted",
			User:    in.User,
			Payload: payload,
		}); err != nil {
			log.Printf("portal adjust tokens %s: logging event: %v", in.User, err)
		}
	}

	c.JSON(http.StatusOK, gin.H{"user": in.User, "tokens": balance, "delta": delta})
}

// userBalance reads a single user's current token balance from rewards.yaml.
// An unknown user reads as zero (consistent with adjustUserTokens creating on
// first credit).
func (s *Server) userBalance(name string) (int, error) {
	users, err := s.rewardStore.Users()
	if err != nil {
		return 0, err
	}
	for _, u := range users {
		if n, _ := u["name"].(string); n == name {
			switch t := u["tokens"].(type) {
			case int:
				return t, nil
			case float64:
				return int(t), nil
			}
			return 0, nil
		}
	}
	return 0, nil
}

// reopenTarget is the (chore,user) completion a revert reopens, or nil.
type reopenTarget struct {
	choreID string
	user    string
}

// revertDecision is the Go port of store/revert.js revertActions's return shape:
//
//	{ tokenDelta, user, restockReward, reopen }
//
// Keeping it a separate value (rather than inlining in the handler) mirrors the
// "pure decision" design of revert.js so it can be unit-tested in isolation.
type revertDecision struct {
	tokenDelta    int
	user          string
	restockReward string
	reopen        *reopenTarget
}

// revertActions is a faithful port of store/revert.js revertActions(event).
// Returns (decision, true) for revertible types, or (zero, false) when the
// event type isn't revertible (revert.js returns null) — i.e. pokemon_catch and
// anything unknown.
func revertActions(event *choresdb.Event) (revertDecision, bool) {
	if event == nil || event.Type == "" {
		return revertDecision{}, false
	}
	p := decodeRevertPayload(event.Payload)

	switch event.Type {
	case "tokens_earned":
		return revertDecision{
			tokenDelta:    -p.Amount,
			user:          event.User,
			restockReward: "",
			reopen:        nil,
		}, true
	case "reward_redeemed":
		restock := ""
		if p.Restock {
			restock = p.Reward
		}
		return revertDecision{
			tokenDelta:    p.Cost,
			user:          event.User,
			restockReward: restock,
			reopen:        nil,
		}, true
	case "chore_completed":
		var reopen *reopenTarget
		if p.ChoreID != "" && event.User != "" {
			reopen = &reopenTarget{choreID: p.ChoreID, user: event.User}
		}
		return revertDecision{
			tokenDelta:    -p.Tokens,
			user:          event.User,
			restockReward: "",
			reopen:        reopen,
		}, true
	default:
		return revertDecision{}, false
	}
}

// revertPayload is the union of fields the three revertible event types carry in
// their JSON payload (matches the shapes logged by node_helper.js).
type revertPayload struct {
	Amount  int    `json:"amount"`  // tokens_earned
	Cost    int    `json:"cost"`    // reward_redeemed
	Reward  string `json:"reward"`  // reward_redeemed
	Restock bool   `json:"restock"` // reward_redeemed
	ChoreID string `json:"choreId"` // chore_completed
	Tokens  int    `json:"tokens"`  // chore_completed
}

func decodeRevertPayload(raw json.RawMessage) revertPayload {
	var p revertPayload
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p) // a malformed payload yields the zero value
	}
	return p
}
