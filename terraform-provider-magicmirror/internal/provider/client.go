package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// MagicMirrorClient handles communication with the Magic Mirror Agent API
type MagicMirrorClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// Module represents a Magic Mirror module
type Module struct {
	ID       string         `json:"id,omitempty"`
	Module   string         `json:"module"`
	Position string         `json:"position,omitempty"`
	Header   string         `json:"header,omitempty"`
	Disabled bool           `json:"disabled,omitempty"`
	Classes  string         `json:"classes,omitempty"`
	Config   map[string]any `json:"config,omitempty"`
}

// GlobalConfig represents Magic Mirror global configuration
type GlobalConfig struct {
	Address     string   `json:"address,omitempty"`
	Port        int      `json:"port,omitempty"`
	BasePath    string   `json:"basePath,omitempty"`
	IPWhitelist []string `json:"ipWhitelist,omitempty"`
	Zoom        float64  `json:"zoom,omitempty"`
	Language    string   `json:"language,omitempty"`
	Locale      string   `json:"locale,omitempty"`
	LogLevel    []string `json:"logLevel,omitempty"`
	TimeFormat  int      `json:"timeFormat,omitempty"`
	Units       string   `json:"units,omitempty"`
	ServerOnly  bool     `json:"serverOnly,omitempty"`
}

// InstalledModule represents a module directory on the Pi with git info
type InstalledModule struct {
	Name       string `json:"name"`
	Ref        string `json:"ref"`
	Commit     string `json:"commit"`
	Repository string `json:"repository"`
}

// CanvasConfig mirrors the agent's canvas.Canvas — singleton globals for
// the Canvas v2 layout surface (HOM-104).
type CanvasConfig struct {
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	DebugBorders bool   `json:"debugBorders"`
	DebugLabels  bool   `json:"debugLabels"`
	DefaultPage  string `json:"defaultPage"`
}

// Page represents a named slot set on the canvas. Page identity is the
// resource address (e.g., magicmirror_page.home → "home"); slots inside
// reference modules by their agent-assigned ID.
type Page struct {
	Slots []Slot `json:"slots"`
}

// Slot is a single rectangular placement of a module on the canvas.
// Coordinates are pixel offsets from the canvas top-left.
type Slot struct {
	Module string `json:"module"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	ZIndex int    `json:"zIndex,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

// APIError represents an error response from the API
type APIError struct {
	Message string `json:"error"`
}

func (e *APIError) Error() string {
	return e.Message
}

// doRequest performs an HTTP request with authentication
func (c *MagicMirrorClient) doRequest(method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	return c.HTTPClient.Do(req)
}

// parseResponse reads and parses the response body
func parseResponse[T any](resp *http.Response) (*T, error) {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr APIError
		if err := json.Unmarshal(body, &apiErr); err != nil {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		}
		return nil, &apiErr
	}

	var result T
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &result, nil
}

// GetModule retrieves a module by ID
func (c *MagicMirrorClient) GetModule(id string) (*Module, error) {
	resp, err := c.doRequest(http.MethodGet, "/modules/"+id, nil)
	if err != nil {
		return nil, err
	}
	return parseResponse[Module](resp)
}

// CreateModule creates a new module
func (c *MagicMirrorClient) CreateModule(module *Module) (*Module, error) {
	resp, err := c.doRequest(http.MethodPost, "/modules", module)
	if err != nil {
		return nil, err
	}
	return parseResponse[Module](resp)
}

// UpdateModule updates an existing module
func (c *MagicMirrorClient) UpdateModule(module *Module) (*Module, error) {
	resp, err := c.doRequest(http.MethodPut, "/modules/"+module.ID, module)
	if err != nil {
		return nil, err
	}
	return parseResponse[Module](resp)
}

// DeleteModule deletes a module
func (c *MagicMirrorClient) DeleteModule(id string) error {
	resp, err := c.doRequest(http.MethodDelete, "/modules/"+id, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		var apiErr APIError
		if err := json.Unmarshal(body, &apiErr); err != nil {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		}
		return &apiErr
	}

	return nil
}

// GetConfig retrieves the global configuration
func (c *MagicMirrorClient) GetConfig() (*GlobalConfig, error) {
	resp, err := c.doRequest(http.MethodGet, "/config", nil)
	if err != nil {
		return nil, err
	}

	// The API returns { global: {...}, modules: [...] }
	type configResponse struct {
		Global GlobalConfig `json:"global"`
	}
	result, err := parseResponse[configResponse](resp)
	if err != nil {
		return nil, err
	}
	return &result.Global, nil
}

// UpdateConfig updates the global configuration
func (c *MagicMirrorClient) UpdateConfig(config *GlobalConfig) error {
	resp, err := c.doRequest(http.MethodPut, "/config", config)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		var apiErr APIError
		if err := json.Unmarshal(body, &apiErr); err != nil {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		}
		return &apiErr
	}

	return nil
}

// GetMMVersion retrieves the core MagicMirror version (read-only)
func (c *MagicMirrorClient) GetMMVersion() (string, error) {
	resp, err := c.doRequest(http.MethodGet, "/mm/version", nil)
	if err != nil {
		return "", err
	}

	type versionResponse struct {
		Version string `json:"version"`
	}
	result, err := parseResponse[versionResponse](resp)
	if err != nil {
		return "", err
	}
	return result.Version, nil
}

// GetInstalledModule retrieves git info for an installed module directory
func (c *MagicMirrorClient) GetInstalledModule(name string) (*InstalledModule, error) {
	resp, err := c.doRequest(http.MethodGet, "/modules/installed/"+name, nil)
	if err != nil {
		return nil, err
	}
	return parseResponse[InstalledModule](resp)
}

// EnsureInstalledModule converges a module install on the Pi
// (clone if missing, fetch + checkout version, npm install)
func (c *MagicMirrorClient) EnsureInstalledModule(name, repository, version string) (*InstalledModule, error) {
	body := map[string]string{
		"repository": repository,
		"version":    version,
	}
	resp, err := c.doRequest(http.MethodPut, "/modules/installed/"+name, body)
	if err != nil {
		return nil, err
	}
	return parseResponse[InstalledModule](resp)
}

// GetCanvas retrieves the singleton canvas configuration. The agent
// returns a default canvas if no document has been written yet, so this
// never returns "not found".
func (c *MagicMirrorClient) GetCanvas() (*CanvasConfig, error) {
	resp, err := c.doRequest(http.MethodGet, "/canvas/document", nil)
	if err != nil {
		return nil, err
	}
	type doc struct {
		Canvas CanvasConfig `json:"canvas"`
	}
	d, err := parseResponse[doc](resp)
	if err != nil {
		return nil, err
	}
	return &d.Canvas, nil
}

// UpdateCanvas writes the canvas globals. The agent rejects shrinking
// the canvas below existing slot bounds — that failure surfaces as an
// APIError with the offending slot named.
func (c *MagicMirrorClient) UpdateCanvas(canvas *CanvasConfig) (*CanvasConfig, error) {
	resp, err := c.doRequest(http.MethodPut, "/canvas", canvas)
	if err != nil {
		return nil, err
	}
	return parseResponse[CanvasConfig](resp)
}

// GetPage retrieves a named page. The agent returns 404 when missing,
// which surfaces as APIError so resource Read can detect drift.
func (c *MagicMirrorClient) GetPage(name string) (*Page, error) {
	resp, err := c.doRequest(http.MethodGet, "/pages/"+name, nil)
	if err != nil {
		return nil, err
	}
	return parseResponse[Page](resp)
}

// PutPage creates or replaces a named page. Slot validation runs on the
// agent; bad rects/overlap/unknown-module surface as APIError so the
// failure shows up in `terraform apply` with a usable message.
func (c *MagicMirrorClient) PutPage(name string, page *Page) (*Page, error) {
	resp, err := c.doRequest(http.MethodPut, "/pages/"+name, page)
	if err != nil {
		return nil, err
	}
	return parseResponse[Page](resp)
}

// DeletePage removes a named page. Returns nil even when the page was
// already gone — terraform destroys are idempotent-friendly.
func (c *MagicMirrorClient) DeletePage(name string) error {
	resp, err := c.doRequest(http.MethodDelete, "/pages/"+name, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		var apiErr APIError
		if err := json.Unmarshal(body, &apiErr); err != nil {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		}
		return &apiErr
	}
	return nil
}

// Restart restarts the Magic Mirror process
func (c *MagicMirrorClient) Restart() error {
	resp, err := c.doRequest(http.MethodPost, "/restart", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		var apiErr APIError
		if err := json.Unmarshal(body, &apiErr); err != nil {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		}
		return &apiErr
	}

	return nil
}
