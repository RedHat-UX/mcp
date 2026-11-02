// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

//lint:file-ignore SA1019 ROOTS-PROTOCOL: docs/PLAN.md section 11 requires a roots/elicitation protocol matrix; the SDK marks roots deprecated but keeps it functional.

package tools

import (
	"context"
	"errors"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Roots handshake values used when the proxy must ask the host client for its
// roots on protocol revisions at or after 2026-07-28, which forbid a
// server-initiated roots/list request.
const (
	rootsInputRequestKey    = "rhds-mcp/roots"
	rootsRequestState       = "rhds-mcp/roots"
	defaultInternalProtocol = "2025-11-25"
	mrtrProtocolVersion     = "2026-07-28"
)

// errMountClosed is returned when a proxied request arrives after Close.
var errMountClosed = errors.New("upstream mount is closed")

// clientForwarder sends every request through one long-lived internal client.
// cem's surface is bound to the process working directory, so one client is
// enough.
type clientForwarder struct {
	client *mcp.ClientSession
}

func (f *clientForwarder) callTool(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if f == nil || f.client == nil {
		return nil, errMountClosed
	}
	result, err := f.client.CallTool(ctx, forwardedToolParams(request))
	return result, preserveUpstreamError(err)
}

func (f *clientForwarder) readResource(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if f == nil || f.client == nil {
		return nil, errMountClosed
	}
	result, err := f.client.ReadResource(ctx, forwardedResourceParams(request))
	return result, preserveUpstreamError(err)
}

// preserveUpstreamError returns the JSON-RPC error inside an internal client
// failure unchanged. The SDK client wraps the wire error in `calling
// "<method>": ...`; forwarding that wrapper would make the host server wrap it
// a second time, so the client would see the method name twice. Returning the
// wire error lets the host produce exactly the response direct registration
// produces.
func preserveUpstreamError(err error) error {
	if err == nil {
		return nil
	}
	var wire *jsonrpc.Error
	if errors.As(err, &wire) && wire != nil {
		return wire
	}
	return err
}

// forwardedToolParams rebuilds a tools/call for the internal client. The
// progress token is forwarded so cancellation and progress stay correlated;
// host input responses are not forwarded because the proxy resolves roots
// itself.
func forwardedToolParams(request *mcp.CallToolRequest) *mcp.CallToolParams {
	params := new(mcp.CallToolParams)
	if request == nil || request.Params == nil {
		return params
	}
	params.Name = request.Params.Name
	params.Arguments = request.Params.Arguments
	if token := request.Params.GetProgressToken(); token != nil {
		params.SetProgressToken(token)
	}
	return params
}

// forwardedResourceParams rebuilds a resources/read for the internal client.
func forwardedResourceParams(request *mcp.ReadResourceRequest) *mcp.ReadResourceParams {
	params := new(mcp.ReadResourceParams)
	if request == nil || request.Params == nil {
		return params
	}
	params.URI = request.Params.URI
	if token := request.Params.GetProgressToken(); token != nil {
		params.SetProgressToken(token)
	}
	return params
}

// asimonimSession is the internal client scope for one host session.
type asimonimSession struct {
	mu        sync.Mutex
	client    *mcp.ClientSession
	server    *mcp.ServerSession
	closed    bool
	closeOnce sync.Once
}

func (s *asimonimSession) close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		client := s.client
		server := s.server
		s.client = nil
		s.server = nil
		s.mu.Unlock()

		if client != nil {
			_ = client.Close()
		}
		if server != nil {
			_ = server.Close()
		}
	})
}

// asimonimForwarder keeps one internal asimonim client per host session so
// asimonim's per-session workspace cache is keyed by the right roots. The
// internal server is shared: asimonim caches workspaces per internal server
// session, and each host session has its own internal client session.
type asimonimForwarder struct {
	state   *mountState
	server  *mcp.Server
	version string

	anonymousOnce   sync.Once
	anonymousClient *mcp.ClientSession
	anonymousServer *mcp.ServerSession
	anonymousErr    error
}

func (f *asimonimForwarder) callTool(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	client, needsInput, err := f.ensure(ctx, requestSession(request), toolInputResponses(request), toolRequestState(request))
	if err != nil {
		return nil, err
	}
	if needsInput {
		return rootsInputRequiredToolResult(), nil
	}
	result, err := client.CallTool(ctx, forwardedToolParams(request))
	return result, preserveUpstreamError(err)
}

func (f *asimonimForwarder) readResource(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	client, needsInput, err := f.ensure(ctx, resourceSession(request), resourceInputResponses(request), resourceRequestState(request))
	if err != nil {
		return nil, err
	}
	if needsInput {
		return rootsInputRequiredResourceResult(), nil
	}
	result, err := client.ReadResource(ctx, forwardedResourceParams(request))
	return result, preserveUpstreamError(err)
}

// ensure returns the internal client for the host session, creating it with
// that session's roots on first use. needsInput reports that the host client
// must answer a roots input request first.
func (f *asimonimForwarder) ensure(
	ctx context.Context,
	hostSession *mcp.ServerSession,
	responses mcp.InputResponseMap,
	state string,
) (*mcp.ClientSession, bool, error) {
	if hostSession == nil {
		client, err := f.anonymous(ctx)
		return client, false, err
	}

	f.state.mu.Lock()
	if f.state.closed {
		f.state.mu.Unlock()
		return nil, false, errMountClosed
	}
	session := f.state.sessions[hostSession]
	if session == nil {
		session = new(asimonimSession)
		f.state.sessions[hostSession] = session
		go f.reap(hostSession, session)
	}
	f.state.mu.Unlock()

	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil, false, errMountClosed
	}
	if session.client != nil {
		client := session.client
		session.mu.Unlock()
		return client, false, nil
	}
	session.mu.Unlock()

	roots, advertiseRoots, needsInput := f.rootsFor(ctx, hostSession, responses, state)
	if needsInput {
		return nil, true, nil
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil, false, errMountClosed
	}
	if session.client != nil {
		return session.client, false, nil
	}

	client, serverSession, err := connectInternalClient(
		ctx,
		f.server,
		"rhds-mcp-asimonim-proxy",
		hostProtocol(hostSession),
		roots,
		advertiseRoots,
	)
	if err != nil {
		return nil, false, err
	}
	session.client = client
	session.server = serverSession
	return client, false, nil
}

// reap closes a session's internal client once the host session ends.
func (f *asimonimForwarder) reap(hostSession *mcp.ServerSession, session *asimonimSession) {
	_ = hostSession.Wait()
	session.close()

	f.state.mu.Lock()
	if f.state.sessions[hostSession] == session {
		delete(f.state.sessions, hostSession)
	}
	f.state.mu.Unlock()
}

// rootsFor decides whether the host session's roots are available, must be
// requested, or should fall back to the working directory.
func (f *asimonimForwarder) rootsFor(
	ctx context.Context,
	hostSession *mcp.ServerSession,
	responses mcp.InputResponseMap,
	state string,
) ([]*mcp.Root, bool, bool) { //nolint:staticcheck // roots required by PLAN section 8.1
	if !hostAdvertisesRoots(hostSession) {
		// Match direct registration: a client without the roots capability
		// gets asimonim's cwd workspace and no roots round trip.
		return nil, false, false
	}
	if roots, ok := rootsFromResponses(responses); ok {
		return roots, true, false
	}
	if hostProtocol(hostSession) < mrtrProtocolVersion {
		// Before 2026-07-28 the server may issue a direct roots/list request,
		// which is what direct asimonim registration does. The input-request
		// path is not used here because the SDK rejects a resource result with
		// no contents before the legacy middleware can fulfill it.
		result, err := hostSession.ListRoots(ctx, nil) //nolint:staticcheck // roots required by PLAN section 8.1
		if err != nil || result == nil {
			return nil, false, false
		}
		return result.Roots, true, false
	}
	if state == rootsRequestState {
		// A retry without usable responses must not loop; fall back to cwd.
		return nil, false, false
	}
	return nil, true, true
}

// anonymous creates the fallback client used when a request has no session.
func (f *asimonimForwarder) anonymous(ctx context.Context) (*mcp.ClientSession, error) {
	f.anonymousOnce.Do(func() {
		f.state.mu.Lock()
		closed := f.state.closed
		f.state.mu.Unlock()
		if closed {
			f.anonymousErr = errMountClosed
			return
		}
		client, serverSession, err := connectInternalClient(
			ctx,
			f.server,
			"rhds-mcp-asimonim-proxy",
			defaultInternalProtocol,
			nil,
			false,
		)
		if err != nil {
			f.anonymousErr = err
			return
		}
		f.anonymousClient = client
		f.anonymousServer = serverSession
		f.state.own(client, serverSession)
	})
	return f.anonymousClient, f.anonymousErr
}

// hostAdvertisesRoots reports whether the host client advertised the roots
// capability during initialize.
func hostAdvertisesRoots(session *mcp.ServerSession) bool {
	if session == nil {
		return false
	}
	params := session.InitializeParams()
	if params == nil || params.Capabilities == nil {
		return false
	}
	return params.Capabilities.RootsV2 != nil //nolint:staticcheck // roots required by PLAN section 8.1
}

func hostProtocol(session *mcp.ServerSession) string {
	if session != nil {
		if params := session.InitializeParams(); params != nil && params.ProtocolVersion != "" {
			return params.ProtocolVersion
		}
	}
	return defaultInternalProtocol
}

// rootsFromResponses extracts the roots the host client supplied when it
// retried a call after a roots input request.
func rootsFromResponses(responses mcp.InputResponseMap) ([]*mcp.Root, bool) { //nolint:staticcheck // roots required by PLAN section 8.1
	if responses == nil {
		return nil, false
	}
	raw, ok := responses[rootsInputRequestKey]
	if !ok || raw == nil {
		return nil, false
	}
	result, ok := raw.(*mcp.ListRootsResult) //nolint:staticcheck // roots required by PLAN section 8.1
	if !ok || result == nil {
		return nil, false
	}
	return result.Roots, true
}

func rootsInputRequiredToolResult() *mcp.CallToolResult {
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{
			rootsInputRequestKey: &mcp.ListRootsParams{}, //nolint:staticcheck // roots required by PLAN section 8.1
		},
		RequestState: rootsRequestState,
	}
}

func rootsInputRequiredResourceResult() *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{
		InputRequests: mcp.InputRequestMap{
			rootsInputRequestKey: &mcp.ListRootsParams{}, //nolint:staticcheck // roots required by PLAN section 8.1
		},
		RequestState: rootsRequestState,
	}
}

func requestSession(request *mcp.CallToolRequest) *mcp.ServerSession {
	if request == nil {
		return nil
	}
	return request.Session
}

func resourceSession(request *mcp.ReadResourceRequest) *mcp.ServerSession {
	if request == nil {
		return nil
	}
	return request.Session
}

func toolInputResponses(request *mcp.CallToolRequest) mcp.InputResponseMap {
	if request == nil || request.Params == nil {
		return nil
	}
	return request.Params.InputResponses
}

func toolRequestState(request *mcp.CallToolRequest) string {
	if request == nil || request.Params == nil {
		return ""
	}
	return request.Params.RequestState
}

func resourceInputResponses(request *mcp.ReadResourceRequest) mcp.InputResponseMap {
	if request == nil || request.Params == nil {
		return nil
	}
	return request.Params.InputResponses
}

func resourceRequestState(request *mcp.ReadResourceRequest) string {
	if request == nil || request.Params == nil {
		return ""
	}
	return request.Params.RequestState
}
