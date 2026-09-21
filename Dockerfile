FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/free-opencode-api ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/free-opencode-api /free-opencode-api
EXPOSE 8080
ENTRYPOINT ["/free-opencode-api"]
