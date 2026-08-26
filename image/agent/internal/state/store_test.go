package state

import (
	"testing"
	"time"
)

func TestSubscribeReceivesInitialAndUpdates(t *testing.T) {
	s := NewStore(Initial("test"))
	ch, cancel := s.Subscribe()
	defer cancel()

	first := <-ch
	if first.ExpressVPN.Connection != ConnDisconnected {
		t.Fatalf("initial connection=%v", first.ExpressVPN.Connection)
	}

	s.Update(func(st *State) { st.ExpressVPN.Connection = ConnConnecting })
	select {
	case got := <-ch:
		if got.ExpressVPN.Connection != ConnConnecting {
			t.Fatalf("got %v", got.ExpressVPN.Connection)
		}
	case <-time.After(time.Second):
		t.Fatal("no update within 1s")
	}
}

func TestSlowSubscriberGetsLatest(t *testing.T) {
	s := NewStore(Initial("test"))
	ch, cancel := s.Subscribe()
	defer cancel()
	<-ch

	// Подписчик не читает: несколько обновлений подряд коалесцируются.
	s.Update(func(st *State) { st.ExpressVPN.Connection = ConnConnecting })
	s.Update(func(st *State) { st.ExpressVPN.Connection = ConnConnected })
	got := <-ch
	if got.ExpressVPN.Connection != ConnConnected {
		t.Fatalf("got %v, want latest snapshot", got.ExpressVPN.Connection)
	}
}

func TestTwoSubscribers(t *testing.T) {
	s := NewStore(Initial("test"))
	ch1, c1 := s.Subscribe()
	ch2, c2 := s.Subscribe()
	defer c1()
	defer c2()
	<-ch1
	<-ch2
	s.Update(func(st *State) { st.Uplink.Status = UplinkUp })
	for i, ch := range []<-chan State{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Uplink.Status != UplinkUp {
				t.Fatalf("sub%d got %v", i+1, got.Uplink.Status)
			}
		case <-time.After(time.Second):
			t.Fatalf("sub%d: no update", i+1)
		}
	}
}
