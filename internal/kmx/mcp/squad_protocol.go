package mcp

import (
	"errors"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func versionedError(err error, version int) *sdk.CallToolResult {
	result := errorResult(err)
	data := result.StructuredContent.(map[string]any)
	if version == 2 || version == 3 {
		data["schemaVersion"] = version
	}
	if version != 2 {
		return result
	}
	var safe *Error
	if errors.As(err, &safe) {
		switch safe.Code {
		case "squad_setup_failed", "unsupported_squad_program", "unsafe_squad_root", "squad_recovery_required", "invalid_squad_member", "invalid_squad_members", "invalid_squad_brief", "invalid_squad_starter", "invalid_squad_enrichment", "squad_starter_changed", "squad_enrichment_failed", "squad_cast_failed", "squad_registration_unknown", "squad_child_io_unknown", "squad_output_limit", "squad_effects_unknown", "squad_not_setup":
			data["reasonCode"] = safe.Code
			data["reason"] = safe.Error()
			result.Content = []sdk.Content{&sdk.TextContent{Text: safe.Error()}}
		}
	}
	return result
}
