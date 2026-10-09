package util

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGenerateBasicAuth(t *testing.T) {
	t.Run("positive: encodes user and password", func(t *testing.T) {
		if got, want := GenerateBasicAuth("admin", "s4cr4t"), "Basic YWRtaW46czRjcjR0"; got != want {
			t.Errorf("GenerateBasicAuth = %q, want %q", got, want)
		}
	})

	t.Run("negative: a missing credential yields no header", func(t *testing.T) {
		for _, creds := range [][2]string{{"", "s4cr4t"}, {"admin", ""}, {"", ""}} {
			if got := GenerateBasicAuth(creds[0], creds[1]); got != "" {
				t.Errorf("GenerateBasicAuth(%q, %q) = %q, want empty", creds[0], creds[1], got)
			}
		}
	})

	// RFC 7617 forbids ':' in the user-id: the server splits on the first one and sees another user.
	t.Run("negative: a colon in the user is not rejected", func(t *testing.T) {
		got := GenerateBasicAuth("ad:min", "s4cr4t")
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "Basic "))
		if err != nil {
			t.Fatalf("decode %q: %v", got, err)
		}
		if user, _, _ := strings.Cut(string(decoded), ":"); user != "ad" {
			t.Errorf("server-side user = %q, want the ambiguous %q", user, "ad")
		}
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestGenerateUniqueID(t *testing.T) {
	t.Run("positive: returns distinct IDs of the asked length", func(t *testing.T) {
		for _, length := range []int{1, 2, 20, maxIDLength} {
			id, err := GenerateUniqueID(length)
			if err != nil {
				t.Fatalf("GenerateUniqueID(%d): %v", length, err)
			}
			if len(id) != length {
				t.Errorf("GenerateUniqueID(%d) = %q, length %d", length, id, len(id))
			}
		}

		seen := make(map[string]bool, 1000)
		for range 1000 {
			id, err := GenerateUniqueID(20)
			if err != nil {
				t.Fatalf("GenerateUniqueID: %v", err)
			}
			if seen[id] {
				t.Fatalf("GenerateUniqueID returned a duplicate: %q", id)
			}
			seen[id] = true
		}
	})

	t.Run("negative: rejects a length out of range", func(t *testing.T) {
		for _, length := range []int{0, -2, maxIDLength + 1} {
			if _, err := GenerateUniqueID(length); err == nil {
				t.Errorf("GenerateUniqueID(%d) = nil error, want a length error", length)
			}
		}
	})

	t.Run("negative: propagates an entropy failure", func(t *testing.T) {
		uuid.SetRand(failingReader{})
		t.Cleanup(func() { uuid.SetRand(nil) })

		if _, err := GenerateUniqueID(20); err == nil {
			t.Error("GenerateUniqueID = nil error, want the entropy failure propagated")
		}
	})
}
