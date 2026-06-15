package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &PageResource{}
var _ resource.ResourceWithImportState = &PageResource{}
var _ resource.ResourceWithValidateConfig = &PageResource{}

// PageResource is one named slot set in the Canvas v2 layout (HOM-104).
// Page identity is the `name` attribute, used as the path segment on
// the agent's /api/v1/pages/:name endpoints AND as the value the
// MMM-Canvas module receives in CANVAS_PAGE_CHANGE notifications.
type PageResource struct {
	client *MagicMirrorClient
}

type PageResourceModel struct {
	ID    types.String     `tfsdk:"id"`
	Name  types.String     `tfsdk:"name"`
	Slots []SlotModel      `tfsdk:"slots"`
}

// SlotModel mirrors client.Slot for Terraform's framework type system.
type SlotModel struct {
	Module types.String `tfsdk:"module"`
	X      types.Int64  `tfsdk:"x"`
	Y      types.Int64  `tfsdk:"y"`
	W      types.Int64  `tfsdk:"w"`
	H      types.Int64  `tfsdk:"h"`
	ZIndex types.Int64  `tfsdk:"z_index"`
	Hidden types.Bool   `tfsdk:"hidden"`
}

func NewPageResource() resource.Resource { return &PageResource{} }

func (r *PageResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_page"
}

func (r *PageResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A named slot set on the Canvas v2 layout (HOM-104). A page is the unit of display: only the active page's slots render. Pages replace the legacy MMM-Scenes2 scenes and the standalone fullscreen overlays (MMM-Chores, MMM-RecipeSage) — all become canvas pages.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Computed page ID — equal to the name attribute. Exposed so other resources can reference a page through state.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "Stable page name used to address this page. The CANVAS_PAGE_CHANGE notification carries this value as the target page.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"slots": schema.ListNestedAttribute{
				Description: "Ordered slot list. Each slot relocates a module's DOM wrapper into a rectangular region of the canvas. Removing a slot from a page does NOT delete the underlying magicmirror_module — registry decoupling per HOM-102.",
				Required:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"module": schema.StringAttribute{
							Description: "ID of the magicmirror_module to place. Reference via magicmirror_module.<name>.id so the slot tracks module renames through state.",
							Required:    true,
							Validators: []validator.String{
								stringvalidator.LengthAtLeast(1),
							},
						},
						"x": schema.Int64Attribute{
							Description: "Left edge of the slot, in pixels from the canvas origin.",
							Required:    true,
							Validators: []validator.Int64{
								int64validator.AtLeast(0),
							},
						},
						"y": schema.Int64Attribute{
							Description: "Top edge of the slot, in pixels from the canvas origin.",
							Required:    true,
							Validators: []validator.Int64{
								int64validator.AtLeast(0),
							},
						},
						"w": schema.Int64Attribute{
							Description: "Slot width in pixels. Must be positive.",
							Required:    true,
							Validators: []validator.Int64{
								int64validator.AtLeast(1),
							},
						},
						"h": schema.Int64Attribute{
							Description: "Slot height in pixels. Must be positive.",
							Required:    true,
							Validators: []validator.Int64{
								int64validator.AtLeast(1),
							},
						},
						"z_index": schema.Int64Attribute{
							Description: "Stacking order within the page. Higher values render on top. Defaults to 0.",
							Optional:    true,
							Computed:    true,
						},
						"hidden": schema.BoolAttribute{
							Description: "When true the slot reserves space but is not visible — useful for staging modules without removing them from the page. Hidden slots are exempt from overlap detection.",
							Optional:    true,
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (r *PageResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *PageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data PageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	page := r.modelToPage(&data)
	if _, err := r.client.PutPage(data.Name.ValueString(), page); err != nil {
		resp.Diagnostics.AddError("Failed to create page", err.Error())
		return
	}
	data.ID = data.Name
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data PageResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	page, err := r.client.GetPage(data.Name.ValueString())
	if err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.Message == "page not found" {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read page", err.Error())
		return
	}
	r.pageToModel(page, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data PageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	page := r.modelToPage(&data)
	if _, err := r.client.PutPage(data.Name.ValueString(), page); err != nil {
		resp.Diagnostics.AddError("Failed to update page", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data PageResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeletePage(data.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Failed to delete page", err.Error())
		return
	}
}

// ValidateConfig surfaces within-page slot overlap at plan time. The
// agent still rechecks at apply (it knows the canvas dimensions; the
// provider doesn't), but catching the common case here means the user
// sees the conflict before any state changes.
func (r *PageResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data PageResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	pairs := detectSlotOverlaps(data.Slots)
	for _, p := range pairs {
		resp.Diagnostics.AddAttributeError(
			path.Root("slots"),
			"Slots overlap within page",
			fmt.Sprintf("Slot %d (%s) and slot %d (%s) cover overlapping pixels. Mark one slot as hidden=true, or move/resize them so they don't intersect.",
				p.i, p.iModule, p.j, p.jModule),
		)
	}
}

type overlapPair struct {
	i, j             int
	iModule, jModule string
}

// detectSlotOverlaps returns every pair of visible slots within the
// list that share at least one pixel. Hidden slots are exempt — they
// reserve space conceptually but never collide. Edge adjacency (rect
// boundaries touching but not crossing) is allowed; the inequality is
// strict so two slots can sit side-by-side at the same column edge.
func detectSlotOverlaps(slots []SlotModel) []overlapPair {
	var out []overlapPair
	for i := 0; i < len(slots); i++ {
		if slots[i].Hidden.ValueBool() {
			continue
		}
		ax, ay := slots[i].X.ValueInt64(), slots[i].Y.ValueInt64()
		aw, ah := slots[i].W.ValueInt64(), slots[i].H.ValueInt64()
		for j := i + 1; j < len(slots); j++ {
			if slots[j].Hidden.ValueBool() {
				continue
			}
			bx, by := slots[j].X.ValueInt64(), slots[j].Y.ValueInt64()
			bw, bh := slots[j].W.ValueInt64(), slots[j].H.ValueInt64()
			if ax < bx+bw && bx < ax+aw && ay < by+bh && by < ay+ah {
				out = append(out, overlapPair{
					i: i, j: j,
					iModule: slots[i].Module.ValueString(),
					jModule: slots[j].Module.ValueString(),
				})
			}
		}
	}
	return out
}

func (r *PageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	page, err := r.client.GetPage(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to import page", err.Error())
		return
	}
	var data PageResourceModel
	data.ID = types.StringValue(req.ID)
	data.Name = types.StringValue(req.ID)
	r.pageToModel(page, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PageResource) modelToPage(data *PageResourceModel) *Page {
	slots := make([]Slot, 0, len(data.Slots))
	for _, s := range data.Slots {
		slots = append(slots, Slot{
			Module: s.Module.ValueString(),
			X:      int(s.X.ValueInt64()),
			Y:      int(s.Y.ValueInt64()),
			W:      int(s.W.ValueInt64()),
			H:      int(s.H.ValueInt64()),
			ZIndex: int(s.ZIndex.ValueInt64()),
			Hidden: s.Hidden.ValueBool(),
		})
	}
	return &Page{Slots: slots}
}

func (r *PageResource) pageToModel(page *Page, data *PageResourceModel) {
	slots := make([]SlotModel, 0, len(page.Slots))
	for _, s := range page.Slots {
		slots = append(slots, SlotModel{
			Module: types.StringValue(s.Module),
			X:      types.Int64Value(int64(s.X)),
			Y:      types.Int64Value(int64(s.Y)),
			W:      types.Int64Value(int64(s.W)),
			H:      types.Int64Value(int64(s.H)),
			ZIndex: types.Int64Value(int64(s.ZIndex)),
			Hidden: types.BoolValue(s.Hidden),
		})
	}
	data.Slots = slots
	data.ID = data.Name
}
