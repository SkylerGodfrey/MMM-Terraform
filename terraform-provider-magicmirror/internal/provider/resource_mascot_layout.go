package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &MascotLayoutResource{}

// MascotLayoutResource is the singleton MMM-Mascot sprite layout
// (HOM-124). Like CanvasResource, declaring more than one technically
// works but only the last apply's values stick — the agent's
// mascot.Store is singleton-backed.
//
// The resource bundles canvas + sprites + holidays in one document
// because the editor saves them atomically (HOM-123) and the IaC mirror
// (mascots.tf) is a single resource block. Splitting holidays into a
// sibling magicmirror_mascot_holiday resource would invite plan-time
// races where the calendar empties between applies — keeping them
// together avoids that class of bug.
type MascotLayoutResource struct {
	client *MagicMirrorClient
}

type MascotLayoutResourceModel struct {
	ID           types.String         `tfsdk:"id"`
	CanvasWidth  types.Int64          `tfsdk:"canvas_width"`
	CanvasHeight types.Int64          `tfsdk:"canvas_height"`
	Sprites      []MascotSpriteModel  `tfsdk:"sprite"`
	Holidays     []MascotHolidayModel `tfsdk:"holiday"`
}

type MascotSpriteModel struct {
	ID       types.String         `tfsdk:"id"`
	Sprite   types.String         `tfsdk:"sprite"`
	X        types.Int64          `tfsdk:"x"`
	Y        types.Int64          `tfsdk:"y"`
	W        types.Int64          `tfsdk:"w"`
	H        types.Int64          `tfsdk:"h"`
	Rotation *MascotRotationModel `tfsdk:"rotation"`
}

// MascotRotationModel is the optional per-sprite animation rotation
// (HOM-117). Absent (nil) means the sprite plays "idle".
type MascotRotationModel struct {
	Animations []types.String `tfsdk:"animations"`
	MinMs      types.Int64    `tfsdk:"min_ms"`
	MaxMs      types.Int64    `tfsdk:"max_ms"`
}

type MascotHolidayModel struct {
	State types.String `tfsdk:"state"`
	Start types.String `tfsdk:"start"`
	End   types.String `tfsdk:"end"`
}

func NewMascotLayoutResource() resource.Resource { return &MascotLayoutResource{} }

func (r *MascotLayoutResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mascot_layout"
}

func (r *MascotLayoutResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Sprite layout document for MMM-Mascot (HOM-117). Bundles canvas dimensions, placed sprites, and the holiday calendar that picks today's state.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Fixed singleton id; always 'mascot'.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"canvas_width": schema.Int64Attribute{
				Description: "Design-space canvas width in pixels. Default 1080.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(1080),
			},
			"canvas_height": schema.Int64Attribute{
				Description: "Design-space canvas height in pixels. Default 1780 (portrait Pi minus Scenes2 strip).",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(1780),
			},
		},
		Blocks: map[string]schema.Block{
			"sprite": schema.ListNestedBlock{
				Description: "One placed sprite. `sprite` is the catalog id (a directory under MMM-Mascot/sprites/); coordinates are in canvas design space.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"id":     schema.StringAttribute{Required: true, Description: "Stable id for this placement. Must be unique within the document."},
						"sprite": schema.StringAttribute{Required: true, Description: "Sprite catalog id (matches a directory under modules/MMM-Mascot/sprites/)."},
						"x":      schema.Int64Attribute{Required: true, Description: "X offset in canvas pixels (0..canvas_width)."},
						"y":      schema.Int64Attribute{Required: true, Description: "Y offset in canvas pixels (0..canvas_height)."},
						"w":      schema.Int64Attribute{Required: true, Description: "Width in canvas pixels."},
						"h":      schema.Int64Attribute{Required: true, Description: "Height in canvas pixels."},
					},
					Blocks: map[string]schema.Block{
						"rotation": schema.SingleNestedBlock{
							Description: "Optional animation rotation (HOM-117). When set, the sprite cycles the listed animation tags at random intervals instead of just playing 'idle'.",
							Attributes: map[string]schema.Attribute{
								"animations": schema.ListAttribute{
									ElementType: types.StringType,
									Optional:    true,
									Description: "Animation tag names to cycle through (must exist in the sprite's Aseprite JSON), e.g. [\"idle\", \"barking-run\"].",
								},
								"min_ms": schema.Int64Attribute{Optional: true, Description: "Shortest dwell on one animation before switching, in milliseconds."},
								"max_ms": schema.Int64Attribute{Optional: true, Description: "Longest dwell on one animation before switching, in milliseconds. Must be >= min_ms."},
							},
						},
					},
				},
			},
			"holiday": schema.ListNestedBlock{
				Description: "One date window where a non-default sprite state is active. Document order matters: the first window matching today wins.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"state": schema.StringAttribute{Required: true, Description: `State name (matches the spritesheet filename: state "halloween" loads <sprite>/halloween.png).`},
						"start": schema.StringAttribute{Required: true, Description: `Inclusive start date in MM-DD form, e.g. "10-15".`},
						"end":   schema.StringAttribute{Required: true, Description: `Inclusive end date in MM-DD form, e.g. "11-01". Must be >= start (no wrap-around in v0).`},
					},
				},
			},
		},
	}
}

func (r *MascotLayoutResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*MagicMirrorClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *MagicMirrorClient, got: %T", req.ProviderData))
		return
	}
	r.client = client
}

func (r *MascotLayoutResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data MascotLayoutResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	doc := r.modelToLayout(&data)
	updated, err := r.client.PutMascotLayout(doc)
	if err != nil {
		resp.Diagnostics.AddError("Failed to write mascot layout", err.Error())
		return
	}
	r.layoutToModel(updated, &data)
	data.ID = types.StringValue("mascot")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *MascotLayoutResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data MascotLayoutResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	doc, err := r.client.GetMascotLayout()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read mascot layout", err.Error())
		return
	}
	r.layoutToModel(doc, &data)
	data.ID = types.StringValue("mascot")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *MascotLayoutResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data MascotLayoutResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	doc := r.modelToLayout(&data)
	updated, err := r.client.PutMascotLayout(doc)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update mascot layout", err.Error())
		return
	}
	r.layoutToModel(updated, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *MascotLayoutResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Singleton-backed; "destroying" leaves the agent's defaults in place
	// (same stance as CanvasResource). We don't zero the document — that
	// would also wipe holidays which the user likely still wants.
}

func (r *MascotLayoutResource) modelToLayout(data *MascotLayoutResourceModel) *MascotLayout {
	sprites := make([]MascotSprite, 0, len(data.Sprites))
	for _, s := range data.Sprites {
		sprite := MascotSprite{
			ID:     s.ID.ValueString(),
			Sprite: s.Sprite.ValueString(),
			X:      int(s.X.ValueInt64()),
			Y:      int(s.Y.ValueInt64()),
			W:      int(s.W.ValueInt64()),
			H:      int(s.H.ValueInt64()),
		}
		if r := s.Rotation; r != nil {
			animations := make([]string, 0, len(r.Animations))
			for _, a := range r.Animations {
				animations = append(animations, a.ValueString())
			}
			sprite.Rotation = &MascotRotation{
				Animations: animations,
				MinMs:      int(r.MinMs.ValueInt64()),
				MaxMs:      int(r.MaxMs.ValueInt64()),
			}
		}
		sprites = append(sprites, sprite)
	}
	holidays := make([]MascotHoliday, 0, len(data.Holidays))
	for _, h := range data.Holidays {
		holidays = append(holidays, MascotHoliday{
			State: h.State.ValueString(),
			Start: h.Start.ValueString(),
			End:   h.End.ValueString(),
		})
	}
	return &MascotLayout{
		Canvas: MascotCanvas{
			Width:  int(data.CanvasWidth.ValueInt64()),
			Height: int(data.CanvasHeight.ValueInt64()),
		},
		Sprites:  sprites,
		Holidays: holidays,
	}
}

func (r *MascotLayoutResource) layoutToModel(doc *MascotLayout, data *MascotLayoutResourceModel) {
	data.CanvasWidth = types.Int64Value(int64(doc.Canvas.Width))
	data.CanvasHeight = types.Int64Value(int64(doc.Canvas.Height))
	sprites := make([]MascotSpriteModel, 0, len(doc.Sprites))
	for _, s := range doc.Sprites {
		model := MascotSpriteModel{
			ID:     types.StringValue(s.ID),
			Sprite: types.StringValue(s.Sprite),
			X:      types.Int64Value(int64(s.X)),
			Y:      types.Int64Value(int64(s.Y)),
			W:      types.Int64Value(int64(s.W)),
			H:      types.Int64Value(int64(s.H)),
		}
		if r := s.Rotation; r != nil {
			animations := make([]types.String, 0, len(r.Animations))
			for _, a := range r.Animations {
				animations = append(animations, types.StringValue(a))
			}
			model.Rotation = &MascotRotationModel{
				Animations: animations,
				MinMs:      types.Int64Value(int64(r.MinMs)),
				MaxMs:      types.Int64Value(int64(r.MaxMs)),
			}
		}
		sprites = append(sprites, model)
	}
	data.Sprites = sprites
	holidays := make([]MascotHolidayModel, 0, len(doc.Holidays))
	for _, h := range doc.Holidays {
		holidays = append(holidays, MascotHolidayModel{
			State: types.StringValue(h.State),
			Start: types.StringValue(h.Start),
			End:   types.StringValue(h.End),
		})
	}
	data.Holidays = holidays
}
