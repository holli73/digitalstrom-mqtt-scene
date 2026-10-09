package modules

import (
	"encoding/json"
	"path"
	"testing"

	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/digitalstrom"
	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/homeassistant"
)

type sceneRegistryStub struct {
	digitalstrom.Registry
	zones []digitalstrom.Zone
}

func (r *sceneRegistryStub) GetZones() ([]digitalstrom.Zone, error) {
	return r.zones, nil
}

type sceneListenerStub struct {
	digitalstrom.Client
	names     map[int]string
	zoneNames map[int]string
}

func (l *sceneListenerStub) SceneEventsStart(digitalstrom.SceneEventCallback) error { return nil }
func (l *sceneListenerStub) SceneEventsStop()                                       {}
func (l *sceneListenerStub) ZoneName(zoneId int) (string, error) {
	return l.zoneNames[zoneId], nil
}
func (l *sceneListenerStub) SceneName(_ int, _ int, sceneId int) (string, error) {
	return l.names[sceneId], nil
}

func newSceneTestModule() *ScenesModule {
	return &ScenesModule{
		mqttClient: &deviceMQTTClientStub{prefix: "digitalstrom"},
		dsRegistry: &sceneRegistryStub{zones: []digitalstrom.Zone{
			{ZoneId: "1234", Attributes: digitalstrom.ZoneAttributes{Name: "Living Room", Applications: []string{"lights", "shades", "awnings", "unknown"}}},
			{ZoneId: "0", Attributes: digitalstrom.ZoneAttributes{Name: "Apartment"}},
		}},
		enabled:             true,
		normalizeDeviceName: true,
		listener: &sceneListenerStub{
			names:     map[int]string{17: "Movie"},
			zoneNames: map[int]string{1234: "Wohn Zimmer"},
		},
	}
}

func TestScenesBuildPayload(t *testing.T) {
	module := newSceneTestModule()

	payload := module.buildPayload(digitalstrom.SceneEvent{
		Event: digitalstrom.EventTypeCallScene, ZoneId: 1234, GroupId: 1, SceneId: 17,
	})

	expected := ScenePayload{
		EventType: "preset2", Event: "callScene",
		ZoneId: 1234, Zone: "Living Room",
		GroupId: 1, Group: "lights",
		SceneId: 17, Scene: "preset2", SceneName: "Movie",
	}
	if payload != expected {
		t.Fatalf("unexpected payload %+v", payload)
	}
	if topic := module.sceneEventTopic(payload.ZoneId, payload.Group); topic != path.Join("scene_events", "1234", "lights", "event") {
		t.Fatalf("unexpected topic %s", topic)
	}
}

func TestScenesBuildPayloadUndoAndUnknownZone(t *testing.T) {
	module := newSceneTestModule()

	payload := module.buildPayload(digitalstrom.SceneEvent{
		Event: digitalstrom.EventTypeUndoScene, ZoneId: 99, GroupId: 42, SceneId: 100,
	})

	if payload.EventType != "undo" || payload.Zone != "zone_99" || payload.Group != "group_42" || payload.Scene != "scene_100" {
		t.Fatalf("unexpected payload %+v", payload)
	}
}

func TestScenesHomeAssistantEntities(t *testing.T) {
	module := newSceneTestModule()

	configs, err := module.GetHomeAssistantEntities()
	if err != nil {
		t.Fatalf("get entities: %v", err)
	}

	var ids []string
	for _, cfg := range configs {
		if cfg.Domain != homeassistant.Event {
			t.Fatalf("unexpected domain %s", cfg.Domain)
		}
		ids = append(ids, cfg.DeviceId+"/"+cfg.ObjectId)
	}
	expected := []string{
		"digitalstrom_zone_0/scene_all",
		"digitalstrom_zone_1234/scene_all",
		"digitalstrom_zone_1234/scene_lights",
		"digitalstrom_zone_1234/scene_shades",
	}
	if len(ids) != len(expected) {
		t.Fatalf("unexpected entities %v", ids)
	}
	for i := range expected {
		if ids[i] != expected[i] {
			t.Fatalf("unexpected entities %v", ids)
		}
	}

	lights := configs[2].Config.(*homeassistant.EventConfig)
	if lights.StateTopic != "digitalstrom/scene_events/1234/lights/event" {
		t.Fatalf("unexpected state topic %s", lights.StateTopic)
	}
	if lights.Device.Name != "Living Room" {
		t.Fatalf("unexpected device name %s", lights.Device.Name)
	}
	if len(lights.EventTypes) != maxSceneId+2 || lights.EventTypes[5] != "preset1" || lights.EventTypes[maxSceneId+1] != "undo" {
		t.Fatalf("unexpected event types %v", lights.EventTypes)
	}
}

func TestScenesDisabled(t *testing.T) {
	module := newSceneTestModule()
	module.enabled = false

	configs, err := module.GetHomeAssistantEntities()
	if err != nil || len(configs) != 0 {
		t.Fatalf("expected no entities, got %v, %v", configs, err)
	}
}

func TestScenesV1PayloadAndTopic(t *testing.T) {
	module := newSceneTestModule()

	named := module.buildV1Payload(digitalstrom.SceneEvent{
		Event: digitalstrom.EventTypeCallScene, ZoneId: 1234, GroupId: 1, SceneId: 17,
	}, "Movie")
	expected := SceneEventV1{ZoneId: 1234, ZoneName: "Wohn Zimmer", GroupId: 1, GroupName: "light", SceneId: 17, SceneName: "Movie"}
	if named != expected {
		t.Fatalf("unexpected payload %+v", named)
	}
	if topic := module.sceneEventV1Topic(named); topic != "scenes/Wohn_Zimmer/Movie/event" {
		t.Fatalf("unexpected topic %s", topic)
	}
	message, _ := json.Marshal(named)
	if string(message) != `{"ZoneId":1234,"ZoneName":"Wohn Zimmer","GroupId":1,"GroupName":"light","SceneId":17,"SceneName":"Movie"}` {
		t.Fatalf("unexpected message %s", message)
	}

	unnamed := module.buildV1Payload(digitalstrom.SceneEvent{
		Event: digitalstrom.EventTypeCallScene, ZoneId: 0, GroupId: 0, SceneId: 72,
	}, "")
	if unnamed.ZoneName != "unnamed-zone-0" || unnamed.GroupName != "unknown" {
		t.Fatalf("unexpected payload %+v", unnamed)
	}
	if topic := module.sceneEventV1Topic(unnamed); topic != "scenes/unnamed-zone-0/72/event" {
		t.Fatalf("unexpected topic %s", topic)
	}
}

type sceneCall struct {
	zoneId, groupId, sceneId int
}

type sceneCallerStub struct {
	calls []sceneCall
}

func (c *sceneCallerStub) CallScene(zoneId int, groupId int, sceneId int, _ bool) error {
	c.calls = append(c.calls, sceneCall{zoneId, groupId, sceneId})
	return nil
}

func TestScenesCommand(t *testing.T) {
	tests := []struct {
		topic   string
		payload string
		want    sceneCall
	}{
		{"digitalstrom/scenes/Living_Room/light/command", "5", sceneCall{1234, 1, 5}},
		{"digitalstrom/scenes/living room/lights/command", "preset1", sceneCall{1234, 1, 5}},
		{"digitalstrom/scenes/Wohn_Zimmer/shade/command", " Preset2 ", sceneCall{1234, 2, 17}},
		{"digitalstrom/scenes/1234/1/command", "movie", sceneCall{1234, 1, 17}},
		{"digitalstrom/scenes/apartment/all/command", "absent", sceneCall{0, 0, 72}},
		{"digitalstrom/scenes/unnamed-zone-42/climate/command", "0", sceneCall{42, 3, 0}},
	}
	for _, test := range tests {
		module := newSceneTestModule()
		caller := &sceneCallerStub{}
		module.caller = caller

		if err := module.onSceneCommand(test.topic, test.payload); err != nil {
			t.Fatalf("%s %q: %v", test.topic, test.payload, err)
		}
		if len(caller.calls) != 1 || caller.calls[0] != test.want {
			t.Fatalf("%s %q: unexpected calls %v", test.topic, test.payload, caller.calls)
		}
	}
}

func TestScenesCommandErrors(t *testing.T) {
	tests := []struct {
		topic   string
		payload string
	}{
		{"digitalstrom/scenes/Kitchen/light/command", "5"},
		{"digitalstrom/scenes/Living_Room/garden/command", "5"},
		{"digitalstrom/scenes/Living_Room/light/command", "Party"},
		{"digitalstrom/scenes/Living_Room/light/command", "128"},
		{"digitalstrom/scenes/Living_Room/light/command", ""},
	}
	for _, test := range tests {
		module := newSceneTestModule()
		caller := &sceneCallerStub{}
		module.caller = caller

		if err := module.onSceneCommand(test.topic, test.payload); err == nil {
			t.Fatalf("%s %q: expected an error", test.topic, test.payload)
		}
		if len(caller.calls) != 0 {
			t.Fatalf("%s %q: unexpected calls %v", test.topic, test.payload, caller.calls)
		}
	}
}
