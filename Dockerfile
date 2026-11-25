# Build a small static binary (pure-Go SQLite, no CGO needed).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ref .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/ref /usr/local/bin/ref
ENV PORT=8080 DB_PATH=/data/ref.db
EXPOSE 8080
CMD ["ref"]
