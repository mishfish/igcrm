FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /igcrm ./cmd/igcrm

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H app
COPY --from=build /igcrm /usr/local/bin/igcrm
USER app
EXPOSE 8080
ENTRYPOINT ["igcrm"]
CMD ["serve"]
