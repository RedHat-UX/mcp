// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

//lint:file-ignore SA1019 ROOTS-PROTOCOL: docs/PLAN.md section 8.1 requires roots negotiation; the SDK marks the feature deprecated but keeps it functional during the SEP-2577 window.

package tools

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

// maxRequestStateBytes bounds client-supplied retry state. The encoded state
// is a tiny JSON object plus a signature.
const maxRequestStateBytes = 4096

// stateSigningKey authenticates retry state for the lifetime of this process.
// A client must not be able to choose the workspace root by forging state.
var stateSigningKey = newStateSigningKey()

func newStateSigningKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("tools: cannot generate request state signing key: " + err.Error())
	}
	return key
}

func signRequestState(payload []byte) string {
	mac := hmac.New(sha256.New, stateSigningKey)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func encodeRequestState(state requestState) string {
	encoded, err := json.Marshal(state)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded) + "." + signRequestState(encoded)
}

func decodeRequestState(raw string) (requestState, bool) {
	if raw == "" || len(raw) > maxRequestStateBytes {
		return requestState{}, false
	}
	encodedPart, signature, ok := strings.Cut(raw, ".")
	if !ok {
		return requestState{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPart)
	if err != nil {
		return requestState{}, false
	}
	if !hmac.Equal([]byte(signature), []byte(signRequestState(payload))) {
		return requestState{}, false
	}
	var state requestState
	if err := json.Unmarshal(payload, &state); err != nil {
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
	// Retry state is client-supplied. decodeRequestState authenticates it, but
	// even authentic state only carries a root that came from the negotiated
	// roots flow. Accept it only for the elicitation retry of an MRTR-capable
	// session; never let state bypass roots negotiation on its own.
	if state.Root != "" && env != nil && env.rootsCapability &&
		env.supportsMRTR() && state.Stage == "elicit" {
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
	offered := offeredVersions(ambiguity, s.bundledElements())
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

// offeredVersions maps each ambiguity candidate to a bundled version the data
// tools can actually serve. Offering a raw range text such as "5" or an
// unbundled exact version would make the elicitation choice fail immediately.
func offeredVersions(ambiguity *resolve.Ambiguity, bundled []string) []string {
	seen := make(map[string]bool)
	var versions []string
	for _, candidate := range ambiguity.Candidates {
		version := bundledVersionFor(candidate, bundled)
		if version == "" || seen[version] {
			continue
		}
		seen[version] = true
		versions = append(versions, version)
	}
	return versions
}

func bundledVersionFor(candidate resolve.Candidate, bundled []string) string {
	for _, text := range []string{candidate.Version, candidate.Spec} {
		parsed := resolve.ParseVersionSpec(text)
		if !parsed.OK {
			continue
		}
		best := ""
		var bestParsed resolve.ParsedVersion
		for _, version := range bundled {
			available := resolve.ParseVersionSpec(version)
			if !available.OK || available.Major != parsed.Major {
				continue
			}
			if parsed.HasMinor && available.Minor != parsed.Minor {
				continue
			}
			if best == "" || resolve.CompareParsed(available, bestParsed) > 0 {
				best = available.Text
				bestParsed = available
			}
		}
		if best != "" {
			return best
		}
		if slices.Contains(bundled, text) {
			return text
		}
	}
	return ""
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
	sources := make([]string, 0, min(len(ambiguity.Candidates), 5))
	for i, candidate := range ambiguity.Candidates {
		if i >= 5 {
			sources = append(sources,
				fmt.Sprintf("and %d more candidates", len(ambiguity.Candidates)-5))
			break
		}
		sources = append(sources, fmt.Sprintf("%s from %s",
			truncateResponseText(candidate.Version, maxCandidateRunes),
			truncateResponseText(candidate.Source, 300)))
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
		"candidates":      boundedCandidates(ambiguity.Candidates, maxOutputCandidates),
		"majors":          ambiguity.Majors,
		"versionArgument": versionArgument,
		"configOptions": []string{
			"version argument on the tool call",
			"align lockfile, package.json, and template references",
			"provide a workspace root that contains a single elements major",
		},
	}
}
