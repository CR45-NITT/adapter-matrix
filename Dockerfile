FROM golang:1.25.6 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags "-s -w" -o /out/adapter ./cmd/adapter

FROM gcr.io/distroless/base-debian12

WORKDIR /

COPY --from=builder /out/adapter /adapter

USER nonroot:nonroot

ENTRYPOINT ["/adapter"]
