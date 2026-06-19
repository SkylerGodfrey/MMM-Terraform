package mmconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realisticConfigJS mirrors a stock MagicMirror config.js: unquoted keys,
// comments, single quotes, trailing commas, and keys the agent doesn't model.
const realisticConfigJS = `/* MagicMirror² Config Sample
 * For more information on how you can configure this file
 * see https://docs.magicmirror.builders/configuration/introduction.html
 */
let config = {
	address: "localhost", // Address to listen on
	port: 8080,
	basePath: "/",
	ipWhitelist: ["127.0.0.1", "::ffff:127.0.0.1", "::1"],

	useHttps: false, // not modeled by the agent — must survive round-trips
	httpsPrivateKey: "",

	language: 'en',
	locale: "en-US",
	logLevel: ["INFO", "LOG", "WARN", "ERROR"],
	timeFormat: 24,
	units: "metric",

	modules: [
		{
			module: "alert",
		},
		{
			module: "clock",
			position: "top_left"
		},
		{
			module: "calendar",
			header: "US Holidays",
			position: "top_left",
			animateIn: "fadeIn", // not modeled — must survive round-trips
			config: {
				calendars: [
					{
						fetchInterval: 7 * 24 * 60 * 60 * 1000,
						symbol: "calendar-check",
						url: "https://ics.calendarlabs.com/76/mm3137/US_Holidays.ics"
					}
				]
			}
		},
	]
};

/*************** DO NOT EDIT THE LINE BELOW ***************/
if (typeof module !== "undefined") { module.exports = config; }
`

func TestParseRealisticConfigJS(t *testing.T) {
	cfg, err := parseConfigJS([]byte(realisticConfigJS))
	if err != nil {
		t.Fatalf("parseConfigJS failed: %v", err)
	}

	if cfg.Global.Address != "localhost" {
		t.Errorf("address = %q, want localhost", cfg.Global.Address)
	}
	if cfg.Global.Port != 8080 {
		t.Errorf("port = %d, want 8080", cfg.Global.Port)
	}
	if cfg.Global.TimeFormat != 24 {
		t.Errorf("timeFormat = %d, want 24", cfg.Global.TimeFormat)
	}
	if len(cfg.Global.IPWhitelist) != 3 {
		t.Errorf("ipWhitelist has %d entries, want 3", len(cfg.Global.IPWhitelist))
	}
	if _, ok := cfg.Extras["useHttps"]; !ok {
		t.Error("unmodeled key useHttps not preserved in Extras")
	}

	if len(cfg.Modules) != 3 {
		t.Fatalf("got %d modules, want 3", len(cfg.Modules))
	}
	cal := cfg.Modules[2]
	if cal.Module != "calendar" || cal.Header != "US Holidays" {
		t.Errorf("calendar module parsed wrong: %+v", cal)
	}
	if cal.Extras["animateIn"] != "fadeIn" {
		t.Errorf("unmodeled module key animateIn not preserved: %v", cal.Extras)
	}
	cals, ok := cal.Config["calendars"].([]any)
	if !ok || len(cals) != 1 {
		t.Fatalf("calendar config not parsed: %v", cal.Config)
	}
	entry := cals[0].(map[string]any)
	if entry["fetchInterval"].(float64) != 7*24*60*60*1000 {
		t.Errorf("JS expression fetchInterval not evaluated: %v", entry["fetchInterval"])
	}
}

func TestRoundTrip(t *testing.T) {
	cfg, err := parseConfigJS([]byte(realisticConfigJS))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	out, err := generateConfigJS(cfg)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	cfg2, err := parseConfigJS(out)
	if err != nil {
		t.Fatalf("re-parse of generated config failed: %v\n---\n%s", err, out)
	}

	if cfg2.Global.Address != cfg.Global.Address || cfg2.Global.Port != cfg.Global.Port {
		t.Errorf("global settings changed across round-trip: %+v vs %+v", cfg.Global, cfg2.Global)
	}
	if len(cfg2.Modules) != len(cfg.Modules) {
		t.Fatalf("module count changed: %d vs %d", len(cfg.Modules), len(cfg2.Modules))
	}
	if _, ok := cfg2.Extras["useHttps"]; !ok {
		t.Error("useHttps lost across round-trip")
	}
	if cfg2.Modules[2].Extras["animateIn"] != "fadeIn" {
		t.Error("module extra animateIn lost across round-trip")
	}
}

func TestGenerateEmptyModules(t *testing.T) {
	out, err := generateConfigJS(&MagicMirrorConfig{
		Global: GlobalConfig{Address: "0.0.0.0", Port: 8080},
	})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	cfg, err := parseConfigJS(out)
	if err != nil {
		t.Fatalf("re-parse failed: %v\n---\n%s", err, out)
	}
	// modules key must exist as [] so MagicMirror doesn't crash
	if !strings.Contains(string(out), `"modules"`) {
		t.Errorf("generated config.js missing modules key:\n%s", out)
	}
	_ = cfg
}

// newTestManager writes a minimal config.js to a temp dir and returns a
// Manager pointed at it with the restart command unset (so ScheduleRestart
// is a no-op — no pm2 in the test env).
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.js")
	if err := os.WriteFile(path, []byte(realisticConfigJS), 0o644); err != nil {
		t.Fatalf("write seed config: %v", err)
	}
	return NewManager(path, "")
}

func TestCreateModule_DefaultsPositionWhenEmpty(t *testing.T) {
	m := newTestManager(t)
	created, err := m.CreateModule(&Module{Module: "MMM-Mascot"})
	if err != nil {
		t.Fatalf("CreateModule: %v", err)
	}
	if created.Position != DefaultPosition {
		t.Errorf("Position: want %q, got %q", DefaultPosition, created.Position)
	}
	// Round-trip through disk to confirm the default landed in config.js.
	cfg, err := m.ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	var persisted *Module
	for i := range cfg.Modules {
		if cfg.Modules[i].ID == created.ID {
			persisted = &cfg.Modules[i]
			break
		}
	}
	if persisted == nil {
		t.Fatalf("created module not found in config")
	}
	if persisted.Position != DefaultPosition {
		t.Errorf("persisted Position: want %q, got %q", DefaultPosition, persisted.Position)
	}
}

func TestCreateModule_PreservesExplicitPosition(t *testing.T) {
	m := newTestManager(t)
	created, err := m.CreateModule(&Module{Module: "clock", Position: "top_right"})
	if err != nil {
		t.Fatalf("CreateModule: %v", err)
	}
	if created.Position != "top_right" {
		t.Errorf("Position: want %q, got %q", "top_right", created.Position)
	}
}

func TestUpdateModule_DoesNotApplyDefaultPosition(t *testing.T) {
	m := newTestManager(t)
	created, err := m.CreateModule(&Module{Module: "MMM-Mascot", Position: "top_bar"})
	if err != nil {
		t.Fatalf("CreateModule: %v", err)
	}
	// An explicit edit that clears the position should NOT silently get
	// re-defaulted — only the create path applies the default.
	updated, err := m.UpdateModule(&Module{ID: created.ID, Module: "MMM-Mascot", Position: ""})
	if err != nil {
		t.Fatalf("UpdateModule: %v", err)
	}
	if updated.Position != "" {
		t.Errorf("UpdateModule should leave Position empty when set explicitly to empty, got %q", updated.Position)
	}
}
