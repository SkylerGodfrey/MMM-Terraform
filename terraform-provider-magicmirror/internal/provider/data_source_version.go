package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ datasource.DataSource = &VersionDataSource{}

// VersionDataSource exposes the core MagicMirror version (read-only).
type VersionDataSource struct {
	client *MagicMirrorClient
}

// VersionDataSourceModel describes the data source data model.
type VersionDataSourceModel struct {
	Version types.String `tfsdk:"version"`
}

// NewVersionDataSource creates a new version data source
func NewVersionDataSource() datasource.DataSource {
	return &VersionDataSource{}
}

func (d *VersionDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_version"
}

func (d *VersionDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the core MagicMirror version installed on the Pi. The core version is read-only; it cannot be managed by Terraform.",

		Attributes: map[string]schema.Attribute{
			"version": schema.StringAttribute{
				Description: "The MagicMirror version from package.json (e.g. '2.34.0').",
				Computed:    true,
			},
		},
	}
}

func (d *VersionDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*MagicMirrorClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected DataSource Configure Type",
			fmt.Sprintf("Expected *MagicMirrorClient, got: %T", req.ProviderData),
		)
		return
	}

	d.client = client
}

func (d *VersionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data VersionDataSourceModel

	version, err := d.client.GetMMVersion()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read MagicMirror version", err.Error())
		return
	}

	tflog.Debug(ctx, "Read MagicMirror version", map[string]any{"version": version})

	data.Version = types.StringValue(version)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
