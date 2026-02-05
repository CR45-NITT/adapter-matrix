FROM golang:1.21-bookworm AS build
WORKDIR /src

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/adapter ./cmd/adapter

FROM gcr.io/distroless/base-debian12
COPY --from=build /out/adapter /adapter
USER nonroot:nonroot
ENTRYPOINT ["/adapter"]
