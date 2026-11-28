ARG NEWPRESS_GO_IMAGE=golang:1.26.11-bookworm@sha256:ea47ee45f1284d60f9cc09cb2c3bb4f508c256b640bd8bb30342ff13d1adcb52
ARG NEWPRESS_RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6
FROM ${NEWPRESS_GO_IMAGE} AS build

RUN apt-get update \
    && apt-get install -y --no-install-recommends --only-upgrade libpam0g libpam-modules libpam-modules-bin libpam-runtime \
    && for package in libpam0g libpam-modules libpam-modules-bin libpam-runtime; do \
         dpkg --compare-versions "$(dpkg-query -W -f='${Version}' "$package")" ge '1.5.2-6+deb12u3' || exit 1; \
       done \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
COPY third_party/xcrypto ./third_party/xcrypto
ARG GOPROXY=https://proxy.golang.org
ARG GOSUMDB=sum.golang.org
RUN GOTOOLCHAIN=local GOPROXY="${GOPROXY}" GOSUMDB="${GOSUMDB}" go mod download

COPY . .
ARG VERSION=1.0.0-beta.1
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ARG TARGETOS
ARG TARGETARCH
RUN GOTOOLCHAIN=local GOPROXY="${GOPROXY}" GOSUMDB="${GOSUMDB}" CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath -buildvcs=false \
    -ldflags="-s -w -X newpress.local/newpress/internal/version.Version=${VERSION} -X newpress.local/newpress/internal/version.Commit=${COMMIT} -X newpress.local/newpress/internal/version.BuildDate=${BUILD_DATE}" \
    -o /out/newpress ./cmd/newpress
# Stage an owned theme root without sample themes or local selection state.
RUN mkdir -p /out/updates /out/var /out/themes && cp -a themes/chunnel /out/themes/chunnel

FROM ${NEWPRESS_RUNTIME_IMAGE}

WORKDIR /app
ENV NEWPRESS_UPDATE_DIR=/app/updates NEWPRESS_CONTAINER=true
COPY --from=build --chown=65532:65532 /out/newpress /newpress
COPY --from=build --chown=65532:65532 /out/updates /app/updates
COPY --from=build --chown=65532:65532 /out/var /app/var
# Copy the root too, so the non-root service can install additional themes.
COPY --from=build --chown=65532:65532 /out/themes /app/themes
COPY --chown=65532:65532 plugins /app/plugins
COPY --from=build /src/LICENSE /licenses/newpress/LICENSE
COPY --from=build /src/THIRD_PARTY_NOTICES.md /licenses/newpress/THIRD_PARTY_NOTICES.md
COPY --from=build /src/internal/licensing/THIRD_PARTY_LICENSES.txt /licenses/newpress/THIRD_PARTY_LICENSES.txt
COPY --from=build /src/internal/licensing/GO_MODULES.json /licenses/newpress/GO_MODULES.json
COPY --from=build /src/third_party/xcrypto/LICENSE /licenses/newpress/third_party/xcrypto/LICENSE
COPY --from=build /src/third_party/licenses /licenses/newpress/third_party/licenses
COPY --from=build /src/internal/coreui/THIRD_PARTY_NOTICES.md /licenses/newpress/internal/coreui/THIRD_PARTY_NOTICES.md
COPY --from=build /src/internal/commentorigin/flags/LICENSE /licenses/newpress/internal/commentorigin/flags/LICENSE
COPY --from=build /src/internal/commentorigin/flags/README.md /licenses/newpress/internal/commentorigin/flags/README.md

USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=4s --start-period=10s --retries=4 CMD ["/newpress", "healthcheck", "--ready"]
ENTRYPOINT ["/newpress"]
CMD ["run"]
