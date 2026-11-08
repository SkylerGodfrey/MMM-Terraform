package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/SkylerGodfrey/magicmirror-agent/internal/choresdb"
	"github.com/gin-gonic/gin"
)

// scheduleActivationsKey matches node_helper.js SCHEDULE_META_KEY: a JSON map
// of "<choreId>|<user>" -> ISO time the chore's current window opened.
const scheduleActivationsKey = "schedule_activations"

// choresSettings is the slice of the MMM-Chores module config (config.js) the
// portal needs to explain schedules: morning/nightly times, whether calendar
// (days/weeks) schedules are on, and per-person colour overrides.
type choresSettings struct {
	Schedule          map[string]any `json:"schedule"`
	CalendarSchedules bool           `json:"calendarSchedules"`
	PersonColors      map[string]any `json:"personColors"`
}

// choresModuleSettings reads choresSettings from config.js. Best-effort like
// disabledRewardUsers: any failure yields the module defaults.
func (s *Server) choresModuleSettings() choresSettings {
	out := choresSettings{
		Schedule: map[string]any{
			"defaults": map[string]any{"morning": "06:00", "nightly": "17:00"},
			"perUser":  map[string]any{},
		},
		CalendarSchedules: true,
		PersonColors:      map[string]any{},
	}
	if s.mmManager == nil {
		return out
	}
	mods, err := s.mmManager.ListModules()
	if err != nil {
		log.Printf("portal chores: reading module config for schedule: %v", err)
		return out
	}
	for _, mod := range mods {
		if mod.Module != "MMM-Chores" {
			continue
		}
		if sched, ok := mod.Config["schedule"].(map[string]any); ok {
			if def, ok := sched["defaults"].(map[string]any); ok {
				merged := map[string]any{"morning": "06:00", "nightly": "17:00"}
				for k, v := range def {
					merged[k] = v
				}
				out.Schedule["defaults"] = merged
			}
			if per, ok := sched["perUser"].(map[string]any); ok {
				out.Schedule["perUser"] = per
			}
		}
		if v, ok := mod.Config["calendarSchedules"].(bool); ok {
			out.CalendarSchedules = v
		}
		if pc, ok := mod.Config["personColors"].(map[string]any); ok {
			out.PersonColors = pc
		}
		break
	}
	return out
}

// choresStatus returns live per-(chore,user) state so the portal can show what
// is open, done, or coming back when — plus the schedule settings above.
// Degrades to available:false (settings still present) without the DB.
func (s *Server) choresStatus(c *gin.Context) {
	settings := s.choresModuleSettings()
	resp := gin.H{
		"available":         false,
		"completions":       []choresdb.Completion{},
		"activations":       map[string]string{},
		"schedule":          settings.Schedule,
		"calendarSchedules": settings.CalendarSchedules,
		"personColors":      settings.PersonColors,
	}

	db, err := s.openChoresDB()
	if err != nil {
		choresDBError(c, err)
		return
	}
	if db == nil {
		c.JSON(http.StatusOK, resp)
		return
	}
	defer db.Close()

	completions, err := db.ListCompletions()
	if err != nil {
		choresDBError(c, err)
		return
	}
	if completions != nil {
		resp["completions"] = completions
	}

	if raw, err := db.MetaGet(scheduleActivationsKey); err == nil && raw != "" {
		var acts map[string]string
		if json.Unmarshal([]byte(raw), &acts) == nil {
			resp["activations"] = acts
		}
	} else if err != nil && !errors.Is(err, choresdb.ErrNotFound) {
		log.Printf("portal chores: reading schedule activations: %v", err)
	}

	resp["available"] = true
	c.JSON(http.StatusOK, resp)
}
