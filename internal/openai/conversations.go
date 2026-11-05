package openai

import (
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/baldaworks/acpboss/internal/openai/api"
	"github.com/baldaworks/acpboss/internal/proxy"
)

var errConversationNotFound = errors.New("conversation not found")

type conversation struct {
	id, session string
	created     int64
	metadata    map[string]string
	busy        bool
}

// conversations holds routing metadata only. Native providers own dialogue history.
type conversations struct {
	mu      sync.Mutex
	entries map[string]*conversation
	limit   int
}

func newConversations(limit int) *conversations {
	return &conversations{entries: map[string]*conversation{}, limit: limit}
}
func copyMetadata(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for k, v := range input {
		result[k] = v
	}
	return result
}
func (c *conversation) snapshot() api.Conversation {
	var metadata *api.ConversationMetadata
	if c.metadata != nil {
		copy := api.ConversationMetadata(copyMetadata(c.metadata))
		metadata = &copy
	}
	return api.Conversation{Id: c.id, Object: "conversation", CreatedAt: c.created, Metadata: metadata}
}
func (r *conversations) add(session string, metadata map[string]string) (api.Conversation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) >= r.limit {
		return api.Conversation{}, proxy.ErrCapacity
	}
	c := &conversation{id: "conv_" + rand.Text(), session: session, created: time.Now().Unix(), metadata: copyMetadata(metadata)}
	r.entries[c.id] = c
	return c.snapshot(), nil
}
func (r *conversations) get(id string) (api.Conversation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.entries[id]
	if c == nil {
		return api.Conversation{}, errConversationNotFound
	}
	return c.snapshot(), nil
}
func (r *conversations) update(id string, metadata map[string]string) (api.Conversation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.entries[id]
	if c == nil {
		return api.Conversation{}, errConversationNotFound
	}
	c.metadata = copyMetadata(metadata)
	return c.snapshot(), nil
}
func (r *conversations) remove(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.entries[id]
	if c == nil {
		return "", errConversationNotFound
	}
	delete(r.entries, id)
	return c.session, nil
}

// acquire reserves selection, validation and prompting as a single HTTP turn.
// Its idempotent release never recreates a deleted handle or releases a newer turn.
func (r *conversations) acquire(id string) (string, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.entries[id]
	if c == nil {
		return "", nil, errConversationNotFound
	}
	if c.busy {
		return "", nil, proxy.ErrBusy
	}
	c.busy = true
	var once sync.Once
	release := func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.entries[id] == c {
				c.busy = false
			}
		})
	}
	return c.session, release, nil
}
