package discovery

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestAnnounceAndDiscoverOverUnicast(t *testing.T) {
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.LocalAddr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Announce(ctx, addr, 19090)
	}()
	time.Sleep(minDiscoveryDelay)

	endpoints, err := Discover(context.Background(), addr, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 1 || endpoints[0] != "127.0.0.1:19090" {
		t.Fatalf("endpoints = %#v", endpoints)
	}
}

func TestParseAnnouncementRejectsInvalidPackets(t *testing.T) {
	tests := [][]byte{
		nil,
		[]byte("not-json"),
		[]byte(`{"magic":"other","port":9000}`),
		[]byte(`{"magic":"kigo-relay-v1","port":0}`),
		[]byte(`{"magic":"kigo-relay-v1","port":70000}`),
	}
	for _, payload := range tests {
		if port, ok := parseAnnouncement(payload); ok {
			t.Fatalf("parseAnnouncement(%q) = %d, true", payload, port)
		}
	}
}
