//go:build integration

package integration

import (
	"context"
	"testing"
)

func TestAudioIsTenantIsolatedAndPurged(t *testing.T) {
	ctx := context.Background()
	tenant, other := newID("au"), newID("au")
	data := []byte("RIFF....WAVEfmt fake recording")

	sum, changed, err := store.PutAudio(ctx, tenant, "call-1", "audio/wav", "user:it", data)
	if err != nil || !changed || len(sum) != 64 {
		t.Fatalf("put: %v %v %q", err, changed, sum)
	}
	if _, changed, _ := store.PutAudio(ctx, tenant, "call-1", "audio/wav", "user:it", data); changed {
		t.Fatal("the same bytes again must not count as a change")
	}

	a, found, err := store.GetAudio(ctx, tenant, "call-1")
	if err != nil || !found || string(a.Bytes) != string(data) || a.ContentType != "audio/wav" {
		t.Fatalf("get own: %v %v %+v", err, found, a)
	}
	// Row-level security: another tenant cannot read or delete it, even by ref.
	if _, found, err := store.GetAudio(ctx, other, "call-1"); err != nil || found {
		t.Fatalf("another tenant must not see the recording: %v %v", err, found)
	}
	if err := store.DeleteAudio(ctx, other, "call-1"); err == nil {
		t.Fatal("another tenant must not delete the recording")
	}

	// Nothing is older than an hour yet.
	if _, err := store.PurgeAudio(ctx, 1, 100); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.GetAudio(ctx, tenant, "call-1"); !found {
		t.Fatal("a fresh recording must survive the purge")
	}
	if _, err := store.PurgeAudio(ctx, 0, 100); err == nil {
		t.Fatal("a retention below one hour must be refused")
	}

	if err := store.DeleteAudio(ctx, tenant, "call-1"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.GetAudio(ctx, tenant, "call-1"); found {
		t.Fatal("a deleted recording must be gone")
	}
}
