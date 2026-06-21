package mascoteditor

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

// Phase 2 of HOM-117: let the /mascot editor onboard a spritesheet end to
// end — upload the PNG, auto-detect its grid, slice rows into named
// animation tags, and export the result back to the repo. The agent
// writes into spritesDir, which is already inside the service's
// ReadWritePaths (it lives under MagicMirror/modules).

// spriteIDPattern guards the sprite-id and state path segments: kebab/snake
// only, must start alphanumeric. This is the sole defense against path
// traversal (no "..", no slashes) since these become directory and file
// names under spritesDir.
var spriteIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

const maxSheetBytes = 4 << 20 // 4 MB — sprite sheets are tiny pixel art

// registerImport mounts the Phase 2 upload/slice/export routes. Split from
// Register so the import surface only exists when a sprites dir is
// configured (writes are meaningless otherwise).
func (h *handlers) registerImport(router *gin.Engine) {
	router.POST("/mascot/api/sprites/upload", h.postUploadSheet)
	router.POST("/mascot/api/sprites/slice", h.postSliceSheet)
	router.GET("/mascot/api/sprites/:id/export", h.getExportSprite)
}

// uploadResponse reports the stored sheet's dimensions and a best-effort
// grid guess the editor pre-fills (the user can correct it).
type uploadResponse struct {
	ID     string     `json:"id"`
	State  string     `json:"state"`
	Sheet  dimensions `json:"sheet"`
	Detect gridGuess  `json:"detect"`
}

type dimensions struct {
	W int `json:"w"`
	H int `json:"h"`
}

type gridGuess struct {
	Cols      int  `json:"cols"`
	Rows      int  `json:"rows"`
	CellW     int  `json:"cellW"`
	CellH     int  `json:"cellH"`
	Confident bool `json:"confident"`
}

func (h *handlers) postUploadSheet(c *gin.Context) {
	if h.spritesDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no sprites directory configured"})
		return
	}
	id := strings.TrimSpace(c.PostForm("id"))
	state := stateOrDefault(c.PostForm("state"))
	if !spriteIDPattern.MatchString(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sprite id must be lowercase letters, digits, - or _ and start alphanumeric"})
		return
	}
	if !spriteIDPattern.MatchString(state) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "state name must be lowercase letters, digits, - or _ and start alphanumeric"})
		return
	}

	fileHeader, err := c.FormFile("sheet")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing 'sheet' file upload"})
		return
	}
	if fileHeader.Size > maxSheetBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("sheet exceeds %d MB limit", maxSheetBytes>>20)})
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "open upload: " + err.Error()})
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSheetBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read upload: " + err.Error()})
		return
	}

	// Decode strictly as PNG — the player and aesthetic both assume PNG, and
	// decoding here both validates the file and gives us dimensions + pixels
	// for grid auto-detect.
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not a valid PNG: " + err.Error()})
		return
	}

	pngPath := filepath.Join(h.spritesDir, id, state+".png")
	if err := writeAtomic(pngPath, data, 0o644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write sheet: " + err.Error()})
		return
	}

	b := img.Bounds()
	c.JSON(http.StatusOK, uploadResponse{
		ID:     id,
		State:  state,
		Sheet:  dimensions{W: b.Dx(), H: b.Dy()},
		Detect: detectGrid(img),
	})
}

// sliceRequest describes how to cut the already-uploaded sheet into an
// Aseprite "Array" JSON. The frames reference cells of the shared sheet —
// no pixels are moved (matches the dog-brown convention and the
// sheet-to-aseprite.mjs generator).
type sliceRequest struct {
	ID         string         `json:"id"`
	State      string         `json:"state"`
	Sheet      dimensions     `json:"sheet"`
	Cell       dimensions     `json:"cell"`
	Cols       int            `json:"cols"`
	Rows       int            `json:"rows"`
	Animations []animationDef `json:"animations"`
}

type animationDef struct {
	Tag      string `json:"tag"`
	Row      int    `json:"row"`
	From     int    `json:"from"`
	To       int    `json:"to"`
	Duration int    `json:"duration"`
}

type sliceResponse struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Frames int    `json:"frames"`
	Tags   int    `json:"tags"`
}

func (h *handlers) postSliceSheet(c *gin.Context) {
	if h.spritesDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no sprites directory configured"})
		return
	}
	var req sliceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
		return
	}
	req.State = stateOrDefault(req.State)
	if !spriteIDPattern.MatchString(req.ID) || !spriteIDPattern.MatchString(req.State) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id/state must be lowercase letters, digits, - or _ and start alphanumeric"})
		return
	}
	// The sheet must already be on disk (upload happens first). Refusing to
	// write a JSON that points at a missing PNG keeps the catalog honest.
	if _, err := os.Stat(filepath.Join(h.spritesDir, req.ID, req.State+".png")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "upload the sheet PNG before slicing"})
		return
	}

	doc, err := buildAsepriteDoc(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	jsonPath := filepath.Join(h.spritesDir, req.ID, req.State+".json")
	if err := writeAtomic(jsonPath, doc, 0o644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write json: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, sliceResponse{ID: req.ID, State: req.State, Frames: len(req.frames()), Tags: len(req.Animations)})
}

func (h *handlers) getExportSprite(c *gin.Context) {
	if h.spritesDir == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no sprites directory configured"})
		return
	}
	id := c.Param("id")
	if !spriteIDPattern.MatchString(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sprite id"})
		return
	}
	dir := filepath.Join(h.spritesDir, id)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "sprite not found"})
		return
	}

	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id+".zip"))
	zw := zip.NewWriter(c.Writer)
	defer zw.Close()
	entries, err := os.ReadDir(dir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		w, err := zw.Create(filepath.Join(id, e.Name()))
		if err != nil {
			return // header already sent; best we can do is truncate
		}
		src, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return
		}
		_, _ = io.Copy(w, src)
		src.Close()
	}
}

// ---- helpers ----

func stateOrDefault(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "default"
	}
	return s
}

// detectGrid guesses the cell grid by counting gap-separated content bands
// along each axis (the same heuristic used to onboard the Pixel Dogs pack).
// It's best-effort: tightly-packed sheets with no transparent gutters won't
// resolve into bands, so Confident is false and the editor asks the user to
// enter the grid. Returns cols=rows=1 as the safe fallback.
func detectGrid(img image.Image) gridGuess {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return gridGuess{Cols: 1, Rows: 1, CellW: w, CellH: h}
	}
	cols := countBands(w, func(x int) bool { return colEmpty(img, b.Min.X+x, b) })
	rows := countBands(h, func(y int) bool { return rowEmpty(img, b.Min.Y+y, b) })
	if cols == 0 {
		cols = 1
	}
	if rows == 0 {
		rows = 1
	}
	confident := w%cols == 0 && h%rows == 0
	return gridGuess{
		Cols:      cols,
		Rows:      rows,
		CellW:     w / cols,
		CellH:     h / rows,
		Confident: confident,
	}
}

// countBands counts runs of "non-empty" indices separated by empty ones.
func countBands(n int, empty func(i int) bool) int {
	bands, inBand := 0, false
	for i := 0; i < n; i++ {
		if empty(i) {
			inBand = false
		} else if !inBand {
			bands++
			inBand = true
		}
	}
	return bands
}

func colEmpty(img image.Image, x int, b image.Rectangle) bool {
	for y := b.Min.Y; y < b.Max.Y; y++ {
		if _, _, _, a := img.At(x, y).RGBA(); a != 0 {
			return false
		}
	}
	return true
}

func rowEmpty(img image.Image, y int, b image.Rectangle) bool {
	for x := b.Min.X; x < b.Max.X; x++ {
		if _, _, _, a := img.At(x, y).RGBA(); a != 0 {
			return false
		}
	}
	return true
}

// frames expands a slice request into its flat frame list (used only for
// the response count; buildAsepriteDoc rebuilds them for the JSON).
func (r sliceRequest) frames() []animationDef {
	out := make([]animationDef, 0)
	for _, an := range r.Animations {
		for c := an.From; c <= an.To; c++ {
			out = append(out, an)
		}
	}
	return out
}

var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// buildAsepriteDoc validates the slice request and renders the Aseprite
// "Array" JSON bytes. Mirrors sheet-to-aseprite.mjs so a sheet sliced in
// the editor is byte-compatible with one sliced on the CLI.
func buildAsepriteDoc(req sliceRequest) ([]byte, error) {
	if req.Cols <= 0 || req.Rows <= 0 {
		return nil, fmt.Errorf("cols and rows must be positive")
	}
	if req.Cell.W <= 0 || req.Cell.H <= 0 {
		return nil, fmt.Errorf("cell dimensions must be positive")
	}
	if req.Cols*req.Cell.W != req.Sheet.W || req.Rows*req.Cell.H != req.Sheet.H {
		return nil, fmt.Errorf("grid %dx%d of %dx%d cells does not tile the %dx%d sheet",
			req.Cols, req.Rows, req.Cell.W, req.Cell.H, req.Sheet.W, req.Sheet.H)
	}
	if len(req.Animations) == 0 {
		return nil, fmt.Errorf("define at least one animation")
	}

	type aseFrameRect struct {
		X int `json:"x"`
		Y int `json:"y"`
		W int `json:"w"`
		H int `json:"h"`
	}
	type aseFrame struct {
		Filename         string       `json:"filename"`
		Frame            aseFrameRect `json:"frame"`
		Rotated          bool         `json:"rotated"`
		Trimmed          bool         `json:"trimmed"`
		SpriteSourceSize aseFrameRect `json:"spriteSourceSize"`
		SourceSize       aseFrameRect `json:"sourceSize"`
		Duration         int          `json:"duration"`
	}
	type aseTag struct {
		Name      string `json:"name"`
		From      int    `json:"from"`
		To        int    `json:"to"`
		Direction string `json:"direction"`
	}

	var frames []aseFrame
	var tags []aseTag
	seenTags := map[string]struct{}{}
	hasIdle := false
	for _, an := range req.Animations {
		if !tagPattern.MatchString(an.Tag) {
			return nil, fmt.Errorf("animation tag %q must be letters, digits, - or _", an.Tag)
		}
		if _, dup := seenTags[an.Tag]; dup {
			return nil, fmt.Errorf("duplicate animation tag %q", an.Tag)
		}
		seenTags[an.Tag] = struct{}{}
		if an.Tag == "idle" {
			hasIdle = true
		}
		if an.Row < 0 || an.Row >= req.Rows {
			return nil, fmt.Errorf("animation %q: row %d out of range 0..%d", an.Tag, an.Row, req.Rows-1)
		}
		if an.From < 0 || an.To >= req.Cols || an.From > an.To {
			return nil, fmt.Errorf("animation %q: columns %d..%d out of range 0..%d", an.Tag, an.From, an.To, req.Cols-1)
		}
		dur := an.Duration
		if dur <= 0 {
			dur = 150
		}
		start := len(frames)
		for col := an.From; col <= an.To; col++ {
			rect := aseFrameRect{X: col * req.Cell.W, Y: an.Row * req.Cell.H, W: req.Cell.W, H: req.Cell.H}
			frames = append(frames, aseFrame{
				Filename:         fmt.Sprintf("%s_%d.png", an.Tag, col-an.From),
				Frame:            rect,
				SpriteSourceSize: aseFrameRect{W: req.Cell.W, H: req.Cell.H},
				SourceSize:       aseFrameRect{W: req.Cell.W, H: req.Cell.H},
				Duration:         dur,
			})
		}
		tags = append(tags, aseTag{Name: an.Tag, From: start, To: len(frames) - 1, Direction: "forward"})
	}
	// An "idle" tag is the contract for sprites without rotation (the module
	// plays it by default); require it so every sliced sheet is usable.
	if !hasIdle {
		return nil, fmt.Errorf("one animation must be named \"idle\" (the default pose)")
	}

	doc := map[string]any{
		"frames": frames,
		"meta": map[string]any{
			"app":       "magicmirror-agent/mascoteditor",
			"version":   "1.0",
			"image":     req.State + ".png",
			"format":    "RGBA8888",
			"size":      map[string]int{"w": req.Sheet.W, "h": req.Sheet.H},
			"scale":     "1",
			"frameTags": tags,
			"layers":    []map[string]any{{"name": "Layer", "opacity": 255, "blendMode": "normal"}},
			"slices":    []any{},
		},
	}
	// 2-space indent matches the store and hand-authored fixtures.
	return json.MarshalIndent(doc, "", "  ")
}
