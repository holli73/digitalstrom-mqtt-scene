# Changelog

## 2.4.0-scene.7

- Goes back to the code of 2.4.0-scene.5 to check a regression of 2.4.0-scene.6. The
  reworked version stays on the `scene-calls` branch.

## 2.4.0-scene.5

- Logs the App version and the built commit at startup.

## 2.4.0-scene.4

- Creates Home Assistant scene entities per zone (off and presets 1-4 for lights, shades,
  audio, video and joker) and for the main apartment scenes. Custom scene names are used
  when set; unnamed presets 2-4 are disabled by default.

## 2.4.0-scene.3

- Calls digitalSTROM scenes from MQTT on `digitalstrom/scenes/{zone}/{group}/command`
  (payload: scene number, standard name like `preset1`, or custom scene name).

## 2.4.0-scene.2

- Publishes scene events with the topic and payload of version 1.x on
  `digitalstrom/scenes/{zoneName}/{sceneName}/event`.
- Moves the Home Assistant event entities to `digitalstrom/scene_events/{zoneId}/{group}/event`.

## 2.4.0-scene.1

- Publishes digitalSTROM scene calls to MQTT and exposes them as Home Assistant event entities.
- Builds the App from the source of the holli73/digitalstrom-mqtt-scene fork.

## 2.4.0-haos.1

- Initial native Home Assistant App package.
- Published as a stable, unofficial community App.
- Uses the Home Assistant MQTT service and existing MQTT Discovery support.
- Supports a manually configured MQTT broker without requiring the Mosquitto App.
- Keeps the digitalSTROM password field visible and clears its value after setup.
- Enables automatic updates once for the regular App and preserves later manual choices.
- Creates and persists a dedicated digitalSTROM API key in the App data directory.
- Recovers interrupted or incomplete API-key setup without overwriting a valid key.
- Waits for temporary password cleanup before starting the bridge and retries without another login.
- Keeps passwords, API keys, and Supervisor option payloads out of App logs.
- Supports a configurable digitalSTROM HTTPS API port.
- Adds actionable API-key bootstrap and recovery logs without exposing credentials.
- Includes Home Assistant store icon and logo artwork.
- Runs under a custom AppArmor profile based on an enforce-mode profile validated
  on Home Assistant OS; CI parses the final packaged profile.
- Adds a Docker health check through a dependency-independent liveness endpoint.
- Enables the watchdog after successful setup and detects blocked bridge work.
- Pauses automatic restarts for rejected credentials and preserves manual watchdog choices.
- Retries an unavailable MQTT service and restores paused watchdog protection after startup crashes.
- Rejects unsupported MQTT service TLS instead of starting with an unusable connection.
- Includes complete English and German setup, migration, and removal guidance.
