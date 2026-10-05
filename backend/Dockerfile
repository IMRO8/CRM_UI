FROM golang:1.22-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/*.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /server .
FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 ledger
WORKDIR /app
COPY --from=build /server ./server
COPY migrations ./migrations
USER ledger
EXPOSE 8080
ENTRYPOINT ["/app/server"]
