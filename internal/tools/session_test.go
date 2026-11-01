// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package tools

import (
	"context"
	"os"
	"testing"
)

func TestRequestStateRejectsForgery(t *testing.T) {
	state := requestState{Stage: "elicit", Root: "/tmp/example"}
	encoded := encodeRequestState(state)
	decoded, ok := decodeRequestState(encoded)
	if !ok {
		t.Fatal("decodeRequestState() rejected a state this process encoded")
	}
	if decoded.Stage != state.Stage || decoded.Root != state.Root {
		t.Errorf("decoded = %+v, want %+v", decoded, state)
	}
	if !decoded.authenticated {
		t.Error("decoded state is not marked authenticated")
	}

	tampered := encoded + "A"
	if _, ok := decodeRequestState(tampered); ok {
		t.Error("decodeRequestState() accepted a tampered signature")
	}
	if _, ok := decodeRequestState("eyJzdGFnZSI6ImVsaWNpdCIsInJvb3QiOiIvdG1wIn0"); ok {
		t.Error("decodeRequestState() accepted unsigned state")
	}
}

func TestAcquireRootIgnoresStateRootWithoutRootsCapability(t *testing.T) {
	bogus := t.TempDir()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	service := New(nil, nil)

	root, warning, required, failure := service.acquireRoot(
		context.Background(),
		&callEnv{},
		requestState{Stage: "elicit", Root: bogus},
	)
	if failure != nil || required != nil {
		t.Fatalf("acquireRoot() failure = %+v required = %+v", failure, required)
	}
	if root == bogus {
		t.Errorf("root = %q, want the bogus client root rejected", root)
	}
	if root != workingDirectory {
		t.Errorf("root = %q, want the working directory fallback %q", root, workingDirectory)
	}
	if warning == "" {
		t.Error("warning is empty, want the documented working directory notice")
	}
}

func TestAcquireRootIgnoresUnnegotiatedStateRootWithRootsCapability(t *testing.T) {
	bogus := t.TempDir()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	service := New(nil, nil)

	root, _, required, failure := service.acquireRoot(
		context.Background(),
		&callEnv{rootsCapability: true, protocolVersion: protocolVersion20251125},
		requestState{Stage: "elicit", Root: bogus},
	)
	if failure != nil || required != nil {
		t.Fatalf("acquireRoot() failure = %+v required = %+v", failure, required)
	}
	if root == bogus {
		t.Errorf("root = %q, want the unnegotiated client root rejected", root)
	}
	if root != workingDirectory {
		t.Errorf("root = %q, want the working directory fallback %q", root, workingDirectory)
	}
}

func TestAcquireRootAcceptsAuthenticatedElicitationRoot(t *testing.T) {
	negotiated := t.TempDir()
	state, ok := decodeRequestState(encodeRequestState(requestState{
		Stage: "elicit",
		Root:  negotiated,
	}))
	if !ok {
		t.Fatal("decodeRequestState() rejected a signed elicitation state")
	}
	service := New(nil, nil)

	root, warning, required, failure := service.acquireRoot(
		context.Background(),
		&callEnv{rootsCapability: true, protocolVersion: protocolVersion20260728},
		state,
	)
	if failure != nil || required != nil {
		t.Fatalf("acquireRoot() failure = %+v required = %+v", failure, required)
	}
	if root != negotiated {
		t.Errorf("root = %q, want the negotiated root %q", root, negotiated)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none", warning)
	}
}

func TestAcquireRootAsksForRootsInsteadOfAcceptingStateRoot(t *testing.T) {
	bogus := t.TempDir()
	service := New(nil, nil)

	root, _, required, failure := service.acquireRoot(
		context.Background(),
		&callEnv{rootsCapability: true, protocolVersion: protocolVersion20260728},
		requestState{Root: bogus},
	)
	if failure != nil {
		t.Fatalf("acquireRoot() failure = %+v", failure)
	}
	if root != "" {
		t.Errorf("root = %q, want no root before the roots negotiation", root)
	}
	if required == nil || required.InputRequests[inputRootsID] == nil {
		t.Fatalf("required = %+v, want a roots input request", required)
	}
}
