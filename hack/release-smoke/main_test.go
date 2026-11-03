// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package main

import "testing"

func TestCheckPackages(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"four packages", `{"packages":[{},{},{},{}]}`, false},
		{"error payload", `{"error":{"code":"no_bundled_data"},"packages":[]}`, true},
		{"missing packages", `{"packages":[{},{},{}]}`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := checkPackages(test.body)
			if (err != nil) != test.wantErr {
				t.Fatalf("checkPackages() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestCheckResolved(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"resolved", `{"resolved":{"elements":"5.0.0"}}`, false},
		{"error payload", `{"error":{"code":"no_bundled_data"},"resolved":{}}`, true},
		{"empty resolution", `{"resolved":{}}`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := checkResolved(test.body)
			if (err != nil) != test.wantErr {
				t.Fatalf("checkResolved() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
