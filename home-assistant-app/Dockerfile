ARG BUILD_FROM=ghcr.io/home-assistant/base:3.23

FROM --platform=$BUILDPLATFORM golang:1.25.0-alpine AS build

ARG TARGETOS
ARG TARGETARCH
# Home Assistant only sends this App directory to the builder, so the bridge
# source is fetched from the fork. BUILD_VERSION is referenced so that bumping
# the App version invalidates the cached checkout.
ARG BUILD_VERSION
ARG SOURCE_REPO=https://github.com/holli73/digitalstrom-mqtt-scene.git
ARG SOURCE_REF=master

RUN apk add --no-cache git

WORKDIR /build

RUN echo "Fetching ${SOURCE_REPO}@${SOURCE_REF} for ${BUILD_VERSION}" \
  && git clone --depth 1 --branch "${SOURCE_REF}" "${SOURCE_REPO}" .
RUN go mod download
RUN COMMIT="$(git rev-parse --short HEAD)" \
  && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags="-s -w -X main.version=${BUILD_VERSION:-dev} -X main.commit=${COMMIT}" \
    -o /out/digitalstrom-mqtt ./main.go

FROM ${BUILD_FROM}

ARG BUILD_ARCH
ARG BUILD_VERSION

COPY --from=build /out/digitalstrom-mqtt /digitalstrom-mqtt
COPY run.sh /run.sh

RUN chmod 0755 /digitalstrom-mqtt /run.sh

LABEL \
  io.hass.type="app" \
  io.hass.name="digitalSTROM MQTT (scenes)" \
  io.hass.description="Unofficial digitalSTROM MQTT bridge for Home Assistant" \
  io.hass.url="https://github.com/holli73/digitalstrom-mqtt-scene/tree/master/home-assistant-app" \
  io.hass.version="${BUILD_VERSION}" \
  io.hass.arch="${BUILD_ARCH}" \
  org.opencontainers.image.title="digitalSTROM MQTT" \
  org.opencontainers.image.description="digitalSTROM MQTT bridge for Home Assistant" \
  org.opencontainers.image.source="https://github.com/holli73/digitalstrom-mqtt-scene" \
  org.opencontainers.image.licenses="AGPL-3.0-or-later"

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD curl --fail --silent --show-error http://127.0.0.1:8080/health/live || exit 1

CMD ["/run.sh"]
