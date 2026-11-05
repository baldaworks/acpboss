package openai

import (
	"errors"
	"testing"

	"github.com/baldaworks/acpboss/internal/proxy"
)

func TestConversationTurnReservation(t *testing.T) {
	server, h := httpFixture(t)
	handle := createHandle(t, server.URL)
	id, finish, err := h.prepareTurn(t.Context(), handle, "alpha/org/model")
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if _, _, err := h.prepareTurn(t.Context(), handle, "beta/other"); !errors.Is(err, proxy.ErrBusy) {
		t.Fatal(err)
	}
	other := createHandle(t, server.URL)
	otherID, otherFinish, err := h.prepareTurn(t.Context(), other, "beta/other")
	if err != nil || otherID == id {
		t.Fatal(otherID, err)
	}
	otherFinish()
	finish()
	if _, _, err := h.prepareTurn(t.Context(), handle, "missing/model"); !errors.Is(err, proxy.ErrUnknownModel) {
		t.Fatal(err)
	}
	reused, release, err := h.prepareTurn(t.Context(), handle, "beta/other")
	if err != nil || reused != id {
		t.Fatal(reused, err)
	}
	release()
	if _, err := h.router.Snapshot(id); err != nil {
		t.Fatal("lease removed native session", err)
	}
	if _, _, err := h.prepareTurn(t.Context(), "conv_missing", "alpha/org/model"); !errors.Is(err, errConversationNotFound) {
		t.Fatal(err)
	}
}
