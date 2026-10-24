package main

import "testing"

func TestEDODestinationUsesTheBoundProcessInsteadOfAnExternalOrigin(t *testing.T) {
	for _, tc := range []struct{ address, want string }{
		{":8080", "http://127.0.0.1:8080/internal/edo/events"},
		{"0.0.0.0:18081", "http://127.0.0.1:18081/internal/edo/events"},
		{"localhost:18081", "http://127.0.0.1:18081/internal/edo/events"},
		{"127.0.0.2:8081", "http://127.0.0.2:8081/internal/edo/events"},
		{"[::]:8080", "http://[::1]:8080/internal/edo/events"},
		{"[::1]:18081", "http://[::1]:18081/internal/edo/events"},
	} {
		got, err := internalEDOURL(tc.address)
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %q, err %v", tc.address, got, err)
		}
	}
	for _, address := range []string{"", "8080", ":0", ":65536", ":http", "example.test:8080", "192.0.2.1:8080", "http://localhost:8080", "[::1]:-1"} {
		if got, err := internalEDOURL(address); err == nil {
			t.Fatalf("unsafe/unusable bind %q accepted as %q", address, got)
		}
	}
}
