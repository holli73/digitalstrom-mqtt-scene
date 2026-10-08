package modules

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"

	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/config"
	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/digitalstrom"
	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/homeassistant"
	"github.com/gaetancollaud/digitalstrom-mqtt/pkg/mqtt"
	"github.com/rs/zerolog/log"
)

const (
	scenes          string = "scenes"
	sceneUndoEvent  string = "undo"
	apartmentZoneId int    = 0
	broadcastGroup  int    = 0
)

// digitalSTROM groups (colors) by id.
var sceneGroupNames = map[int]string{
	0:  "all",
	1:  "lights",
	2:  "shades",
	3:  "heating",
	4:  "audio",
	5:  "video",
	6:  "security",
	7:  "access",
	8:  "joker",
	9:  "cooling",
	10: "ventilation",
	11: "window",
	12: "recirculation",
}

// Smarthome API zone applications mapped to the digitalSTROM group id.
var sceneApplicationGroups = map[string]int{
	"lights":        1,
	"shades":        2,
	"awnings":       2,
	"heating":       3,
	"audio":         4,
	"video":         5,
	"security":      6,
	"access":        7,
	"joker":         8,
	"cooling":       9,
	"ventilation":   10,
	"window":        11,
	"recirculation": 12,
}

// Standard digitalSTROM scene numbers.
var standardSceneNames = map[int]string{
	0:  "preset0",
	1:  "area1_off",
	2:  "area2_off",
	3:  "area3_off",
	4:  "area4_off",
	5:  "preset1",
	6:  "area1_on",
	7:  "area2_on",
	8:  "area3_on",
	9:  "area4_on",
	10: "area_stepping_continue",
	11: "decrement",
	12: "increment",
	13: "minimum",
	14: "maximum",
	15: "stop",
	17: "preset2",
	18: "preset3",
	19: "preset4",
	20: "preset12",
	21: "preset13",
	22: "preset14",
	23: "preset22",
	24: "preset23",
	25: "preset24",
	26: "preset32",
	27: "preset33",
	28: "preset34",
	29: "preset42",
	30: "preset43",
	31: "preset44",
	32: "preset10",
	33: "preset11",
	34: "preset20",
	35: "preset21",
	36: "preset30",
	37: "preset31",
	38: "preset40",
	39: "preset41",
	40: "auto_off",
	50: "local_off",
	51: "local_on",
	52: "area1_stop",
	53: "area2_stop",
	54: "area3_stop",
	55: "area4_stop",
	56: "sun_protection",
	64: "auto_standby",
	65: "panic",
	66: "energy_overload",
	67: "standby",
	68: "deep_off",
	69: "sleeping",
	70: "wakeup",
	71: "present",
	72: "absent",
	73: "door_bell",
	74: "alarm1",
	75: "zone_active",
	76: "fire",
}

const maxSceneId = 127

// ScenePayload is the JSON message published on every scene call.
type ScenePayload struct {
	EventType string `json:"event_type"`
	Event     string `json:"event"`
	ZoneId    int    `json:"zone_id"`
	Zone      string `json:"zone"`
	GroupId   int    `json:"group_id"`
	Group     string `json:"group"`
	SceneId   int    `json:"scene_id"`
	Scene     string `json:"scene"`
	SceneName string `json:"scene_name,omitempty"`
	Forced    bool   `json:"forced"`
	OriginId  string `json:"origin_id,omitempty"`
}

// Scenes Module forwards the scene calls of digitalSTROM to MQTT. Each call
// is published (not retained) on scenes/{zone}/{group}/event.
type ScenesModule struct {
	mqttClient mqtt.Client
	dsClient   digitalstrom.Client
	dsRegistry digitalstrom.Registry

	enabled             bool
	normalizeDeviceName bool
	listener            digitalstrom.SceneEventListener
}

func (c *ScenesModule) Start() error {
	if !c.enabled {
		log.Info().Msg("Scenes module disabled.")
		return nil
	}
	listener, ok := c.dsClient.(digitalstrom.SceneEventListener)
	if !ok {
		log.Warn().Msg("Digitalstrom client does not support scene events. Scenes module disabled.")
		return nil
	}
	c.listener = listener
	return listener.SceneEventsStart(c.onSceneEvent)
}

func (c *ScenesModule) Stop() error {
	if c.listener != nil {
		c.listener.SceneEventsStop()
		c.listener = nil
	}
	return nil
}

func (c *ScenesModule) onSceneEvent(event digitalstrom.SceneEvent) {
	payload := c.buildPayload(event)
	log.Info().
		Str("event", payload.Event).
		Str("zone", payload.Zone).
		Str("group", payload.Group).
		Int("sceneId", payload.SceneId).
		Str("scene", payload.Scene).
		Msg("Scene event")

	message, err := json.Marshal(payload)
	if err != nil {
		log.Error().Err(err).Msg("Error serializing scene event")
		return
	}
	topic := c.mqttClient.GetFullTopic(c.sceneEventTopic(payload.Zone, payload.Group))
	// Events must never be retained, otherwise they would be replayed on
	// every reconnect.
	t := c.mqttClient.RawClient().Publish(topic, mqtt.QOS, false, message)
	go func() {
		<-t.Done()
		if t.Error() != nil {
			log.Error().Err(t.Error()).Str("topic", topic).Msg("Error publishing scene event")
		}
	}()
}

func (c *ScenesModule) buildPayload(event digitalstrom.SceneEvent) ScenePayload {
	scene := sceneKey(event.SceneId)
	eventType := scene
	if event.Event == digitalstrom.EventTypeUndoScene {
		eventType = sceneUndoEvent
	}
	sceneName := ""
	if c.listener != nil {
		name, err := c.listener.SceneName(event.ZoneId, event.GroupId, event.SceneId)
		if err != nil {
			log.Debug().Err(err).Msg("Unable to get scene name")
		}
		sceneName = name
	}
	return ScenePayload{
		EventType: eventType,
		Event:     string(event.Event),
		ZoneId:    event.ZoneId,
		Zone:      c.zoneName(event.ZoneId),
		GroupId:   event.GroupId,
		Group:     groupName(event.GroupId),
		SceneId:   event.SceneId,
		Scene:     scene,
		SceneName: sceneName,
		Forced:    event.Forced,
		OriginId:  event.OriginId,
	}
}

func (c *ScenesModule) zoneName(zoneId int) string {
	if zoneId == apartmentZoneId {
		return "apartment"
	}
	if zones, err := c.dsRegistry.GetZones(); err == nil {
		for _, zone := range zones {
			if zone.ZoneId == strconv.Itoa(zoneId) && zone.Attributes.Name != "" {
				return zone.Attributes.Name
			}
		}
	}
	return fmt.Sprintf("zone_%d", zoneId)
}

func (c *ScenesModule) sceneEventTopic(zoneName string, group string) string {
	if c.normalizeDeviceName {
		zoneName = normalizeForTopicName(zoneName)
	}
	return path.Join(scenes, zoneName, group, mqtt.Event)
}

func groupName(groupId int) string {
	if name, ok := sceneGroupNames[groupId]; ok {
		return name
	}
	return fmt.Sprintf("group_%d", groupId)
}

func sceneKey(sceneId int) string {
	if name, ok := standardSceneNames[sceneId]; ok {
		return name
	}
	return fmt.Sprintf("scene_%d", sceneId)
}

func sceneEventTypes() []string {
	types := make([]string, 0, maxSceneId+2)
	for id := 0; id <= maxSceneId; id++ {
		types = append(types, sceneKey(id))
	}
	return append(types, sceneUndoEvent)
}

func (c *ScenesModule) GetHomeAssistantEntities() ([]homeassistant.DiscoveryConfig, error) {
	if !c.enabled {
		return nil, nil
	}
	configs := []homeassistant.DiscoveryConfig{
		c.eventEntity(apartmentZoneId, broadcastGroup),
	}

	zones, err := c.dsRegistry.GetZones()
	if err != nil {
		return nil, err
	}
	for _, zone := range zones {
		zoneId, err := strconv.Atoi(zone.ZoneId)
		if err != nil || zoneId == apartmentZoneId {
			continue
		}
		configs = append(configs, c.eventEntity(zoneId, broadcastGroup))
		seen := map[int]bool{}
		for _, application := range zone.Attributes.Applications {
			groupId, ok := sceneApplicationGroups[application]
			if !ok || seen[groupId] {
				continue
			}
			seen[groupId] = true
			configs = append(configs, c.eventEntity(zoneId, groupId))
		}
	}
	return configs, nil
}

func (c *ScenesModule) eventEntity(zoneId int, groupId int) homeassistant.DiscoveryConfig {
	zoneName := c.zoneName(zoneId)
	group := groupName(groupId)
	deviceId := fmt.Sprintf("digitalstrom_zone_%d", zoneId)
	objectId := "scene_" + group
	return homeassistant.DiscoveryConfig{
		Domain:   homeassistant.Event,
		DeviceId: deviceId,
		ObjectId: objectId,
		Config: &homeassistant.EventConfig{
			BaseConfig: homeassistant.BaseConfig{
				Device: homeassistant.Device{
					Identifiers: []string{deviceId},
					Model:       "Zone",
					Name:        zoneName,
				},
				Name:     group + " scene",
				UniqueId: deviceId + "_" + objectId,
			},
			StateTopic: c.mqttClient.GetFullTopic(c.sceneEventTopic(zoneName, group)),
			EventTypes: sceneEventTypes(),
			Icon:       "mdi:palette",
		},
	}
}

func NewScenesModule(mqttClient mqtt.Client, dsClient digitalstrom.Client, dsRegistry digitalstrom.Registry, config *config.Config) Module {
	return &ScenesModule{
		mqttClient:          mqttClient,
		dsClient:            dsClient,
		dsRegistry:          dsRegistry,
		enabled:             config.ScenesEnabled,
		normalizeDeviceName: config.Mqtt.NormalizeDeviceName,
	}
}

func init() {
	Register(scenes, NewScenesModule)
}
