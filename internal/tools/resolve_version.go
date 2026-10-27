// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-ux/mcp/internal/resolve"
)

// ResolveVersionInput is the input for rhds_resolve_version.
type ResolveVersionInput struct {
	Version       string `json:"version,omitempty" jsonschema:"Explicit @rhds/elements version; skips workspace resolution"`
	TokensVersion string `json:"tokensVersion,omitempty" jsonschema:"Explicit @rhds/tokens version; overrides the coupled version with a warning"`
}

// ResolveVersionOutput is the output for rhds_resolve_version.
type ResolveVersionOutput struct {
	Resolved   ResolvedVersion     `json:"resolved"`
	Candidates []resolve.Candidate `json:"candidates"`
	Ambiguity  *resolve.Ambiguity  `json:"ambiguity,omitempty"`
	Error      *ToolError          `json:"error,omitempty"`
}

// ResolveVersion implements rhds_resolve_version for direct service calls.
// It reads only the index and, when an explicit version is absent, the
// workspace.
func (s *Service) ResolveVersion(_ context.Context, input ResolveVersionInput) ResolveVersionOutput {
	return s.resolveVersionInWorkspace("", input)
}

// ResolveVersionInWorkspace resolves against a specific workspace root. It is
// used by tests and by callers that already know the root.
func (s *Service) ResolveVersionInWorkspace(root string, input ResolveVersionInput) ResolveVersionOutput {
	return s.resolveVersionInWorkspace(root, input)
}

func (s *Service) resolveVersionInWorkspace(root string, input ResolveVersionInput) ResolveVersionOutput {
	if failure := s.dataFailure(); failure != nil {
		return ResolveVersionOutput{Candidates: []resolve.Candidate{}, Error: failure}
	}
	result, err := resolve.Resolve(resolve.Options{
		Root:            root,
		Explicit:        input.Version,
		BundledElements: s.bundledElements(),
		Limits:          resolve.DefaultLimits(),
	})
	if err != nil {
		var failure *ToolError
		if err == resolve.ErrNoBundledElements {
			failure = &ToolError{
				Code:    "artifact_not_found",
				Message: "no bundled elements data",
				Hint:    "build with generated data: make data build",
			}
		} else {
			failure = &ToolError{
				Code:    "workspace_unavailable",
				Message: "could not inspect the workspace for an @rhds/elements version",
				Hint:    err.Error(),
			}
		}
		return ResolveVersionOutput{Candidates: []resolve.Candidate{}, Error: failure}
	}
	if result.Ambiguity != nil {
		return ResolveVersionOutput{
			Candidates: result.Candidates,
			Ambiguity:  result.Ambiguity,
			Error:      ambiguityError(result.Ambiguity, input.Version),
		}
	}
	resolved, failure := s.resolutionFrom(result, input.TokensVersion, false)
	if failure != nil {
		return ResolveVersionOutput{
			Candidates: result.Candidates,
			Ambiguity:  result.Ambiguity,
			Error:      failure,
		}
	}
	return ResolveVersionOutput{
		Resolved:   resolved.Resolved,
		Candidates: resolved.Candidates,
		Ambiguity:  resolved.Ambiguity,
	}
}

// resolveVersionWithEnv implements rhds_resolve_version for an MCP call. A
// 2026-07-28 ambiguity returns an input-required result; older protocols use
// ServerSession.Elicit.
func (s *Service) resolveVersionWithEnv(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input ResolveVersionInput,
) (*mcp.CallToolResult, ResolveVersionOutput) {
	resolved, required, failure := s.prepareResolution(
		ctx, newCallEnv(request), input.Version, input.TokensVersion, false)
	if required != nil {
		return required, ResolveVersionOutput{}
	}
	if failure != nil {
		candidates := resolved.Candidates
		if candidates == nil {
			candidates = []resolve.Candidate{}
		}
		return nil, ResolveVersionOutput{
			Candidates: candidates,
			Ambiguity:  resolved.Ambiguity,
			Error:      failure,
		}
	}
	return nil, ResolveVersionOutput{
		Resolved:   resolved.Resolved,
		Candidates: resolved.Candidates,
		Ambiguity:  resolved.Ambiguity,
	}
}
