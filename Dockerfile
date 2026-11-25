FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG APP=api
# Cross-compile natively instead of emulating the target platform.
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${APP}
# The runtime image has no shell to create a writable log dir.
RUN mkdir -p /out/logs

# Dependabot cannot bump the Debian release in the image name; do it by hand.
FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /app
COPY --from=build /out/app /app/app
COPY --from=build --chown=65532:65532 /out/logs /app/logs

# Documentation only: the real ports come from server.port and metrics.port in the config.
EXPOSE 8080 9464

# The base's default, stated for scanners (Trivy DS-0002); numeric, as runAsNonRoot requires.
USER 65532:65532

# Exec form, so the binary is PID 1 and gets SIGTERM directly.
ENTRYPOINT ["/app/app"]
