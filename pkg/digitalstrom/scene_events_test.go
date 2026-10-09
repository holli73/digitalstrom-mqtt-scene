package digitalstrom

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestSceneEventsLoginSubscribeAndPoll(t *testing.T) {
	var mu sync.Mutex
	var subscribed []string
	polls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		query := request.URL.Query()
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/json/system/loginApplication":
			if query.Get("loginToken") != "test-api-key" {
				_, _ = writer.Write([]byte(`{"ok":false,"message":"Application-Authentication failed"}`))
				return
			}
			_, _ = writer.Write([]byte(`{"ok":true,"result":{"token":"session-token"}}`))
		case "/json/event/subscribe":
			if query.Get("token") != "session-token" {
				t.Errorf("unexpected token %q", query.Get("token"))
			}
			subscribed = append(subscribed, query.Get("name"))
			_, _ = writer.Write([]byte(`{"ok":true}`))
		case "/json/event/get":
			polls++
			if polls == 1 {
				_, _ = writer.Write([]byte(`{"ok":true,"result":{"events":[
					{"name":"callScene","properties":{"sceneID":"5","groupID":"1","zoneID":"1234","forced":"false","originDSUID":"abc"},"source":{"zoneID":1234,"groupID":1}},
					{"name":"undoScene","properties":{"sceneID":"72","groupID":"0","zoneID":"0"},"source":{"zoneID":0,"groupID":0}},
					{"name":"buttonClick","properties":{}}
				]}}`))
				return
			}
			time.Sleep(20 * time.Millisecond)
			_, _ = writer.Write([]byte(`{"ok":true,"result":{}}`))
		case "/json/event/unsubscribe":
			_, _ = writer.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()

	serverURL, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(serverURL.Port())
	listener := newSceneEvents(ClientOptions{Host: serverURL.Hostname(), Port: port, ApiKey: "test-api-key"})
	listener.httpClient = server.Client()

	received := make(chan SceneEvent, 10)
	if err := listener.start(func(event SceneEvent) { received <- event }); err != nil {
		t.Fatalf("start: %v", err)
	}
	var events []SceneEvent
	for len(events) < 2 {
		select {
		case event := <-received:
			events = append(events, event)
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout waiting for events, got %v", events)
		}
	}
	listener.stop()

	if events[0] != (SceneEvent{Event: EventTypeCallScene, ZoneId: 1234, GroupId: 1, SceneId: 5, OriginId: "abc"}) {
		t.Fatalf("unexpected first event %+v", events[0])
	}
	if events[1] != (SceneEvent{Event: EventTypeUndoScene, ZoneId: 0, GroupId: 0, SceneId: 72}) {
		t.Fatalf("unexpected second event %+v", events[1])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(subscribed) != 2 || subscribed[0] != "callScene" || subscribed[1] != "undoScene" {
		t.Fatalf("unexpected subscriptions %v", subscribed)
	}
}

func TestParseSceneEventFallsBackToSource(t *testing.T) {
	event, ok := parseSceneEvent(legacyEvent{
		Name:       "callScene",
		Properties: map[string]interface{}{"sceneID": "17"},
		Source:     map[string]interface{}{"zoneID": float64(42), "groupID": float64(2)},
	})
	if !ok {
		t.Fatal("expected event to be parsed")
	}
	if event.ZoneId != 42 || event.GroupId != 2 || event.SceneId != 17 {
		t.Fatalf("unexpected event %+v", event)
	}
}

func TestParseSceneEventIgnoresOtherEvents(t *testing.T) {
	if _, ok := parseSceneEvent(legacyEvent{Name: "buttonClick", Properties: map[string]interface{}{"sceneID": "5"}}); ok {
		t.Fatal("expected buttonClick to be ignored")
	}
	if _, ok := parseSceneEvent(legacyEvent{Name: "callScene", Properties: map[string]interface{}{}}); ok {
		t.Fatal("expected event without scene id to be ignored")
	}
}

func TestCallSceneRetriesWithNewSession(t *testing.T) {
	var calls []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		switch request.URL.Path {
		case "/json/system/loginApplication":
			calls = append(calls, "login")
			_, _ = writer.Write([]byte(`{"ok":true,"result":{"token":"new-token"}}`))
		case "/json/zone/callScene":
			calls = append(calls, "callScene "+query.Get("token")+" "+query.Get("id")+"/"+query.Get("groupID")+"/"+query.Get("sceneNumber"))
			if query.Get("token") != "new-token" {
				writer.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = writer.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()

	serverURL, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(serverURL.Port())
	events := newSceneEvents(ClientOptions{Host: serverURL.Hostname(), Port: port, ApiKey: "test-api-key"})
	events.httpClient = server.Client()
	events.token = "expired-token"

	if err := events.callScene(1234, 1, 5, false); err != nil {
		t.Fatalf("call scene: %v", err)
	}
	expected := []string{"callScene expired-token 1234/1/5", "login", "callScene new-token 1234/1/5"}
	if len(calls) != len(expected) {
		t.Fatalf("unexpected calls %v", calls)
	}
	for i := range expected {
		if calls[i] != expected[i] {
			t.Fatalf("unexpected calls %v", calls)
		}
	}
}
