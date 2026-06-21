package mascoteditor

import (
	"encoding/json"
	"image"
	"image/color"
	"strings"
	"testing"
)

func TestBuildAsepriteDocSlicesRows(t *testing.T) {
	req := sliceRequest{
		ID: "dog-brown", State: "default",
		Sheet: dimensions{W: 512, H: 432}, Cell: dimensions{W: 64, H: 48},
		Cols: 8, Rows: 9,
		Animations: []animationDef{
			{Tag: "idle", Row: 0, From: 0, To: 7, Duration: 150},
			{Tag: "barking-run", Row: 3, From: 0, To: 7, Duration: 90},
		},
	}
	data, err := buildAsepriteDoc(req)
	if err != nil {
		t.Fatalf("buildAsepriteDoc: %v", err)
	}
	var doc struct {
		Frames []struct {
			Frame struct{ X, Y, W, H int } `json:"frame"`
		} `json:"frames"`
		Meta struct {
			Image     string `json:"image"`
			FrameTags []struct {
				Name     string `json:"name"`
				From, To int
			} `json:"frameTags"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Frames) != 16 {
		t.Fatalf("want 16 frames, got %d", len(doc.Frames))
	}
	if doc.Meta.Image != "default.png" {
		t.Fatalf("meta.image = %q", doc.Meta.Image)
	}
	if len(doc.Meta.FrameTags) != 2 || doc.Meta.FrameTags[1].Name != "barking-run" {
		t.Fatalf("tags: %+v", doc.Meta.FrameTags)
	}
	// barking-run is row 3 → y = 3*48 = 144.
	if got := doc.Frames[doc.Meta.FrameTags[1].From].Frame.Y; got != 144 {
		t.Fatalf("barking-run first frame y = %d, want 144", got)
	}
}

func TestBuildAsepriteDocRejects(t *testing.T) {
	base := func() sliceRequest {
		return sliceRequest{
			ID: "d", State: "default",
			Sheet: dimensions{W: 512, H: 432}, Cell: dimensions{W: 64, H: 48}, Cols: 8, Rows: 9,
			Animations: []animationDef{{Tag: "idle", Row: 0, From: 0, To: 7, Duration: 150}},
		}
	}
	cases := map[string]func(*sliceRequest){
		"bad tiling":   func(r *sliceRequest) { r.Cols = 7 },
		"missing idle": func(r *sliceRequest) { r.Animations[0].Tag = "run" },
		"row OOB":      func(r *sliceRequest) { r.Animations[0].Row = 99 },
		"col OOB":      func(r *sliceRequest) { r.Animations[0].To = 99 },
		"no anims":     func(r *sliceRequest) { r.Animations = nil },
		"bad tag":      func(r *sliceRequest) { r.Animations[0].Tag = "has space" },
	}
	for name, mutate := range cases {
		req := base()
		mutate(&req)
		if _, err := buildAsepriteDoc(req); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestBuildAsepriteDocRejectsDuplicateTag(t *testing.T) {
	req := sliceRequest{
		ID: "d", State: "default",
		Sheet: dimensions{W: 128, H: 48}, Cell: dimensions{W: 64, H: 48}, Cols: 2, Rows: 1,
		Animations: []animationDef{
			{Tag: "idle", Row: 0, From: 0, To: 1, Duration: 150},
			{Tag: "idle", Row: 0, From: 0, To: 0, Duration: 150},
		},
	}
	if _, err := buildAsepriteDoc(req); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate-tag error, got %v", err)
	}
}

func TestDetectGridGapSeparated(t *testing.T) {
	// 10x4 image: two 5px-wide content bands separated/edged by transparent
	// gutters, one row band. Expect cols=2, rows=1, confident.
	img := image.NewNRGBA(image.Rect(0, 0, 10, 4))
	opaque := color.NRGBA{R: 200, G: 80, B: 80, A: 255}
	for _, xr := range [][2]int{{0, 3}, {5, 8}} {
		for x := xr[0]; x <= xr[1]; x++ {
			for y := 1; y <= 2; y++ {
				img.SetNRGBA(x, y, opaque)
			}
		}
	}
	g := detectGrid(img)
	if g.Cols != 2 || g.Rows != 1 {
		t.Fatalf("detectGrid cols/rows = %d/%d, want 2/1 (%+v)", g.Cols, g.Rows, g)
	}
	if !g.Confident || g.CellW != 5 || g.CellH != 4 {
		t.Fatalf("detectGrid = %+v, want confident 5x4", g)
	}
}

func TestDetectGridEmptyIsSafe(t *testing.T) {
	g := detectGrid(image.NewNRGBA(image.Rect(0, 0, 16, 16)))
	if g.Cols != 1 || g.Rows != 1 {
		t.Fatalf("empty image should fall back to 1x1, got %+v", g)
	}
}
