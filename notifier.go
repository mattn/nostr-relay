package main

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/fiatjaf/eventstore"
	"github.com/nbd-wtf/go-nostr"
	"github.com/redis/go-redis/v9"
)

// redisNotifier propagates accepted events between instances through a Redis
// pub/sub channel. Unlike the postgresql backend's LISTEN/NOTIFY it does not
// depend on the storage, so instances on any driver (sqlite3, mysql,
// opensearch, ...) can share their live deliveries, and the storage does not
// even have to be the same database.
type redisNotifier struct {
	client  *redis.Client
	channel string
}

var _ eventstore.Notifier = (*redisNotifier)(nil)

func newRedisNotifier(rawURL, channel string) (*redisNotifier, error) {
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	return &redisNotifier{client: redis.NewClient(opts), channel: channel}, nil
}

// Notify publishes evt on the channel so that every instance, this one
// included, delivers it from Notifications.
func (n *redisNotifier) Notify(ctx context.Context, evt *nostr.Event) error {
	b, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	return n.client.Publish(ctx, n.channel, b).Err()
}

// Notifications subscribes to the channel and hands the events over to relayer,
// which matches them against the subscriptions of this instance's clients.
func (n *redisNotifier) Notifications(ctx context.Context) (<-chan *nostr.Event, error) {
	sub := n.client.Subscribe(ctx, n.channel)
	// wait for the subscription to be confirmed before returning, so that
	// events published from now on are not missed
	if _, err := sub.Receive(ctx); err != nil {
		sub.Close()
		return nil, err
	}

	ch := make(chan *nostr.Event)
	go func() {
		defer close(ch)
		defer sub.Close()
		// sub.Channel reconnects by itself if the connection drops
		msgs := sub.Channel()
		for {
			select {
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				var evt nostr.Event
				if err := json.Unmarshal([]byte(msg.Payload), &evt); err != nil {
					slog.Warn("dropping malformed redis notification", "err", err)
					continue
				}
				select {
				case ch <- &evt:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}
