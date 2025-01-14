package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	cloudquery_api "github.com/cloudquery/cloudquery-api-go"
	"github.com/cloudquery/cloudquery/cli/internal/hub"
)

type PackageJSONV1 struct {
	Team             string                    `json:"team"`
	Name             string                    `json:"name"`
	Message          string                    `json:"message"`
	Version          string                    `json:"version"`
	Kind             cloudquery_api.PluginKind `json:"kind"`
	Protocols        []int                     `json:"protocols"`
	SupportedTargets []TargetBuild             `json:"supported_targets"`
	PackageType      string                    `json:"package_type"`
}

type TargetBuild struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Path     string `json:"path"`
	Checksum string `json:"checksum"`
}

func ReadPackageJSON(distDir string) (PackageJSONV1, error) {
	v := SchemaVersion{}
	b, err := os.ReadFile(filepath.Join(distDir, "package.json"))
	if err != nil {
		return PackageJSONV1{}, err
	}
	err = json.Unmarshal(b, &v)
	if err != nil {
		return PackageJSONV1{}, err
	}
	if v.SchemaVersion != 1 {
		return PackageJSONV1{}, errors.New("unsupported schema version. This CLI version only supports package.json v1. Try upgrading your CloudQuery CLI version")
	}
	pkgJSON := PackageJSONV1{}
	err = json.Unmarshal(b, &pkgJSON)
	if err != nil {
		return PackageJSONV1{}, err
	}
	return pkgJSON, nil
}

func UploadPluginDocs(ctx context.Context, c *cloudquery_api.ClientWithResponses, teamName, pluginKind, pluginName, version, docsDir string, replace bool) error {
	dirEntries, err := os.ReadDir(docsDir)
	if err != nil {
		return fmt.Errorf("failed to read docs directory: %w", err)
	}

	pages := make([]cloudquery_api.PluginDocsPageCreate, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() {
			continue
		}
		fileExt := filepath.Ext(dirEntry.Name())
		if fileExt != ".md" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(docsDir, dirEntry.Name()))
		if err != nil {
			return fmt.Errorf("failed to read docs file: %w", err)
		}
		contentStr := hub.NormalizeContent(string(content))
		pages = append(pages, cloudquery_api.PluginDocsPageCreate{
			Content: contentStr,
			Name:    strings.TrimSuffix(dirEntry.Name(), fileExt),
		})
	}

	if replace {
		body := cloudquery_api.ReplacePluginVersionDocsJSONRequestBody{
			Pages: pages,
		}
		resp, err := c.ReplacePluginVersionDocsWithResponse(ctx, teamName, cloudquery_api.PluginKind(pluginKind), pluginName, version, body)
		if err != nil {
			return fmt.Errorf("failed to upload docs: %w", err)
		}
		if resp.HTTPResponse.StatusCode > 299 {
			return hub.ErrorFromHTTPResponse(resp.HTTPResponse, resp)
		}
	} else {
		body := cloudquery_api.CreatePluginVersionDocsJSONRequestBody{
			Pages: pages,
		}
		resp, err := c.CreatePluginVersionDocsWithResponse(ctx, teamName, cloudquery_api.PluginKind(pluginKind), pluginName, version, body)
		if err != nil {
			return fmt.Errorf("failed to upload docs: %w", err)
		}
		if resp.HTTPResponse.StatusCode > 299 {
			return hub.ErrorFromHTTPResponse(resp.HTTPResponse, resp)
		}
	}

	return nil
}

func CreateNewPluginDraftVersion(ctx context.Context, c *cloudquery_api.ClientWithResponses, teamName, pluginName string, pkgJSON PackageJSONV1) error {
	targets := make([]string, len(pkgJSON.SupportedTargets))
	checksums := make([]string, len(pkgJSON.SupportedTargets))
	for i, t := range pkgJSON.SupportedTargets {
		targets[i] = fmt.Sprintf("%s_%s", t.OS, t.Arch)
		checksums[i] = strings.TrimPrefix(t.Checksum, "sha256:")
	}

	body := cloudquery_api.CreatePluginVersionJSONRequestBody{
		Message:          pkgJSON.Message,
		PackageType:      cloudquery_api.CreatePluginVersionJSONBodyPackageType(pkgJSON.PackageType),
		Protocols:        pkgJSON.Protocols,
		SupportedTargets: targets,
		Checksums:        checksums,
	}
	resp, err := c.CreatePluginVersionWithResponse(ctx, teamName, pkgJSON.Kind, pluginName, pkgJSON.Version, body)
	if err != nil {
		return fmt.Errorf("failed to create plugin version: %w", err)
	}
	if resp.HTTPResponse.StatusCode > 299 {
		err := hub.ErrorFromHTTPResponse(resp.HTTPResponse, resp)
		if resp.HTTPResponse.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w. Hint: You may need to create the plugin first", err)
		}
		return err
	}
	return nil
}

func UploadTableSchemas(ctx context.Context, c *cloudquery_api.ClientWithResponses, teamName, pluginName, tablesJSONPath string, pkgJSON PackageJSONV1) error {
	b, err := os.ReadFile(tablesJSONPath)
	if err != nil {
		return fmt.Errorf("failed to read tables.json: %w", err)
	}
	tables := make([]cloudquery_api.PluginTableCreate, 0)
	err = json.Unmarshal(b, &tables)
	if err != nil {
		return fmt.Errorf("failed to parse tables.json: %w", err)
	}
	body := cloudquery_api.CreatePluginVersionTablesJSONRequestBody{
		Tables: tables,
	}
	resp, err := c.CreatePluginVersionTablesWithResponse(ctx, teamName, pkgJSON.Kind, pluginName, pkgJSON.Version, body)
	if err != nil {
		return fmt.Errorf("failed to upload table schemas: %w", err)
	}
	if resp.HTTPResponse.StatusCode > 299 {
		return hub.ErrorFromHTTPResponse(resp.HTTPResponse, resp)
	}
	return nil
}

func UploadPluginBinary(ctx context.Context, c *cloudquery_api.ClientWithResponses, teamName, pluginName, goos, goarch, localPath string, pkgJSON PackageJSONV1) error {
	target := goos + "_" + goarch
	resp, err := c.UploadPluginAssetWithResponse(ctx, teamName, pkgJSON.Kind, pluginName, pkgJSON.Version, target)
	if err != nil {
		return fmt.Errorf("failed to upload binary: %w", err)
	}
	if resp.HTTPResponse.StatusCode > 299 {
		msg := fmt.Sprintf("failed to upload binary: %s", resp.HTTPResponse.Status)
		switch {
		case resp.JSON403 != nil:
			msg = fmt.Sprintf("%s: %s", msg, resp.JSON403.Message)
		case resp.JSON401 != nil:
			msg = fmt.Sprintf("%s: %s", msg, resp.JSON401.Message)
		}
		return fmt.Errorf(msg)
	}
	if resp.JSON201 == nil {
		return fmt.Errorf("upload response is nil, failed to upload binary")
	}
	uploadURL := resp.JSON201.Url
	err = hub.UploadFile(uploadURL, localPath)
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}
	return nil
}
