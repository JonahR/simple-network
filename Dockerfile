# One Dockerfile for every service. Pick the binary with --build-arg SERVICE=<name>,
# where <name> is a directory under cmd/.
FROM golang:1.26-alpine AS build
ARG SERVICE
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot
ENTRYPOINT ["/app"]
