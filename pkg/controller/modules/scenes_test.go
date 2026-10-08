package modules

import (
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
	names map[int]string
}

func (l *sceneListenerStub) SceneEventsStart(digitalstrom.SceneEventCallback) error { return nil }
func (l *sceneListenerStub) SceneEventsStop()                                       {}
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
		listener:            &sceneListenerStub{names: map[int]string{17: "Movie"}},
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
	if topic := module.sceneEventTopic(payload.Zone, payload.Group); topic != path.Join("scenes", "Living_Room", "lights", "event") {
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
	if lights.StateTopic != "digitalstrom/scenes/Living_Room/lights/event" {
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
