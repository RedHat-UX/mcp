// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

//lint:file-ignore SA1019 ROOTS-PROTOCOL: docs/PLAN.md section 11 requires a roots/elicitation protocol matrix; the SDK marks roots deprecated but keeps it functional.

package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

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
	relay  *progressRelay
}

// progressBarrierTokenBase prefixes the internal drain notification sent from
// the internal server after a forwarded call returns. The internal client
// dispatches inbound messages in order, so once the relay sees the barrier,
// every progress notification enqueued before the call's response has been
// relayed. Each relay adds a random nonce so a caller-chosen progress token
// cannot predict or squat on a barrier token.
const (
	progressBarrierTokenBase = "rhds-mcp/progress-barrier/"
	progressBarrierTimeout   = time.Second
)

// progressRelay maps an in-flight request's progress token to the host session
// that should receive the upstream's progress notifications. Interposed
// sessions (for example a shared cem client) use the same relay, so a token
// registered by more than one session is ambiguous and is not relayed; this
// prevents one client's progress from reaching another. A nil relay is valid
// and drops every notification.
type progressRelay struct {
	mu            sync.Mutex
	sessions      map[any]map[*mcp.ServerSession]int
	barriers      map[string]chan struct{}
	barrierPrefix string
	nextBarrier   uint64
	server        *mcp.ServerSession
}

// newProgressRelay returns an empty relay.
func newProgressRelay() *progressRelay {
	prefix := progressBarrierTokenBase
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err == nil {
		prefix += hex.EncodeToString(nonce[:]) + "/"
	} else {
		prefix += "local/"
	}
	return &progressRelay{
		sessions:      make(map[any]map[*mcp.ServerSession]int),
		barriers:      make(map[string]chan struct{}),
		barrierPrefix: prefix,
	}
}

// setServer records the internal server session used to drain notifications.
func (r *progressRelay) setServer(server *mcp.ServerSession) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.server = server
	r.mu.Unlock()
}

// register associates token with the host session while a call is in flight.
// Multiple calls may register the same token for one session; the count keeps
// each release from deleting another in-flight call's mapping.
func (r *progressRelay) register(token any, session *mcp.ServerSession) {
	if r == nil || session == nil {
		return
	}
	key, ok := progressTokenKey(token)
	if !ok {
		return
	}
	r.mu.Lock()
	sessions := r.sessions[key]
	if sessions == nil {
		sessions = make(map[*mcp.ServerSession]int)
		r.sessions[key] = sessions
	}
	sessions[session]++
	r.mu.Unlock()
}

// release removes this call's registration and waits until every notification
// the internal client had queued before the call's response has been
// dispatched. Without the drain, the response can be dispatched before the
// progress notification that preceded it on the wire, and deleting first would
// silently drop it.
func (r *progressRelay) release(ctx context.Context, token any, session *mcp.ServerSession) {
	if r == nil || session == nil {
		return
	}
	key, ok := progressTokenKey(token)
	if !ok {
		return
	}
	r.drain(ctx)
	r.mu.Lock()
	if sessions := r.sessions[key]; sessions != nil {
		if count := sessions[session]; count > 1 {
			sessions[session] = count - 1
		} else {
			delete(sessions, session)
		}
		if len(sessions) == 0 {
			delete(r.sessions, key)
		}
	}
	r.mu.Unlock()
}

// drain sends an internal barrier notification and waits for the relay to
// observe it. The barrier is enqueued after the call's response, so it is
// processed after every notification that arrived before that response.
func (r *progressRelay) drain(ctx context.Context) {
	r.mu.Lock()
	server := r.server
	if server == nil {
		r.mu.Unlock()
		return
	}
	r.nextBarrier++
	token := r.barrierPrefix + strconv.FormatUint(r.nextBarrier, 10)
	drained := make(chan struct{})
	r.barriers[token] = drained
	r.mu.Unlock()

	// The barrier exists to deliver notifications the caller already caused;
	// caller cancellation must not bypass it. The timer still bounds the wait.
	drainCtx := context.WithoutCancel(ctx)
	if err := server.NotifyProgress(drainCtx, &mcp.ProgressNotificationParams{ProgressToken: token}); err != nil {
		r.forgetBarrier(token)
		return
	}

	timer := time.NewTimer(progressBarrierTimeout)
	defer timer.Stop()
	select {
	case <-drained:
	case <-timer.C:
		r.forgetBarrier(token)
		slog.Debug("upstream proxy: timed out draining progress notifications",
			"timeout", progressBarrierTimeout)
	}
}

func (r *progressRelay) forgetBarrier(token string) {
	r.mu.Lock()
	delete(r.barriers, token)
	r.mu.Unlock()
}

// progressTokenKey admits only the token types the SDK and JSON can produce,
// so an unexpected shape in caller _meta cannot panic a map lookup.
func progressTokenKey(token any) (any, bool) {
	switch token.(type) {
	case string, int, int32, int64, float64:
		return token, true
	default:
		return nil, false
	}
}

// target returns the single host session registered for token. A token
// registered by more than one session (a legal reuse across client sessions
// that share one internal client) is ambiguous, so it has no target and its
// notifications are dropped rather than delivered to the wrong client.
func (r *progressRelay) target(token any) *mcp.ServerSession {
	if r == nil {
		return nil
	}
	key, ok := progressTokenKey(token)
	if !ok {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sessions := r.sessions[key]
	if len(sessions) != 1 {
		return nil
	}
	for session := range sessions {
		return session
	}
	return nil
}

// takeBarrier consumes token if it is an active drain barrier. Membership, not
// the token's prefix, decides: a caller-chosen token that merely resembles a
// barrier is relayed normally.
func (r *progressRelay) takeBarrier(token any) (chan struct{}, bool) {
	if r == nil {
		return nil, false
	}
	value, ok := token.(string)
	if !ok {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	drained, ok := r.barriers[value]
	if !ok {
		return nil, false
	}
	delete(r.barriers, value)
	return drained, true
}

// activeTokens reports the in-flight registrations. It exists for cleanup
// tests.
func (r *progressRelay) activeTokens() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// notify relays one internal-client progress notification to the host session
// registered for its token. Notifications whose call has already returned, or
// whose token was never registered, are dropped.
func (r *progressRelay) notify(ctx context.Context, request *mcp.ProgressNotificationClientRequest) {
	if r == nil || request == nil || request.Params == nil {
		return
	}
	if drained, ok := r.takeBarrier(request.Params.ProgressToken); ok {
		if drained != nil {
			close(drained)
		}
		return
	}
	hostSession := r.target(request.Params.ProgressToken)
	if hostSession == nil {
		return
	}
	if err := hostSession.NotifyProgress(ctx, request.Params); err != nil {
		slog.Debug("upstream proxy: relaying progress notification failed", "error", err)
	}
}

func (f *clientForwarder) callTool(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if f == nil || f.client == nil {
		return nil, errMountClosed
	}
	token := toolProgressToken(request)
	f.relay.register(token, requestSession(request))
	defer f.relay.release(ctx, token, requestSession(request))
	result, err := f.client.CallTool(ctx, forwardedToolParams(request))
	return result, preserveUpstreamError(err)
}

func (f *clientForwarder) readResource(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if f == nil || f.client == nil {
		return nil, errMountClosed
	}
	token := resourceProgressToken(request)
	f.relay.register(token, resourceSession(request))
	defer f.relay.release(ctx, token, resourceSession(request))
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

// forwardedToolParams rebuilds a tools/call for the internal client. Everything
// the host request exposes is copied through: arguments, _meta (including the
// progress token), input responses, and request state. Sharing the caller's
// _meta map is unsafe because the internal client may add the SEP-2575 request
// triple to it, so the map is cloned. RequestExtra is populated by the
// transport and has no client-side equivalent here.
//
// The proxy resolves its own roots negotiation from input responses first; any
// roots response it consumed is harmless to the upstream, which never asked for
// that input request. Upstream elicitation and sampling input requests are
// forwarded, but the internal client has no handlers to fulfill them.
func forwardedToolParams(request *mcp.CallToolRequest) *mcp.CallToolParams {
	params := new(mcp.CallToolParams)
	if request == nil || request.Params == nil {
		return params
	}
	params.Name = request.Params.Name
	params.Arguments = request.Params.Arguments
	params.Meta = cloneMeta(request.Params.GetMeta())
	params.InputResponses = request.Params.InputResponses
	params.RequestState = request.Params.RequestState
	return params
}

// forwardedResourceParams rebuilds a resources/read for the internal client.
func forwardedResourceParams(request *mcp.ReadResourceRequest) *mcp.ReadResourceParams {
	params := new(mcp.ReadResourceParams)
	if request == nil || request.Params == nil {
		return params
	}
	params.URI = request.Params.URI
	params.Meta = cloneMeta(request.Params.GetMeta())
	params.InputResponses = request.Params.InputResponses
	params.RequestState = request.Params.RequestState
	return params
}

// forwardedInputState decides which caller input state reaches asimonim.
//
// On the call that establishes the internal client, asimonim has not issued
// any input request yet, so caller input responses cannot answer one. The only
// possible input state is the proxy's roots handshake, plus anything a hostile
// or unusual client added. Forwarding the latter would make asimonim skip its
// own roots negotiation and silently use the server working directory, so the
// whole map and state are dropped; asimonim's own MRTR then resolves roots from
// the internal client's advertised roots.
//
// On later calls the client is established, so only the proxy's own roots
// handshake is removed and every other caller response is preserved.
func forwardedInputState(fresh bool, responses mcp.InputResponseMap, state string) (mcp.InputResponseMap, string) {
	if fresh {
		return nil, ""
	}
	return stripProxyRootsState(responses, state)
}

// stripProxyRootsState removes the proxy's own roots handshake from the state
// forwarded to asimonim. The proxy already used those roots to connect the
// internal client, and letting its request state reach asimonim would collide
// with asimonim's own multi-round-trip roots negotiation ("roots retry did not
// include the asimonim/roots input response"). Every other caller input
// response and request state is preserved.
func stripProxyRootsState(responses mcp.InputResponseMap, state string) (mcp.InputResponseMap, string) {
	if state == rootsRequestState {
		state = ""
	}
	if _, ok := responses[rootsInputRequestKey]; !ok {
		return responses, state
	}
	filtered := make(mcp.InputResponseMap, len(responses))
	for key, response := range responses {
		if key == rootsInputRequestKey {
			continue
		}
		filtered[key] = response
	}
	if len(filtered) == 0 {
		filtered = nil
	}
	return filtered, state
}

// asimonimSession is the internal client scope for one host session.
type asimonimSession struct {
	mu        sync.Mutex
	client    *mcp.ClientSession
	server    *mcp.ServerSession
	relay     *progressRelay
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

	anonymousMu     sync.Mutex
	anonymousClient *mcp.ClientSession
	anonymousRelay  *progressRelay
}

func (f *asimonimForwarder) callTool(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	client, relay, fresh, needsInput, err := f.ensure(ctx, requestSession(request), toolInputResponses(request), toolRequestState(request))
	if err != nil {
		return nil, err
	}
	if needsInput {
		return rootsInputRequiredToolResult(), nil
	}
	token := toolProgressToken(request)
	relay.register(token, requestSession(request))
	defer relay.release(ctx, token, requestSession(request))
	params := forwardedToolParams(request)
	params.InputResponses, params.RequestState = forwardedInputState(fresh, params.InputResponses, params.RequestState)
	result, err := client.CallTool(ctx, params)
	return result, preserveUpstreamError(err)
}

func (f *asimonimForwarder) readResource(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	client, relay, fresh, needsInput, err := f.ensure(ctx, resourceSession(request), resourceInputResponses(request), resourceRequestState(request))
	if err != nil {
		return nil, err
	}
	if needsInput {
		return rootsInputRequiredResourceResult(), nil
	}
	token := resourceProgressToken(request)
	relay.register(token, resourceSession(request))
	defer relay.release(ctx, token, resourceSession(request))
	params := forwardedResourceParams(request)
	params.InputResponses, params.RequestState = forwardedInputState(fresh, params.InputResponses, params.RequestState)
	result, err := client.ReadResource(ctx, params)
	return result, preserveUpstreamError(err)
}

// ensure returns the internal client for the host session, creating it with
// that session's roots on first use. fresh reports that this call established
// the client, and needsInput reports that the host client must answer a roots
// input request first.
func (f *asimonimForwarder) ensure(
	ctx context.Context,
	hostSession *mcp.ServerSession,
	responses mcp.InputResponseMap,
	state string,
) (*mcp.ClientSession, *progressRelay, bool, bool, error) {
	if hostSession == nil {
		client, relay, err := f.anonymous(ctx)
		return client, relay, false, false, err
	}

	f.state.mu.Lock()
	if f.state.closed {
		f.state.mu.Unlock()
		return nil, nil, false, false, errMountClosed
	}
	session := f.state.sessions[hostSession]
	if session == nil {
		session = new(asimonimSession)
		f.state.sessions[hostSession] = session
		go f.reap(hostSession, session)
	}
	f.state.mu.Unlock()

	// Hold the session lock across roots resolution and connection so
	// concurrent first calls share one roots round trip and one internal
	// client instead of dialing twice.
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil, nil, false, false, errMountClosed
	}
	if session.client != nil {
		return session.client, session.relay, false, false, nil
	}

	roots, advertiseRoots, needsInput := f.rootsFor(ctx, hostSession, responses, state)
	if needsInput {
		return nil, nil, false, true, nil
	}

	relay := newProgressRelay()
	client, serverSession, err := connectInternalClient(
		ctx,
		f.server,
		"rhds-mcp-asimonim-proxy",
		hostProtocol(hostSession),
		roots,
		advertiseRoots,
		relay,
	)
	if err != nil {
		return nil, nil, false, false, err
	}
	session.relay = relay
	session.client = client
	session.server = serverSession
	return client, relay, true, false, nil
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
			// Match asimonim's cwd fallback but keep the failure visible on
			// stderr; a silently wrong workspace is worse than a warning.
			// Direct registration also records the failure in result _meta,
			// but the proxy does not reach into asimonim's meta shape.
			slog.Warn("asimonim proxy: roots/list failed; falling back to the server working directory",
				"session", hostSession.ID(), "error", err)
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
// Failures are not cached, so a transient connect error does not poison later
// requests.
func (f *asimonimForwarder) anonymous(ctx context.Context) (*mcp.ClientSession, *progressRelay, error) {
	f.anonymousMu.Lock()
	defer f.anonymousMu.Unlock()
	if f.anonymousClient != nil {
		return f.anonymousClient, f.anonymousRelay, nil
	}

	f.state.mu.Lock()
	closed := f.state.closed
	f.state.mu.Unlock()
	if closed {
		return nil, nil, errMountClosed
	}

	relay := newProgressRelay()
	client, serverSession, err := connectInternalClient(
		ctx,
		f.server,
		"rhds-mcp-asimonim-proxy",
		defaultInternalProtocol,
		nil,
		false,
		relay,
	)
	if err != nil {
		return nil, nil, err
	}
	f.anonymousClient = client
	f.anonymousRelay = relay
	f.state.own(client, serverSession)
	return client, relay, nil
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

func toolProgressToken(request *mcp.CallToolRequest) any {
	if request == nil || request.Params == nil {
		return nil
	}
	return request.Params.GetProgressToken()
}

func resourceProgressToken(request *mcp.ReadResourceRequest) any {
	if request == nil || request.Params == nil {
		return nil
	}
	return request.Params.GetProgressToken()
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
