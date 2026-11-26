# syntax=docker/dockerfile:1
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY *.go ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/pulse .

# Статический бинарник в distroless: нет shell, пакетного менеджера и root-пользователя.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pulse /pulse
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/pulse"]
