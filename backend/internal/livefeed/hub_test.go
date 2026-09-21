package livefeed

import (
	"testing"
	"time"
)

func TestHub_PublishDeliversToSubscriber(t *testing.T) {
	hub := NewHub()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	want := Event{Type: EventTargetStatus, TargetID: "t-1", State: "alerting", Streak: 3, OccurredAt: time.Now()}
	hub.Publish(want)

	select {
	case got := <-events:
		if got.TargetID != want.TargetID || got.State != want.State || got.Streak != want.Streak {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber never received the published event")
	}
}

func TestHub_PublishFansOutToEverySubscriber(t *testing.T) {
	hub := NewHub()
	eventsA, unsubA := hub.Subscribe()
	defer unsubA()
	eventsB, unsubB := hub.Subscribe()
	defer unsubB()

	hub.Publish(Event{Type: EventIncident, TargetID: "t-2", Kind: "opened", IncidentID: 42})

	for _, ch := range []<-chan Event{eventsA, eventsB} {
		select {
		case got := <-ch:
			if got.Kind != "opened" || got.IncidentID != 42 {
				t.Fatalf("got %+v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("a subscriber never received the fan-out event")
		}
	}
}

func TestHub_UnsubscribeStopsDeliveryAndClosesChannel(t *testing.T) {
	hub := NewHub()
	events, unsubscribe := hub.Subscribe()
	if got := hub.SubscriberCount(); got != 1 {
		t.Fatalf("expected 1 subscriber, got %d", got)
	}

	unsubscribe()
	if got := hub.SubscriberCount(); got != 0 {
		t.Fatalf("expected 0 subscribers after unsubscribe, got %d", got)
	}

	hub.Publish(Event{Type: EventTargetStatus, TargetID: "t-3"})

	if _, ok := <-events; ok {
		t.Fatal("expected channel to be closed with no further events after unsubscribe")
	}
}

func TestHub_SlowSubscriberDropsRatherThanBlocksPublish(t *testing.T) {
	hub := NewHub()
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < subscriberBuffer+10; i++ {
			hub.Publish(Event{Type: EventTargetStatus, TargetID: "t-4", Streak: i})
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber buffer instead of dropping")
	}

	// Drain whatever made it through — proves the subscriber still works,
	// just didn't receive every one of the flood above.
	drained := 0
	for {
		select {
		case <-events:
			drained++
		default:
			if drained == 0 {
				t.Fatal("expected at least some events to have been delivered before the buffer filled")
			}
			return
		}
	}
}
