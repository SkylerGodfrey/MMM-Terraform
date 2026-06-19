package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure MagicMirrorProvider satisfies various provider interfaces.
var _ provider.Provider = &MagicMirrorProvider{}

// MagicMirrorProvider defines the provider implementation.
type MagicMirrorProvider struct {
	version string
}

// MagicMirrorProviderModel describes the provider data model.
type MagicMirrorProviderModel struct {
	Host    types.String `tfsdk:"host"`
	Port    types.Int64  `tfsdk:"port"`
	APIKey  types.String `tfsdk:"api_key"`
	Timeout types.Int64  `tfsdk:"timeout"`
}

// New creates a new provider instance
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &MagicMirrorProvider{
			version: version,
		}
	}
}

func (p *MagicMirrorProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "magicmirror"
	resp.Version = p.version
}

func (p *MagicMirrorProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Terraform provider for managing Magic Mirror configuration via the Magic Mirror Agent API.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Description: "The hostname or IP address of the Magic Mirror Agent. Can also be set via MM_HOST environment variable.",
				Optional:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The port of the Magic Mirror Agent API. Defaults to 8484. Can also be set via MM_PORT environment variable.",
				Optional:    true,
			},
			"api_key": schema.StringAttribute{
				Description: "The API key for authenticating with the Magic Mirror Agent. Can also be set via MM_API_KEY environment variable.",
				Optional:    true,
				Sensitive:   true,
			},
			"timeout": schema.Int64Attribute{
				Description: "HTTP client timeout in seconds. Defaults to 30.",
				Optional:    true,
			},
		},
	}
}

func (p *MagicMirrorProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config MagicMirrorProviderModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Default values
	host := "localhost"
	port := int64(8484)
	apiKey := ""
	timeout := int64(30)

	if !config.Host.IsNull() {
		host = config.Host.ValueString()
	}
	if !config.Port.IsNull() {
		port = config.Port.ValueInt64()
	}
	if !config.APIKey.IsNull() {
		apiKey = config.APIKey.ValueString()
	}
	if !config.Timeout.IsNull() {
		timeout = config.Timeout.ValueInt64()
	}

	// Create API client
	client := &MagicMirrorClient{
		BaseURL: fmt.Sprintf("http://%s:%d/api/v1", host, port),
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			Timeout: time.Duration(timeout) * time.Second,
		},
	}

	// Make client available to resources and data sources
	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *MagicMirrorProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewModuleResource,
		NewConfigResource,
		NewCanvasResource,
		NewPageResource,
		NewMascotLayoutResource,
	}
}

func (p *MagicMirrorProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewVersionDataSource,
	}
}
