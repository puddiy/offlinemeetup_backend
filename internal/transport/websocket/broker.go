package websocket

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// wsBroadcastChannel is the single Redis Pub/Sub channel all WS broadcast events
// flow through. Addressing (which users / which room) lives inside the envelope,
// so one channel is enough for the current chat volume.
const wsBroadcastChannel = "ws:broadcast"

// publishTimeout bounds a single best-effort publish so a hung Redis never
// blocks the goroutine that emits a chat event.
const publishTimeout = 2 * time.Second

// MessageBus is the transport seam the Hub uses to fan WS broadcasts across
// backend instances. Declared at the consumer (this package) and kept tiny so it
// can be backed by Redis in production and an in-process bus in tests.
type MessageBus interface {
	// Publish sends data to every subscriber of channel (best-effort).
	Publish(ctx context.Context, channel string, data []byte) error
	// Subscribe returns a channel of raw payloads for the given channels. The
	// returned channel is fed until ctx is cancelled, after which the underlying
	// subscription is torn down. Subscribe blocks until the subscription is
	// established, so a publish issued after it returns is not missed.
	Subscribe(ctx context.Context, channels ...string) (<-chan []byte, error)
}

// redisBus is the production MessageBus backed by Redis Pub/Sub.
type redisBus struct {
	rdb *redis.Client
}

// NewRedisBus builds a MessageBus over a Redis client.
func NewRedisBus(rdb *redis.Client) MessageBus {
	return &redisBus{rdb: rdb}
}

func (b *redisBus) Publish(ctx context.Context, channel string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	return b.rdb.Publish(ctx, channel, data).Err()
}

func (b *redisBus) Subscribe(ctx context.Context, channels ...string) (<-chan []byte, error) {
	pubsub := b.rdb.Subscribe(ctx, channels...)
	// Block until the subscription is confirmed so a publish that races with
	// Subscribe is not silently dropped (Pub/Sub is at-most-once).
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, err
	}

	out := make(chan []byte, 256)
	go func() {
		defer close(out)
		defer func() { _ = pubsub.Close() }()
		redisCh := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-redisCh:
				if !ok {
					return
				}
				select {
				case out <- []byte(msg.Payload):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// localBus is an in-process MessageBus for tests and single-binary setups
// without Redis. It fans a publish out to every live subscriber synchronously.
type localBus struct {
	mu   sync.Mutex
	subs map[string][]chan []byte
}

// NewLocalBus builds an in-process MessageBus (no Redis required).
func NewLocalBus() MessageBus {
	return &localBus{subs: make(map[string][]chan []byte)}
}

func (b *localBus) Publish(ctx context.Context, channel string, data []byte) error {
	b.mu.Lock()
	targets := append([]chan []byte(nil), b.subs[channel]...)
	b.mu.Unlock()

	for _, ch := range targets {
		select {
		case ch <- data:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (b *localBus) Subscribe(ctx context.Context, channels ...string) (<-chan []byte, error) {
	out := make(chan []byte, 256)

	b.mu.Lock()
	for _, c := range channels {
		b.subs[c] = append(b.subs[c], out)
	}
	b.mu.Unlock()

	// On cancellation, stop routing to this subscriber. We do not close out:
	// an in-flight Publish may still hold a copy of it, and the consumer exits
	// via its own ctx.Done() select.
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		for _, c := range channels {
			subs := b.subs[c]
			for i, ch := range subs {
				if ch == out {
					b.subs[c] = append(subs[:i], subs[i+1:]...)
					break
				}
			}
		}
		b.mu.Unlock()
	}()

	return out, nil
}
