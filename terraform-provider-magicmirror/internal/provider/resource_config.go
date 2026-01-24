package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ resource.Resource = &ConfigResource{}

// ConfigResource defines the resource implementation.
type ConfigResource struct {
	client *MagicMirrorClient
}

// ConfigResourceModel describes the resource data model.
type ConfigResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Address     types.String `tfsdk:"address"`
	Port        types.Int64  `tfsdk:"port"`
	BasePath    types.String `tfsdk:"base_path"`
	IPWhitelist types.List   `tfsdk:"ip_whitelist"`
	Language    types.String `tfsdk:"language"`
	Locale      types.String `tfsdk:"locale"`
	TimeFormat  types.Int64  `tfsdk:"time_format"`
	Units       types.String `tfsdk:"units"`
	Zoom        types.Float64 `tfsdk:"zoom"`
	ServerOnly  types.Bool   `tfsdk:"server_only"`
}

// NewConfigResource creates a new config resource
func NewConfigResource() resource.Resource {
	return &ConfigResource{}
}

func (r *ConfigResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_config"
}

func (r *ConfigResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the global Magic Mirror configuration. Only one config resource should exist per Magic Mirror instance.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Resource identifier (always 'global').",
				Computed:    true,
			},
			"address": schema.StringAttribute{
				Description: "The IP address to bind the Magic Mirror server to. Defaults to 'localhost'.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("localhost"),
			},
			"port": schema.Int64Attribute{
				Description: "The port to run the Magic Mirror server on. Defaults to 8080.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(8080),
			},
			"base_path": schema.StringAttribute{
				Description: "The base path for the Magic Mirror server.",
				Optional:    true,
			},
			"ip_whitelist": schema.ListAttribute{
				Description: "List of IP addresses allowed to access the Magic Mirror.",
				ElementType: types.StringType,
				Optional:    true,
			},
			"language": schema.StringAttribute{
				Description: "The language for Magic Mirror. Defaults to 'en'.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("en"),
			},
			"locale": schema.StringAttribute{
				Description: "The locale for date/time formatting.",
				Optional:    true,
			},
			"time_format": schema.Int64Attribute{
				Description: "Time format: 12 or 24 hour. Defaults to 24.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(24),
			},
			"units": schema.StringAttribute{
				Description: "Units for measurements: 'metric' or 'imperial'. Defaults to 'metric'.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("metric"),
			},
			"zoom": schema.Float64Attribute{
				Description: "Zoom factor for the display.",
				Optional:    true,
			},
			"server_only": schema.BoolAttribute{
				Description: "Run in server-only mode (no Electron window).",
				Optional:    true,
			},
		},
	}
}

func (r *ConfigResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*MagicMirrorClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *MagicMirrorClient, got: %T", req.ProviderData),
		)
		return
	}

	r.client = client
}

func (r *ConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ConfigResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config := r.modelToConfig(ctx, &data)

	tflog.Debug(ctx, "Updating global configuration")

	if err := r.client.UpdateConfig(config); err != nil {
		resp.Diagnostics.AddError("Failed to update configuration", err.Error())
		return
	}

	data.ID = types.StringValue("global")

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ConfigResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.client.GetConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read configuration", err.Error())
		return
	}

	r.configToModel(ctx, config, &data)
	data.ID = types.StringValue("global")

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data ConfigResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config := r.modelToConfig(ctx, &data)

	if err := r.client.UpdateConfig(config); err != nil {
		resp.Diagnostics.AddError("Failed to update configuration", err.Error())
		return
	}

	data.ID = types.StringValue("global")

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Config resource cannot be truly deleted; we just remove it from state
	// The Magic Mirror will continue to use its configuration
	tflog.Info(ctx, "Removing config from Terraform state (Magic Mirror configuration remains on device)")
}

// modelToConfig converts the Terraform model to an API config
func (r *ConfigResource) modelToConfig(ctx context.Context, data *ConfigResourceModel) *GlobalConfig {
	config := &GlobalConfig{
		Address:    data.Address.ValueString(),
		Port:       int(data.Port.ValueInt64()),
		BasePath:   data.BasePath.ValueString(),
		Language:   data.Language.ValueString(),
		Locale:     data.Locale.ValueString(),
		TimeFormat: int(data.TimeFormat.ValueInt64()),
		Units:      data.Units.ValueString(),
		ServerOnly: data.ServerOnly.ValueBool(),
	}

	if !data.Zoom.IsNull() {
		config.Zoom = data.Zoom.ValueFloat64()
	}

	if !data.IPWhitelist.IsNull() {
		var whitelist []string
		data.IPWhitelist.ElementsAs(ctx, &whitelist, false)
		config.IPWhitelist = whitelist
	}

	return config
}

// configToModel converts an API config to the Terraform model
func (r *ConfigResource) configToModel(ctx context.Context, config *GlobalConfig, data *ConfigResourceModel) {
	data.Address = types.StringValue(config.Address)
	data.Port = types.Int64Value(int64(config.Port))
	data.Language = types.StringValue(config.Language)
	data.TimeFormat = types.Int64Value(int64(config.TimeFormat))
	data.Units = types.StringValue(config.Units)

	if config.BasePath != "" {
		data.BasePath = types.StringValue(config.BasePath)
	}
	if config.Locale != "" {
		data.Locale = types.StringValue(config.Locale)
	}
	if config.Zoom != 0 {
		data.Zoom = types.Float64Value(config.Zoom)
	}
	if config.ServerOnly {
		data.ServerOnly = types.BoolValue(config.ServerOnly)
	}

	if len(config.IPWhitelist) > 0 {
		whitelist, _ := types.ListValueFrom(ctx, types.StringType, config.IPWhitelist)
		data.IPWhitelist = whitelist
	}
}
