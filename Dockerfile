FROM golang:1.26.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/co ./cmd/co

FROM alpine:3
COPY --from=build /bin/co /bin/co
ENTRYPOINT ["/bin/co"]
