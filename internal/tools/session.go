// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

//lint:file-ignore SA1019 ROOTS-PROTOCOL: docs/PLAN.md section 8.1 requires roots negotiation; the SDK marks the feature deprecated but keeps it functional during the SEP-2577 window.

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/resolve"
)

// Protocol versions relevant to server-to-client input. The dates sort
// lexicographically, which is exactly the comparison the SDK uses.
const (
	protocolVersion20250618 = "2025-06-18"
	protocolVersion20251125 = "2025-11-25"
	protocolVersion20260728 = "2026-07-28"
)

const (
	inputRootsID   = "roots"
	inputVersionID = "version"
)

// callEnv carries the negotiated protocol and client capabilities for one
// tool call. It is built from the request, never from server-side state.
type callEnv struct {
	session          *mcp.ServerSession
	protocolVersion  string
	rootsCapability  bool
	elicitCapability bool
	inputResponses   mcp.InputResponseMap
	requestState     string
}

func newCallEnv(request *mcp.CallToolRequest) *callEnv {
	if request == nil {
		return nil
	}
	env := &callEnv{
		inputResponses: request.Params.InputResponses,
		requestState:   request.Params.RequestState,
	}
	session, ok := request.GetSession().(*mcp.ServerSession)
	if !ok || session == nil {
		return env
	}
	env.session = session
	initialize := session.InitializeParams()
	if initialize == nil {
		return env
	}
	env.protocolVersion = initialize.ProtocolVersion
	if initialize.Capabilities != nil {
		// RootsV2 is the non-lossy representation the SDK fills during
		// initialize. Roots (the struct) cannot distinguish absent from empty.
		env.rootsCapability = initialize.Capabilities.RootsV2 != nil //nolint:staticcheck // roots required by PLAN
		env.elicitCapability = initialize.Capabilities.Elicitation != nil
	}
	return env
}

func (e *callEnv) supportsMRTR() bool {
	return e != nil && e.protocolVersion >= protocolVersion20260728
}

// requestState is carried opaquely through a multi-round-trip retry. It must
// be self-contained: a 2026-07-28 retry never depends on server memory.
type requestState struct {
	Stage string `json:"stage"`
	Root  string `json:"root,omitempty"`
}

func encodeRequestState(state requestState) string {
	encoded, err := json.Marshal(state)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeRequestState(raw string) (requestState, bool) {
	if raw == "" {
		return requestState{}, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return requestState{}, false
	}
	var state requestState
	if err := json.Unmarshal(decoded, &state); err != nil {
		return requestState{}, false
	}
	return state, true
}

func inputRequired(requests mcp.InputRequestMap, state requestState) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		InputRequests: requests,
		RequestState:  encodeRequestState(state),
	}
}

// acquireRoot returns the workspace root for a call. When the client declares
// roots, a 2026-07-28 call returns a ListRoots input request; older versions
// use ServerSession.ListRoots. Without the capability, cwd is used.
func (s *Service) acquireRoot(
	ctx context.Context,
	env *callEnv,
	state requestState,
) (root string, warning string, required *mcp.CallToolResult, failure *ToolError) {
	if state.Root != "" {
		if info, err := os.Stat(state.Root); err == nil && info.IsDir() {
			return state.Root, "", nil, nil
		}
	}
	if env == nil || !env.rootsCapability {
		return currentWorkingDirectory()
	}

	if env.supportsMRTR() {
		if response, ok := env.inputResponses[inputRootsID]; ok {
			result, ok := response.(*mcp.ListRootsResult) //nolint:staticcheck // roots required by PLAN
			if !ok {
				return currentWorkingDirectory()
			}
			if root, ok := firstFileRoot(result); ok {
				return root, "", nil, nil
			}
			root, warning, _, _ = currentWorkingDirectory()
			return root, warning + "; the client returned no file roots", nil, nil
		}
		state.Stage = "roots"
		return "", "", inputRequired(mcp.InputRequestMap{
			inputRootsID: &mcp.ListRootsParams{}, //nolint:staticcheck // roots required by PLAN
		}, state), nil
	}

	if env.session == nil {
		return currentWorkingDirectory()
	}
	result, err := env.session.ListRoots(ctx, &mcp.ListRootsParams{}) //nolint:staticcheck // roots required by PLAN
	if err != nil {
		root, warning, _, _ = currentWorkingDirectory()
		return root, warning + "; the roots request failed: " + err.Error(), nil, nil
	}
	if root, ok := firstFileRoot(result); ok {
		return root, "", nil, nil
	}
	root, warning, _, _ = currentWorkingDirectory()
	return root, warning + "; the client returned no file roots", nil, nil
}

func currentWorkingDirectory() (string, string, *mcp.CallToolResult, *ToolError) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", "", nil, &ToolError{
			Code:    "workspace_unavailable",
			Message: "could not determine the current working directory",
			Hint:    err.Error(),
		}
	}
	return workingDirectory, "workspace root unavailable; using the current working directory", nil, nil
}

func firstFileRoot(result *mcp.ListRootsResult) (string, bool) { //nolint:staticcheck // roots required by PLAN
	if result == nil {
		return "", false
	}
	for _, root := range result.Roots {
		if root == nil {
			continue
		}
		if path, ok := fileURIPath(root.URI); ok {
			return path, true
		}
	}
	return "", false
}

func fileURIPath(uri string) (string, bool) {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return "", false
	}
	path := parsed.Path
	if path == "" {
		path = parsed.Opaque
	}
	if path == "" {
		return "", false
	}
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return path, true
}

// resolveAmbiguity asks the client which major to use. It returns the chosen
// version, or an input-required result for a 2026-07-28 retry, or a structured
// error when elicitation is unavailable.
func (s *Service) resolveAmbiguity(
	ctx context.Context,
	env *callEnv,
	state requestState,
	ambiguity *resolve.Ambiguity,
) (string, *mcp.CallToolResult, *ToolError) {
	if ambiguity == nil {
		return "", nil, nil
	}
	offered := offeredVersions(ambiguity)
	if len(offered) == 0 {
		return "", nil, ambiguityError(ambiguity, "")
	}
	if env == nil || !env.elicitCapability || env.session == nil {
		return "", nil, ambiguityError(ambiguity, "")
	}

	params := elicitationParams(ambiguity, offered)
	if env.supportsMRTR() {
		if response, ok := env.inputResponses[inputVersionID]; ok {
			result, ok := response.(*mcp.ElicitResult)
			if !ok {
				return "", nil, invalidElicitationResponse(offered)
			}
			return chosenVersion(result, offered)
		}
		state.Stage = "elicit"
		return "", inputRequired(mcp.InputRequestMap{inputVersionID: params}, state), nil
	}

	result, err := env.session.Elicit(ctx, params)
	if err != nil {
		return "", nil, &ToolError{
			Code:    "elicitation_failed",
			Message: "could not ask the client to choose an @rhds/elements version",
			Hint:    err.Error(),
			Details: ambiguityDetails(ambiguity, ""),
		}
	}
	return chosenVersion(result, offered)
}

func offeredVersions(ambiguity *resolve.Ambiguity) []string {
	seen := make(map[string]bool)
	var versions []string
	for _, candidate := range ambiguity.Candidates {
		version := candidate.Version
		if parsed := resolve.ParseVersionSpec(version); parsed.OK {
			version = parsed.Text
		}
		if version == "" || seen[version] {
			continue
		}
		seen[version] = true
		versions = append(versions, version)
	}
	return versions
}

func elicitationParams(ambiguity *resolve.Ambiguity, offered []string) *mcp.ElicitParams {
	return &mcp.ElicitParams{
		Message: fmt.Sprintf(
			"Workspace sources resolve @rhds/elements to different majors (%v). Which version should this tool use?",
			ambiguity.Majors),
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"version": map[string]any{
					"type":        "string",
					"title":       "@rhds/elements version",
					"description": "A candidate version reported by the workspace",
					"enum":        offered,
				},
			},
			"required": []string{"version"},
		},
	}
}

func chosenVersion(result *mcp.ElicitResult, offered []string) (string, *mcp.CallToolResult, *ToolError) {
	if result == nil {
		return "", nil, invalidElicitationResponse(offered)
	}
	switch result.Action {
	case "accept":
	case "decline":
		return "", nil, &ToolError{
			Code:    "elicitation_declined",
			Message: "the client declined to choose an @rhds/elements version",
			Hint:    "pass the version argument to avoid the prompt",
		}
	case "cancel":
		return "", nil, &ToolError{
			Code:    "elicitation_cancelled",
			Message: "the client cancelled the @rhds/elements version prompt",
			Hint:    "pass the version argument to avoid the prompt",
		}
	default:
		return "", nil, invalidElicitationResponse(offered)
	}
	value, ok := result.Content["version"].(string)
	if !ok || value == "" {
		return "", nil, invalidElicitationResponse(offered)
	}
	for _, candidate := range offered {
		if value == candidate {
			return value, nil, nil
		}
	}
	return "", nil, invalidElicitationResponse(offered)
}

func invalidElicitationResponse(offered []string) *ToolError {
	return &ToolError{
		Code:    "invalid_elicitation_response",
		Message: "the client did not return one of the offered @rhds/elements versions",
		Hint:    "offered versions: " + strings.Join(offered, ", "),
	}
}

func ambiguityError(ambiguity *resolve.Ambiguity, versionArgument string) *ToolError {
	sources := make([]string, 0, len(ambiguity.Candidates))
	for _, candidate := range ambiguity.Candidates {
		sources = append(sources, fmt.Sprintf("%s from %s", candidate.Version, candidate.Source))
	}
	return &ToolError{
		Code: "ambiguous_version",
		Message: fmt.Sprintf(
			"workspace sources resolve @rhds/elements to different majors %v: %s",
			ambiguity.Majors, strings.Join(sources, "; ")),
		Hint: "pass the version argument, align the conflicting lockfile/" +
			"package.json/template sources so they agree on a major, or " +
			"configure the MCP client to provide a workspace root with a single major",
		Details: ambiguityDetails(ambiguity, versionArgument),
	}
}

func ambiguityDetails(ambiguity *resolve.Ambiguity, versionArgument string) map[string]any {
	return map[string]any{
		"candidates":      ambiguity.Candidates,
		"majors":          ambiguity.Majors,
		"versionArgument": versionArgument,
		"configOptions": []string{
			"version argument on the tool call",
			"align lockfile, package.json, and template references",
			"provide a workspace root that contains a single elements major",
		},
	}
}
