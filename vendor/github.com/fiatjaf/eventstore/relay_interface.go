package eventstore

import (
	"context"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
)

// RelayInterface is a wrapper thing that unifies Store and nostr.Relay under a common API.
type RelayInterface interface {
	Publish(ctx context.Context, event nostr.Event) (nostr.Status, error)
	QuerySync(ctx context.Context, filter nostr.Filter, opts ...nostr.SubscriptionOption) ([]*nostr.Event, error)
}

type RelayWrapper struct {
	Store
}

var _ RelayInterface = (*RelayWrapper)(nil)

func (w RelayWrapper) Publish(ctx context.Context, evt nostr.Event) (nostr.Status, error) {
	if 20000 <= evt.Kind && evt.Kind < 30000 {
		// do not store ephemeral events
		return nostr.PublishStatusSucceeded, nil
	} else if evt.Kind == 0 || evt.Kind == 3 || (10000 <= evt.Kind && evt.Kind < 20000) {
		// replaceable event, delete before storing
		ch, err := w.Store.QueryEvents(ctx, nostr.Filter{Authors: []string{evt.PubKey}, Kinds: []int{evt.Kind}})
		if err != nil {
			return nostr.PublishStatusFailed, fmt.Errorf("failed to query before replacing: %w", err)
		}
		if previous := <-ch; previous != nil && isOlder(previous, &evt) {
			if err := w.Store.DeleteEvent(ctx, previous); err != nil {
				return nostr.PublishStatusFailed, fmt.Errorf("failed to delete event for replacing: %w", err)
			}
		}
	} else if 30000 <= evt.Kind && evt.Kind < 40000 {
		// parameterized replaceable event, delete before storing
		d := evt.Tags.GetFirst([]string{"d", ""})
		if d != nil {
			ch, err := w.Store.QueryEvents(ctx, nostr.Filter{Authors: []string{evt.PubKey}, Kinds: []int{evt.Kind}, Tags: nostr.TagMap{"d": []string{d.Value()}}})
			if err != nil {
				return nostr.PublishStatusFailed, fmt.Errorf("failed to query before parameterized replacing: %w", err)
			}
			if previous := <-ch; previous != nil && isOlder(previous, &evt) {
				if err := w.Store.DeleteEvent(ctx, previous); err != nil {
					return nostr.PublishStatusFailed,
						fmt.Errorf("failed to delete event for parameterized replacing: %w", err)
				}
			}
		}
	}

	if err := w.SaveEvent(ctx, &evt); err != nil && err != ErrDupEvent {
		return nostr.PublishStatusFailed, fmt.Errorf("failed to save: %w", err)
	}

	return nostr.PublishStatusSucceeded, nil
}

func (w RelayWrapper) QuerySync(ctx context.Context, filter nostr.Filter, opts ...nostr.SubscriptionOption) ([]*nostr.Event, error) {
	ch, err := w.Store.QueryEvents(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query: %w", err)
	}

	n := filter.Limit
	if n == 0 {
		n = 500
	}

	results := make([]*nostr.Event, 0, n)
	for evt := range ch {
		results = append(results, evt)
	}

	return results, nil
}
