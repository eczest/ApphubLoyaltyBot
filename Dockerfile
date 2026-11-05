FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/apphub-bot ./cmd/bot

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && addgroup -S apphub && adduser -S -G apphub apphub
WORKDIR /app
COPY --from=build /out/apphub-bot /app/apphub-bot
USER apphub:apphub
ENTRYPOINT ["/app/apphub-bot"]
