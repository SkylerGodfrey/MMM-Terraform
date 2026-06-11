package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ resource.Resource = &ModuleResource{}
var _ resource.ResourceWithImportState = &ModuleResource{}

// ModuleResource defines the resource implementation.
type ModuleResource struct {
	client *MagicMirrorClient
}

// ModuleResourceModel describes the resource data model.
type ModuleResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Module     types.String `tfsdk:"module"`
	Position   types.String `tfsdk:"position"`
	Header     types.String `tfsdk:"header"`
	Disabled   types.Bool   `tfsdk:"disabled"`
	Classes    types.String `tfsdk:"classes"`
	Config     types.String `tfsdk:"config"` // JSON string for flexibility
	Repository types.String `tfsdk:"repository"`
	Version    types.String `tfsdk:"version"`
}

// NewModuleResource creates a new module resource
func NewModuleResource() resource.Resource {
	return &ModuleResource{}
}

func (r *ModuleResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_module"
}

func (r *ModuleResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Magic Mirror module configuration.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier for this module instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"module": schema.StringAttribute{
				Description: "The name of the Magic Mirror module (e.g., 'clock', 'weather', 'calendar').",
				Required:    true,
			},
			"position": schema.StringAttribute{
				Description: "The position on the mirror where the module will be displayed. Valid values: top_bar, top_left, top_center, top_right, upper_third, middle_center, lower_third, bottom_left, bottom_center, bottom_right, bottom_bar, fullscreen_above, fullscreen_below.",
				Optional:    true,
			},
			"header": schema.StringAttribute{
				Description: "Optional header text displayed above the module.",
				Optional:    true,
			},
			"disabled": schema.BoolAttribute{
				Description: "Whether the module is disabled. Defaults to false.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"classes": schema.StringAttribute{
				Description: "Additional CSS classes to apply to the module.",
				Optional:    true,
			},
			"config": schema.StringAttribute{
				Description: "Module-specific configuration as a JSON string. Use jsonencode() to convert HCL maps to JSON.",
				Optional:    true,
			},
			"repository": schema.StringAttribute{
				Description: "Git repository URL used to install the module on the Pi if it isn't already present. On destroy, the module directory is left in place; only the config entry is removed.",
				Optional:    true,
			},
			"version": schema.StringAttribute{
				Description: "Git ref (tag, branch, or commit SHA) the installed module should be checked out at. The agent runs git fetch + checkout, then npm install. On destroy, the module directory is left in place; only the config entry is removed.",
				Optional:    true,
			},
		},
	}
}

func (r *ModuleResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *ModuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ModuleResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.ensureInstalled(ctx, &data); err != nil {
		resp.Diagnostics.AddError("Failed to install module on the mirror", err.Error())
		return
	}

	module := r.modelToModule(&data)

	tflog.Debug(ctx, "Creating module", map[string]any{"module": module.Module})

	created, err := r.client.CreateModule(module)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create module", err.Error())
		return
	}

	data.ID = types.StringValue(created.ID)
	r.moduleToModel(created, &data)

	tflog.Debug(ctx, "Created module", map[string]any{"id": created.ID})

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ModuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ModuleResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	module, err := r.client.GetModule(data.ID.ValueString())
	if err != nil {
		// Check if it's a not found error
		if apiErr, ok := err.(*APIError); ok && apiErr.Message == "Module not found" {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read module", err.Error())
		return
	}

	r.moduleToModel(module, &data)

	if !data.Version.IsNull() && data.Version.ValueString() != "" {
		installed, err := r.client.GetInstalledModule(data.Module.ValueString())
		if err != nil {
			if apiErr, ok := err.(*APIError); ok && apiErr.Message == "module not installed" {
				// Module directory is gone — report drift
				data.Version = types.StringNull()
			} else {
				resp.Diagnostics.AddError("Failed to read installed module version", err.Error())
				return
			}
		} else if !versionMatches(data.Version.ValueString(), installed.Ref, installed.Commit) {
			actual := installed.Ref
			if actual == "" {
				actual = installed.Commit
			}
			tflog.Debug(ctx, "Installed module version drifted", map[string]any{
				"module":   data.Module.ValueString(),
				"declared": data.Version.ValueString(),
				"actual":   actual,
			})
			data.Version = types.StringValue(actual)
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ModuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data ModuleResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.ensureInstalled(ctx, &data); err != nil {
		resp.Diagnostics.AddError("Failed to install module on the mirror", err.Error())
		return
	}

	module := r.modelToModule(&data)

	updated, err := r.client.UpdateModule(module)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update module", err.Error())
		return
	}

	r.moduleToModel(updated, &data)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ModuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ModuleResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteModule(data.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Failed to delete module", err.Error())
		return
	}
}

func (r *ModuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	module, err := r.client.GetModule(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to import module", err.Error())
		return
	}

	var data ModuleResourceModel
	data.ID = types.StringValue(req.ID)
	r.moduleToModel(module, &data)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ensureInstalled converges the module install on the Pi before the
// config entry is written, so the module exists when MagicMirror restarts.
func (r *ModuleResource) ensureInstalled(ctx context.Context, data *ModuleResourceModel) error {
	if data.Repository.IsNull() && data.Version.IsNull() {
		return nil
	}

	name := data.Module.ValueString()
	tflog.Debug(ctx, "Converging installed module", map[string]any{
		"module":     name,
		"repository": data.Repository.ValueString(),
		"version":    data.Version.ValueString(),
	})

	_, err := r.client.EnsureInstalledModule(name, data.Repository.ValueString(), data.Version.ValueString())
	return err
}

// versionMatches reports whether the declared version matches what is
// installed: exact ref match, a prefix of the commit SHA, or a tag the
// ref builds on (git describe output like v1.4.6-1-gf51d88a).
func versionMatches(declared, ref, commit string) bool {
	if declared == "" {
		return true
	}
	if declared == ref {
		return true
	}
	if commit != "" && strings.HasPrefix(commit, declared) {
		return true
	}
	if ref != "" && strings.HasPrefix(ref, declared) {
		return true
	}
	return false
}

// modelToModule converts the Terraform model to an API module
func (r *ModuleResource) modelToModule(data *ModuleResourceModel) *Module {
	module := &Module{
		ID:       data.ID.ValueString(),
		Module:   data.Module.ValueString(),
		Position: data.Position.ValueString(),
		Header:   data.Header.ValueString(),
		Disabled: data.Disabled.ValueBool(),
		Classes:  data.Classes.ValueString(),
	}

	if !data.Config.IsNull() && data.Config.ValueString() != "" {
		var config map[string]any
		if err := json.Unmarshal([]byte(data.Config.ValueString()), &config); err == nil {
			module.Config = config
		}
	}

	return module
}

// moduleToModel converts an API module to the Terraform model
func (r *ModuleResource) moduleToModel(module *Module, data *ModuleResourceModel) {
	data.ID = types.StringValue(module.ID)
	data.Module = types.StringValue(module.Module)

	if module.Position != "" {
		data.Position = types.StringValue(module.Position)
	}
	if module.Header != "" {
		data.Header = types.StringValue(module.Header)
	}
	data.Disabled = types.BoolValue(module.Disabled)
	if module.Classes != "" {
		data.Classes = types.StringValue(module.Classes)
	}

	if len(module.Config) > 0 {
		configJSON, err := json.Marshal(module.Config)
		if err == nil {
			data.Config = types.StringValue(string(configJSON))
		}
	}
}
