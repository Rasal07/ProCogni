FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/statsjob ./cmd/statsjob
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mock ./cmd/mock

# Single static binary, no shell, no package manager.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /api
COPY --from=build /out/statsjob /statsjob
COPY --from=build /out/mock /mock
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/api"]
