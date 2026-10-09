package modules

import (
	"testing"

	mqtt_base "github.com/eclipse/paho.mqtt.golang"
	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/config"
	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/digitalstrom"
	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/homeassistant"
	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/mqtt"
)

type scenePublish struct {
	topic   string
	message string
	retain  bool
}

type sceneRawClientStub struct {
	mqtt_base.Client
	published []scenePublish
}

func (c *sceneRawClientStub) Publish(topic string, _ byte, retained bool, payload interface{}) mqtt_base.Token {
	c.published = append(c.published, scenePublish{topic, string(payload.([]byte)), retained})
	return &mqtt_base.DummyToken{}
}

type sceneMQTTClientStub struct {
	mqtt.Client
	raw sceneRawClientStub
}

func (c *sceneMQTTClientStub) GetFullTopic(topic string) string { return "digitalstrom/" + topic }
func (c *sceneMQTTClientStub) RawClient() mqtt_base.Client      { return &c.raw }

func TestScenesV1PayloadAndTopic(t *testing.T) {
	mqttClient := &sceneMQTTClientStub{}
	module := &ScenesModule{mqttClient: mqttClient, normalizeDeviceName: true}

	module.onSceneCall(digitalstrom.SceneCall{ZoneId: 1234, ZoneName: "Wohn Zimmer", GroupId: 1, SceneId: 17, SceneName: "Movie"})
	module.onSceneCall(digitalstrom.SceneCall{ZoneId: 0, ZoneName: "unnamed-zone-0", GroupId: 0, SceneId: 72, SceneName: "unnamed-scene-72"})
	module.onSceneCall(digitalstrom.SceneCall{ZoneId: 1234, ZoneName: "Wohn Zimmer", GroupId: -1, SceneId: 5})

	expected := []scenePublish{
		{"digitalstrom/scenes/Wohn_Zimmer/Movie/event", `{"ZoneId":1234,"ZoneName":"Wohn Zimmer","GroupId":1,"GroupName":"light","SceneId":17,"SceneName":"Movie"}`, false},
		{"digitalstrom/scenes/unnamed-zone-0/unnamed-scene-72/event", `{"ZoneId":0,"ZoneName":"unnamed-zone-0","GroupId":0,"GroupName":"unknown","SceneId":72,"SceneName":"unnamed-scene-72"}`, false},
		{"digitalstrom/scenes/Wohn_Zimmer/5/event", `{"ZoneId":1234,"ZoneName":"Wohn Zimmer","GroupId":-1,"GroupName":"unknown","SceneId":5,"SceneName":""}`, false},
	}
	if len(mqttClient.raw.published) != len(expected) {
		t.Fatalf("unexpected publishes %+v", mqttClient.raw.published)
	}
	for i := range expected {
		if mqttClient.raw.published[i] != expected[i] {
			t.Errorf("publish %d: got %+v, expected %+v", i, mqttClient.raw.published[i], expected[i])
		}
	}
}

func TestScenesTopicWithoutWildcards(t *testing.T) {
	module := &ScenesModule{}

	topic := module.sceneEventTopic(SceneEvent{ZoneName: "Living Room", SceneName: "TV + Licht #2 Auf/Zu"})
	if topic != "scenes/Living Room/TV _ Licht _2 Auf_Zu/event" {
		t.Fatalf("unexpected topic %s", topic)
	}
}

func TestScenesDisabledDoesNotCreateLegacyClient(t *testing.T) {
	module := NewScenesModule(&sceneMQTTClientStub{}, nil, nil, &config.Config{ScenesEnabled: false}).(*ScenesModule)

	if err := module.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if module.legacyClient != nil {
		t.Fatal("legacy client created while scenes are disabled")
	}
	if err := module.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

type sceneRegistryStub struct {
	digitalstrom.Registry
	zones []digitalstrom.Zone
}

func (r *sceneRegistryStub) GetApartmentId() string        { return "apartment-1" }
func (r *sceneRegistryStub) GetZones() []digitalstrom.Zone { return r.zones }

type sceneLegacyClientStub struct {
	digitalstrom.LegacyClient
	calls [][3]int
}

func (c *sceneLegacyClientStub) CallScene(zoneId int, groupId int, sceneId int) error {
	c.calls = append(c.calls, [3]int{zoneId, groupId, sceneId})
	return nil
}

func newSceneCommandTestModule() (*ScenesModule, *sceneLegacyClientStub) {
	legacyClient := &sceneLegacyClientStub{}
	return &ScenesModule{
		mqttClient: &deviceMQTTClientStub{prefix: "digitalstrom"},
		dsRegistry: &sceneRegistryStub{zones: []digitalstrom.Zone{
			{ZoneId: "1234", Attributes: digitalstrom.ZoneAttributes{Name: "Living Room", Applications: []string{"lights", "shades", "awnings", "heating"}}},
			{ZoneId: "0", Attributes: digitalstrom.ZoneAttributes{Name: "Apartment", Applications: []string{"lights"}}},
		}},
		enabled:      true,
		legacyClient: legacyClient,
	}, legacyClient
}

func TestScenesCommand(t *testing.T) {
	module, legacyClient := newSceneCommandTestModule()

	for topic, payload := range map[string]string{
		"digitalstrom/zones/1234/light/scene/command": "5",
		"digitalstrom/zones/1234/shade/scene/command": " 0\n",
		"digitalstrom/zones/0/all/scene/command":      "72",
	} {
		if err := module.onSceneCommand(topic, payload); err != nil {
			t.Fatalf("%s: %v", topic, err)
		}
	}
	expected := map[[3]int]bool{{1234, 1, 5}: true, {1234, 2, 0}: true, {0, 0, 72}: true}
	if len(legacyClient.calls) != len(expected) {
		t.Fatalf("unexpected calls %v", legacyClient.calls)
	}
	for _, call := range legacyClient.calls {
		if !expected[call] {
			t.Errorf("unexpected call %v", call)
		}
	}
}

func TestScenesCommandErrors(t *testing.T) {
	module, legacyClient := newSceneCommandTestModule()

	for topic, payload := range map[string]string{
		"digitalstrom/zones/9999/light/scene/command":   "5",
		"digitalstrom/zones/Living/light/scene/command": "5",
		"digitalstrom/zones/1234/lights/scene/command":  "5",
		"digitalstrom/zones/1234/light/scene/command":   "preset1",
		"digitalstrom/zones/0/all/scene/command":        "128",
	} {
		if err := module.onSceneCommand(topic, payload); err == nil {
			t.Errorf("%s %q: expected an error", topic, payload)
		}
	}
	if len(legacyClient.calls) != 0 {
		t.Fatalf("unexpected calls %v", legacyClient.calls)
	}
}

func TestScenesHomeAssistantEntities(t *testing.T) {
	module, _ := newSceneCommandTestModule()

	configs, err := module.GetHomeAssistantEntities()
	if err != nil {
		t.Fatalf("entities: %v", err)
	}
	// 7 apartment scenes, and 5 scenes for the lights and the shades of the
	// living room (no entities for heating, awnings are shades).
	if len(configs) != 7+2*5 {
		t.Fatalf("unexpected number of entities %d", len(configs))
	}

	absent := configs[1]
	config := absent.Config.(*homeassistant.SceneConfig)
	if absent.DeviceId != "apartment-1_zone_0" || absent.ObjectId != "scene_all_72" ||
		config.Name != "Absent" || config.UniqueId != "apartment-1_zone_0_scene_all_72" ||
		config.CommandTopic != "digitalstrom/zones/0/all/scene/command" || config.PayloadOn != "72" ||
		!config.EnabledByDefault {
		t.Fatalf("unexpected apartment entity %+v %+v", absent, config)
	}

	byObjectId := map[string]*homeassistant.SceneConfig{}
	for _, c := range configs[7:] {
		if c.DeviceId != "apartment-1_zone_1234" {
			t.Fatalf("unexpected device %s", c.DeviceId)
		}
		byObjectId[c.ObjectId] = c.Config.(*homeassistant.SceneConfig)
	}
	preset1 := byObjectId["scene_light_5"]
	if preset1 == nil || preset1.Name != "Light Preset 1" || preset1.Device.Name != "Living Room" ||
		preset1.CommandTopic != "digitalstrom/zones/1234/light/scene/command" || !preset1.EnabledByDefault {
		t.Fatalf("unexpected preset 1 entity %+v", preset1)
	}
	if preset2 := byObjectId["scene_shade_17"]; preset2 == nil || preset2.Name != "Shade Preset 2" || preset2.EnabledByDefault {
		t.Fatalf("unexpected preset 2 entity %+v", preset2)
	}
}
