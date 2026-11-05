package openai

import (
	"errors"
	"sync"
	"testing"

	"github.com/baldaworks/acpboss/internal/proxy"
)

func TestConversationRegistryOwnership(t *testing.T) {
	r := newConversations(1)
	metadata := map[string]string{"topic": "first"}
	c, err := r.add("native-logical-session", metadata)
	if err != nil {
		t.Fatal(err)
	}
	metadata["topic"] = "mutated"
	got, err := r.get(c.Id)
	if err != nil || (*got.Metadata)["topic"] != "first" {
		t.Fatal(got, err)
	}
	(*got.Metadata)["topic"] = "mutated snapshot"
	got, err = r.get(c.Id)
	if err != nil || (*got.Metadata)["topic"] != "first" {
		t.Fatal(got, err)
	}
	if _, err := r.add("another", nil); !errors.Is(err, proxy.ErrCapacity) {
		t.Fatal(err)
	}
	sid, release, err := r.acquire(c.Id)
	if err != nil || sid != "native-logical-session" {
		t.Fatal(sid, err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, _, err := r.acquire(c.Id); !errors.Is(err, proxy.ErrBusy) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	release()
	_, releaseNext, err := r.acquire(c.Id)
	if err != nil {
		t.Fatal(err)
	}
	release() // A stale duplicate release must not clear the newer lease.
	if _, _, err := r.acquire(c.Id); !errors.Is(err, proxy.ErrBusy) {
		t.Fatal(err)
	}
	removed, err := r.remove(c.Id)
	if err != nil || removed != sid {
		t.Fatal(removed, err)
	}
	releaseNext()
	if _, err := r.get(c.Id); !errors.Is(err, errConversationNotFound) {
		t.Fatal(err)
	}
	if _, _, err := r.acquire(c.Id); !errors.Is(err, errConversationNotFound) {
		t.Fatal(err)
	}
	if _, err := r.update(c.Id, nil); !errors.Is(err, errConversationNotFound) {
		t.Fatal(err)
	}
	if _, err := r.remove(c.Id); !errors.Is(err, errConversationNotFound) {
		t.Fatal(err)
	}
	next, err := r.add("new", nil)
	if err != nil || next.Id == c.Id || next.Metadata != nil {
		t.Fatal(next, err)
	}
	value := map[string]string{"topic": "new"}
	if _, err := r.update(next.Id, value); err != nil {
		t.Fatal(err)
	}
	value["topic"] = "external mutation"
	got, err = r.get(next.Id)
	if err != nil || (*got.Metadata)["topic"] != "new" {
		t.Fatal(got, err)
	}
}
