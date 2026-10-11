# One Dockerfile for every service. Pick the binary with --build-arg SERVICE=<name>,
# where <name> is a directory under cmd/.
FROM golang:1.26.3-alpine AS build
ARG SERVICE
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${SERVICE}
# An empty directory owned by nonroot, so a volume mounted there is writable.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
COPY --from=build --chown=nonroot:nonroot /out/data /data
# Services save their state here (internal/store); mount a volume to keep it.
ENV SN_DATA_DIR=/data
USER nonroot
ENTRYPOINT ["/app"]
