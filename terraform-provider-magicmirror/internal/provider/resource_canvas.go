package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &CanvasResource{}

// CanvasResource is the singleton Canvas v2 globals (HOM-104). Declaring
// more than one is technically allowed but only the last apply's values
// take effect — the agent storage is singleton-backed by design.
type CanvasResource struct {
	client *MagicMirrorClient
}

// CanvasResourceModel matches the singleton in the agent's canvas store.
type CanvasResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Width        types.Int64  `tfsdk:"width"`
	Height       types.Int64  `tfsdk:"height"`
	DebugBorders types.Bool   `tfsdk:"debug_borders"`
	DebugLabels  types.Bool   `tfsdk:"debug_labels"`
	DefaultPage  types.String `tfsdk:"default_page"`
}

func NewCanvasResource() resource.Resource { return &CanvasResource{} }

func (r *CanvasResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_canvas"
}

func (r *CanvasResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Singleton globals for the Canvas v2 layout surface (HOM-104). Defines the canvas pixel dimensions, the page rendered on boot, and debug border behaviour.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Fixed singleton id; always 'canvas'.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"width": schema.Int64Attribute{
				Description: "Canvas width in pixels. Default 1080 (portrait Pi).",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(1080),
			},
			"height": schema.Int64Attribute{
				Description: "Canvas height in pixels. Default 1780 (portrait Pi minus the 140 px Scenes2 strip).",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(1780),
			},
			"debug_borders": schema.BoolAttribute{
				Description: "Render a per-module colored outline around every visible slot for layout debugging on the mirror. Runtime-toggleable via the CANVAS_DEBUG_TOGGLE notification (HOM-106).",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"debug_labels": schema.BoolAttribute{
				Description: "When debug_borders is on, also render a corner label with the module identifier and slot dimensions.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"default_page": schema.StringAttribute{
				Description: "Name of the page rendered on boot or when no other page is active.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("home"),
			},
		},
	}
}

func (r *CanvasResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *CanvasResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data CanvasResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfg := r.modelToCanvas(&data)
	if _, err := r.client.UpdateCanvas(cfg); err != nil {
		resp.Diagnostics.AddError("Failed to write canvas", err.Error())
		return
	}
	data.ID = types.StringValue("canvas")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *CanvasResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data CanvasResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfg, err := r.client.GetCanvas()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read canvas", err.Error())
		return
	}
	r.canvasToModel(cfg, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *CanvasResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data CanvasResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfg := r.modelToCanvas(&data)
	if _, err := r.client.UpdateCanvas(cfg); err != nil {
		resp.Diagnostics.AddError("Failed to update canvas", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *CanvasResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// The canvas is singleton-backed; "destroying" the resource leaves
	// the agent's defaults in place. We don't write a zero document.
}

func (r *CanvasResource) modelToCanvas(data *CanvasResourceModel) *CanvasConfig {
	return &CanvasConfig{
		Width:        int(data.Width.ValueInt64()),
		Height:       int(data.Height.ValueInt64()),
		DebugBorders: data.DebugBorders.ValueBool(),
		DebugLabels:  data.DebugLabels.ValueBool(),
		DefaultPage:  data.DefaultPage.ValueString(),
	}
}

func (r *CanvasResource) canvasToModel(cfg *CanvasConfig, data *CanvasResourceModel) {
	data.ID = types.StringValue("canvas")
	data.Width = types.Int64Value(int64(cfg.Width))
	data.Height = types.Int64Value(int64(cfg.Height))
	data.DebugBorders = types.BoolValue(cfg.DebugBorders)
	data.DebugLabels = types.BoolValue(cfg.DebugLabels)
	data.DefaultPage = types.StringValue(cfg.DefaultPage)
}
